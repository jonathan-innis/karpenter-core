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
	"context"
	"fmt"
	"sort"

	"go.uber.org/multierr"
	v1 "k8s.io/api/core/v1"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/controllers/state"
	"github.com/aws/karpenter-core/pkg/scheduling"
	"github.com/aws/karpenter-core/pkg/utils/resources"
)

func NewScheduler(ctx context.Context, kubeClient client.Client, machines []*MachineTemplate,
	provisioners []v1alpha5.Provisioner, cluster *state.Cluster, stateNodes []*state.StateNode, topology *Topology,
	instanceTypes map[string][]*cloudprovider.InstanceType, daemonSetPods []*v1.Pod) *Scheduler {

	// if any of the provisioners add a taint with a prefer no schedule effect, we add a toleration for the taint
	// during preference relaxation
	toleratePreferNoSchedule := false
	for _, prov := range provisioners {
		for _, taint := range prov.Spec.Taints {
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
		preferences:        &Preferences{ToleratePreferNoSchedule: toleratePreferNoSchedule},
		remainingResources: map[string]v1.ResourceList{},
	}
	for _, provisioner := range provisioners {
		if provisioner.Spec.Limits != nil {
			s.remainingResources[provisioner.Name] = provisioner.Spec.Limits.Resources
		}
	}
	s.calculateExistingMachines(stateNodes, daemonSetPods)
	return s
}

type Scheduler struct {
	ctx                context.Context
	newMachines        []*Machine
	existingNodes      []*ExistingNode
	machineTemplates   []*MachineTemplate
	remainingResources map[string]v1.ResourceList // provisioner name -> remaining resources for that provisioner
	instanceTypes      map[string][]*cloudprovider.InstanceType
	daemonOverhead     map[*MachineTemplate]v1.ResourceList
	preferences        *Preferences
	topology           *Topology
	cluster            *state.Cluster
	kubeClient         client.Client
}

type Solution struct {
	NewMachines      []*Machine
	ExistingMachines []*ExistingNode
	Unschedulable    map[*v1.Pod]error
}

func (s *Scheduler) Solve(ctx context.Context, pods []*v1.Pod) *Solution {
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

	for _, m := range s.newMachines {
		m.FinalizeScheduling()
	}

	return &Solution{
		NewMachines:      s.newMachines,
		ExistingMachines: s.existingNodes,
		Unschedulable:    errors,
	}
}

func (s *Scheduler) add(ctx context.Context, pod *v1.Pod) error {
	// first try to schedule against an in-flight real node
	for _, node := range s.existingNodes {
		if err := node.Add(ctx, s.kubeClient, pod); err == nil {
			return nil
		}
	}

	// Consider using https://pkg.go.dev/container/heap
	sort.Slice(s.newMachines, func(a, b int) bool { return len(s.newMachines[a].Pods) < len(s.newMachines[b].Pods) })

	// Pick existing node that we are about to create
	for _, machine := range s.newMachines {
		if err := machine.Add(ctx, pod); err == nil {
			return nil
		}
	}

	// Create new node
	var errs error
	for _, machineTemplate := range s.machineTemplates {
		instanceTypes := s.instanceTypes[machineTemplate.ProvisionerName]
		// if limits have been applied to the provisioner, ensure we filter instance types to avoid violating those limits
		if remaining, ok := s.remainingResources[machineTemplate.ProvisionerName]; ok {
			instanceTypes = filterByRemainingResources(s.instanceTypes[machineTemplate.ProvisionerName], remaining)
			if len(instanceTypes) == 0 {
				errs = multierr.Append(errs, fmt.Errorf("all available instance types exceed provisioner limits"))
				continue
			}
			// else if len(s.instanceTypes[machineTemplate.ProvisionerName]) != len(instanceTypes) && !s.opts.SimulationMode {
			// 	logging.FromContext(ctx).Debugf("%d out of %d instance types were excluded because they would breach provisioner limits",
			// 		len(s.instanceTypes[machineTemplate.ProvisionerName])-len(instanceTypes), len(s.instanceTypes[machineTemplate.ProvisionerName]))
			// }
		}

		machine := NewMachine(machineTemplate, s.topology, s.daemonOverhead[machineTemplate], instanceTypes)
		if err := machine.Add(ctx, pod); err != nil {
			errs = multierr.Append(errs, fmt.Errorf("incompatible with provisioner %q, %w", machineTemplate.ProvisionerName, err))
			continue
		}
		// we will launch this machine and need to track its maximum possible resource usage against our remaining resources
		s.newMachines = append(s.newMachines, machine)
		s.remainingResources[machineTemplate.ProvisionerName] = subtractMax(s.remainingResources[machineTemplate.ProvisionerName], machine.InstanceTypeOptions)
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
		// we don't create Machine resources.
		if _, ok := s.remainingResources[node.Labels()[v1alpha5.ProvisionerNameLabelKey]]; ok {
			s.remainingResources[node.Labels()[v1alpha5.ProvisionerNameLabelKey]] = resources.Subtract(s.remainingResources[node.Labels()[v1alpha5.ProvisionerNameLabelKey]], node.Capacity())
		}
	}
}

func getDaemonOverhead(nodeTemplates []*MachineTemplate, daemonSetPods []*v1.Pod) map[*MachineTemplate]v1.ResourceList {
	overhead := map[*MachineTemplate]v1.ResourceList{}

	for _, nodeTemplate := range nodeTemplates {
		var daemons []*v1.Pod
		for _, p := range daemonSetPods {
			if err := nodeTemplate.Taints.Tolerates(p); err != nil {
				continue
			}
			if err := nodeTemplate.Requirements.Compatible(scheduling.NewPodRequirements(p)); err != nil {
				continue
			}
			daemons = append(daemons, p)
		}
		overhead[nodeTemplate] = resources.RequestsForPods(daemons...)
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
