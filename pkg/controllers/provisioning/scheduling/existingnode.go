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

	v1 "k8s.io/api/core/v1"

	"github.com/aws/karpenter-core/pkg/controllers/state"
	"github.com/aws/karpenter-core/pkg/scheduling"
	"github.com/aws/karpenter-core/pkg/utils/resources"
)

type ExistingNode struct {
	*state.Node

	Pods         []*v1.Pod
	topology     *Topology
	requests     v1.ResourceList
	requirements scheduling.Requirements
}

func NewExistingNode(n *state.Node, topology *Topology, daemonResources v1.ResourceList) *ExistingNode {
	// The state node passed in here must be a deep copy from cluster state as we modify it
	// the remaining daemonResources to schedule are the total daemonResources minus what has already scheduled
	remainingDaemonResources := resources.Subtract(daemonResources, n.DaemonSetRequests())
	// If unexpected daemonset pods schedule to the node due to labels appearing on the node which cause the
	// DS to be able to schedule, we need to ensure that we don't let our remainingDaemonResources go negative as
	// it will cause us to mis-calculate the amount of remaining resources
	for k, v := range remainingDaemonResources {
		if v.AsApproximateFloat64() < 0 {
			v.Set(0)
			remainingDaemonResources[k] = v
		}
	}
	node := &ExistingNode{
		Node: n,

		topology:     topology,
		requests:     remainingDaemonResources,
		requirements: scheduling.NewLabelRequirements(n.Labels()),
	}
	node.requirements.Add(scheduling.NewRequirement(v1.LabelHostname, v1.NodeSelectorOpIn, n.HostName()))
	topology.Register(v1.LabelHostname, n.HostName())
	return node
}

func (n *ExistingNode) Add(ctx context.Context, pod *v1.Pod) error {
	// Check Taints
	if err := scheduling.Taints(n.Taints()).Tolerates(pod); err != nil {
		return err
	}

	if err := n.HostPortUsage().Validate(pod); err != nil {
		return err
	}

	// determine the number of volumes that will be mounted if the pod schedules
	mountedVolumeCount, err := n.VolumeUsage().Validate(ctx, pod)
	if err != nil {
		return err
	}
	if mountedVolumeCount.Exceeds(n.VolumeLimits()) {
		return fmt.Errorf("would exceed node volume limits")
	}

	// check resource requests first since that's a pretty likely reason the pod won't schedule on an in-flight
	// node, which at this point can't be increased in size
	requests := resources.Merge(n.requests, resources.RequestsForPods(pod))

	if !resources.Fits(requests, n.Available()) {
		return fmt.Errorf("exceeds node resources")
	}

	nodeRequirements := scheduling.NewRequirements(n.requirements.Values()...)
	podRequirements := scheduling.NewPodRequirements(pod, false)

	// Check Node Affinity Requirements
	reqs, err := nodeRequirements.FlexibleCompatible(podRequirements)
	if err != nil {
		return err
	}
	nodeRequirements.Add(reqs.Values()...)

	// Check Topology Requirements
	topologyRequirements, err := n.topology.AddRequirements(reqs, nodeRequirements, pod)
	if err != nil {
		return err
	}
	if err = nodeRequirements.Compatible(topologyRequirements); err != nil {
		return err
	}
	nodeRequirements.Add(topologyRequirements.Values()...)

	// Update node
	n.Pods = append(n.Pods, pod)
	n.requests = requests
	n.requirements = nodeRequirements
	n.topology.Record(pod, nodeRequirements)
	n.HostPortUsage().Add(ctx, pod)
	n.VolumeUsage().Add(ctx, pod)
	return nil
}
