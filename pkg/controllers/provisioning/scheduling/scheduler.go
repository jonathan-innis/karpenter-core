/*
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scheduling

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/samber/lo"
	"go.uber.org/multierr"
	v1 "k8s.io/api/core/v1"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	schedulingevents "github.com/aws/karpenter-core/pkg/controllers/provisioning/scheduling/events"
	"github.com/aws/karpenter-core/pkg/controllers/state"
	"github.com/aws/karpenter-core/pkg/events"
	"github.com/aws/karpenter-core/pkg/scheduling"
	nodepoolutil "github.com/aws/karpenter-core/pkg/utils/nodepool"
	"github.com/aws/karpenter-core/pkg/utils/resources"
)

// SchedulerOptions can be used to control the scheduling, these options are currently only used during consolidation.
type SchedulerOptions struct {
	// SimulationMode if true will prevent recording of the pod nomination decisions as events
	SimulationMode bool
}

func NewScheduler(ctx context.Context, kubeClient client.Client, machines []*NodeClaimTemplate,
	nodePools []v1beta1.NodePool, cluster *state.Cluster, stateNodes []*state.StateNode, topology *Topology,
	instanceTypes map[string][]*cloudprovider.InstanceType, daemonSetPods []*v1.Pod,
	recorder events.Recorder, opts SchedulerOptions) *Scheduler {

	// if any of the nodePools add a taint with a prefer no schedule effect, we add a toleration for the taint
	// during preference relaxation
	toleratePreferNoSchedule := false
	for _, prov := range nodePools {
		for _, taint := range prov.Spec.Template.Spec.Taints {
			if taint.Effect == v1.TaintEffectPreferNoSchedule {
				toleratePreferNoSchedule = true
			}
		}
	}

	s := &Scheduler{
		ctx:                ctx,
		kubeClient:         kubeClient,
		machineTemplates:   machines,
		topology:           topology,
		cluster:            cluster,
		instanceTypes:      instanceTypes,
		daemonOverhead:     getDaemonOverhead(machines, daemonSetPods),
		recorder:           recorder,
		opts:               opts,
		preferences:        &Preferences{ToleratePreferNoSchedule: toleratePreferNoSchedule},
		remainingResources: map[string]v1.ResourceList{},
	}
	for _, nodePool := range nodePools {
		s.remainingResources[nodePool.Name] = v1.ResourceList(nodePool.Spec.Limits)
	}
	s.calculateExistingMachines(stateNodes, daemonSetPods)
	return s
}

type Scheduler struct {
	ctx                context.Context
	newNodeClaims      []*NodeClaim
	existingNodes      []*ExistingNode
	machineTemplates   []*NodeClaimTemplate
	remainingResources map[string]v1.ResourceList // provisioner name -> remaining resources for that provisioner
	instanceTypes      map[string][]*cloudprovider.InstanceType
	daemonOverhead     map[*NodeClaimTemplate]v1.ResourceList
	preferences        *Preferences
	topology           *Topology
	cluster            *state.Cluster
	recorder           events.Recorder
	opts               SchedulerOptions
	kubeClient         client.Client
}

// Results contains the results of the scheduling operation
type Results struct {
	NewNodeClaims []*NodeClaim
	ExistingNodes []*ExistingNode
	PodErrors     map[*v1.Pod]error
}

func (r Results) AllPodsScheduled() bool {
	return len(r.PodErrors) == 0
}

// PodSchedulingErrors creates a string that describes why pods wouldn't schedule that is suitable for presentation
func (r Results) PodSchedulingErrors() string {
	if len(r.PodErrors) == 0 {
		return "No Pod Scheduling Errors"
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "not all pods would schedule, ")
	const MaxErrors = 5
	numErrors := 0
	for k, err := range r.PodErrors {
		fmt.Fprintf(&msg, "%s/%s => %s ", k.Namespace, k.Name, err)
		numErrors++
		if numErrors >= MaxErrors {
			fmt.Fprintf(&msg, " and %d other(s)", len(r.PodErrors)-MaxErrors)
			break
		}
	}
	return msg.String()
}

func (s *Scheduler) Solve(ctx context.Context, pods []*v1.Pod) (*Results, error) {
	// We loop trying to schedule unschedulable pods as long as we are making progress.  This solves a few
	// issues including pods with affinity to another pod in the batch. We could topo-sort to solve this, but it wouldn't
	// solve the problem of scheduling pods where a particular order is needed to prevent a max-skew violation. E.g. if we
	// had 5xA pods and 5xB pods were they have a zonal topology spread, but A can only go in one zone and B in another.
	// We need to schedule them alternating, A, B, A, B, .... and this solution also solves that as well.
	errors := map[*v1.Pod]error{}
	q := NewQueue(pods...)
	for {
		// Try the next pod
		pod, ok := q.Pop()
		if !ok {
			break
		}

		// Schedule to existing nodes or create a new node
		if errors[pod] = s.add(ctx, pod); errors[pod] == nil {
			continue
		}

		// If unsuccessful, relax the pod and recompute topology
		relaxed := s.preferences.Relax(ctx, pod)
		q.Push(pod, relaxed)
		if relaxed {
			if err := s.topology.Update(ctx, pod); err != nil {
				logging.FromContext(ctx).Errorf("updating topology, %s", err)
			}
		}
	}

	for _, m := range s.newNodeClaims {
		m.FinalizeScheduling()
	}
	if !s.opts.SimulationMode {
		s.recordSchedulingResults(ctx, pods, q.List(), errors)
	}
	// clear any nil errors so we can know that len(PodErrors) == 0 => all pods scheduled
	for k, v := range errors {
		if v == nil {
			delete(errors, k)
		}
	}
	return &Results{
		NewNodeClaims: s.newNodeClaims,
		ExistingNodes: s.existingNodes,
		PodErrors:     errors,
	}, nil
}

func (s *Scheduler) recordSchedulingResults(ctx context.Context, pods []*v1.Pod, failedToSchedule []*v1.Pod, errors map[*v1.Pod]error) {
	// Report failures and nominations
	for _, pod := range failedToSchedule {
		logging.FromContext(ctx).With("pod", client.ObjectKeyFromObject(pod)).Errorf("Could not schedule pod, %s", errors[pod])
		s.recorder.Publish(schedulingevents.PodFailedToSchedule(pod, errors[pod]))
	}

	for _, existing := range s.existingNodes {
		if len(existing.Pods) > 0 {
			s.cluster.NominateNodeForPod(ctx, existing.Name())
		}
		for _, pod := range existing.Pods {
			s.recorder.Publish(schedulingevents.NominatePod(pod, existing.Node, existing.Machine))
		}
	}

	// Report new nodes, or exit to avoid log spam
	newCount := 0
	for _, machine := range s.newNodeClaims {
		newCount += len(machine.Pods)
	}
	if newCount == 0 {
		return
	}
	logging.FromContext(ctx).With("pods", len(pods)).Infof("found provisionable pod(s)")
	logging.FromContext(ctx).With("machines", len(s.newNodeClaims), "pods", newCount).Infof("computed new machine(s) to fit pod(s)")
	// Report in flight newNodes, or exit to avoid log spam
	inflightCount := 0
	existingCount := 0
	for _, node := range lo.Filter(s.existingNodes, func(node *ExistingNode, _ int) bool { return len(node.Pods) > 0 }) {
		inflightCount++
		existingCount += len(node.Pods)
	}
	if existingCount == 0 {
		return
	}
	logging.FromContext(ctx).Infof("computed %d unready node(s) will fit %d pod(s)", inflightCount, existingCount)
}

func (s *Scheduler) add(ctx context.Context, pod *v1.Pod) error {
	// first try to schedule against an in-flight real node
	for _, node := range s.existingNodes {
		if err := node.Add(ctx, s.kubeClient, pod); err == nil {
			return nil
		}
	}

	// Consider using https://pkg.go.dev/container/heap
	sort.Slice(s.newNodeClaims, func(a, b int) bool { return len(s.newNodeClaims[a].Pods) < len(s.newNodeClaims[b].Pods) })

	// Pick existing node that we are about to create
	for _, machine := range s.newNodeClaims {
		if err := machine.Add(ctx, pod); err == nil {
			return nil
		}
	}

	// Create new node
	var errs error
	for _, machineTemplate := range s.machineTemplates {
		ownerName := lo.Must(nodepoolutil.OwnerName(machineTemplate))
		instanceTypes := s.instanceTypes[ownerName]
		// if limits have been applied to the provisioner, ensure we filter instance types to avoid violating those limits
		if remaining, ok := s.remainingResources[ownerName]; ok {
			instanceTypes = filterByRemainingResources(s.instanceTypes[ownerName], remaining)
			if len(instanceTypes) == 0 {
				errs = multierr.Append(errs, fmt.Errorf("all available instance types exceed limits for provisioner: %q", ownerName))
				continue
			} else if len(s.instanceTypes[ownerName]) != len(instanceTypes) && !s.opts.SimulationMode {
				logging.FromContext(ctx).With("provisioner", lo.Must(nodepoolutil.OwnerName(machineTemplate))).Debugf("%d out of %d instance types were excluded because they would breach provisioner limits",
					len(s.instanceTypes[ownerName])-len(instanceTypes), len(s.instanceTypes[ownerName]))
			}
		}

		// TODO @joinnis: Replace instances of provisioner with the new naming convention
		machine := NewNodeClaim(machineTemplate, s.topology, s.daemonOverhead[machineTemplate], instanceTypes)
		if err := machine.Add(ctx, pod); err != nil {
			errs = multierr.Append(errs, fmt.Errorf("incompatible with provisioner %q, daemonset overhead=%s, %w",
				ownerName,
				resources.String(s.daemonOverhead[machineTemplate]),
				err))
			continue
		}
		// we will launch this machine and need to track its maximum possible resource usage against our remaining resources
		s.newNodeClaims = append(s.newNodeClaims, machine)
		s.remainingResources[ownerName] = subtractMax(s.remainingResources[ownerName], machine.InstanceTypeOptions)
		return nil
	}
	return errs
}

func (s *Scheduler) calculateExistingMachines(stateNodes []*state.StateNode, daemonSetPods []*v1.Pod) {
	// create our existing nodes
	for _, node := range stateNodes {
		if !node.Owned() {
			// ignoring this node as it wasn't launched by us
			continue
		}
		// Calculate any daemonsets that should schedule to the inflight node
		var daemons []*v1.Pod
		for _, p := range daemonSetPods {
			if err := scheduling.Taints(node.Taints()).Tolerates(p); err != nil {
				continue
			}
			if err := scheduling.NewLabelRequirements(node.Labels()).Compatible(scheduling.NewPodRequirements(p)); err != nil {
				continue
			}
			daemons = append(daemons, p)
		}
		s.existingNodes = append(s.existingNodes, NewExistingNode(node, s.topology, resources.RequestsForPods(daemons...)))

		// We don't use the status field and instead recompute the remaining resources to ensure we have a consistent view
		// of the cluster during scheduling.  Depending on how node creation falls out, this will also work for cases where
		// we don't create NodeClaim resources.
		if _, ok := s.remainingResources[node.Labels()[v1alpha5.ProvisionerNameLabelKey]]; ok {
			s.remainingResources[node.Labels()[v1alpha5.ProvisionerNameLabelKey]] = resources.Subtract(s.remainingResources[node.Labels()[v1alpha5.ProvisionerNameLabelKey]], node.Capacity())
		}
	}
	// Order the existing nodes for scheduling with initialized nodes first
	// This is done specifically for consolidation where we want to make sure we schedule to initialized nodes
	// before we attempt to schedule un-initialized ones
	sort.SliceStable(s.existingNodes, func(i, j int) bool {
		if s.existingNodes[i].Initialized() && !s.existingNodes[j].Initialized() {
			return true
		}
		if !s.existingNodes[i].Initialized() && s.existingNodes[j].Initialized() {
			return false
		}
		return s.existingNodes[i].Name() < s.existingNodes[j].Name()
	})
}

func getDaemonOverhead(machineTemplates []*NodeClaimTemplate, daemonSetPods []*v1.Pod) map[*NodeClaimTemplate]v1.ResourceList {
	overhead := map[*NodeClaimTemplate]v1.ResourceList{}

	for _, machineTemplate := range machineTemplates {
		var daemons []*v1.Pod
		for _, p := range daemonSetPods {
			if err := scheduling.Taints(machineTemplate.Spec.Taints).Tolerates(p); err != nil {
				continue
			}
			if err := machineTemplate.Requirements.Compatible(scheduling.NewPodRequirements(p)); err != nil {
				continue
			}
			daemons = append(daemons, p)
		}
		overhead[machineTemplate] = resources.RequestsForPods(daemons...)
	}
	return overhead
}

// subtractMax returns the remaining resources after subtracting the max resource quantity per instance type. To avoid
// overshooting out, we need to pessimistically assume that if e.g. we request a 2, 4 or 8 CPU instance type
// that the 8 CPU instance type is all that will be available.  This could cause a batch of pods to take multiple rounds
// to schedule.
func subtractMax(remaining v1.ResourceList, instanceTypes []*cloudprovider.InstanceType) v1.ResourceList {
	// shouldn't occur, but to be safe
	if len(instanceTypes) == 0 {
		return remaining
	}
	var allInstanceResources []v1.ResourceList
	for _, it := range instanceTypes {
		allInstanceResources = append(allInstanceResources, it.Capacity)
	}
	result := v1.ResourceList{}
	itResources := resources.MaxResources(allInstanceResources...)
	for k, v := range remaining {
		cp := v.DeepCopy()
		cp.Sub(itResources[k])
		result[k] = cp
	}
	return result
}

// filterByRemainingResources is used to filter out instance types that if launched would exceed the provisioner limits
func filterByRemainingResources(instanceTypes []*cloudprovider.InstanceType, remaining v1.ResourceList) []*cloudprovider.InstanceType {
	var filtered []*cloudprovider.InstanceType
	for _, it := range instanceTypes {
		itResources := it.Capacity
		viableInstance := true
		for resourceName, remainingQuantity := range remaining {
			// if the instance capacity is greater than the remaining quantity for this resource
			if resources.Cmp(itResources[resourceName], remainingQuantity) > 0 {
				viableInstance = false
			}
		}
		if viableInstance {
			filtered = append(filtered, it)
		}
	}
	return filtered
}
