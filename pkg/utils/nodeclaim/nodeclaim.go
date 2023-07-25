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

package nodeclaim

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
)

// PodEventHandler is a watcher on v1.Pods that maps Pods to NodeClaim based on the node names
// and enqueues reconcile.Requests for the NodeClaims
func PodEventHandler(ctx context.Context, c client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(o client.Object) (requests []reconcile.Request) {
		if name := o.(*v1.Pod).Spec.NodeName; name != "" {
			node := &v1.Node{}
			if err := c.Get(ctx, types.NamespacedName{Name: name}, node); err != nil {
				return []reconcile.Request{}
			}
			nodeClaimList := &v1beta1.NodeClaimList{}
			if err := c.List(ctx, nodeClaimList, client.MatchingFields{"status.providerID": node.Spec.ProviderID}); err != nil {
				return []reconcile.Request{}
			}
			return lo.Map(nodeClaimList.Items, func(n v1beta1.NodeClaim, _ int) reconcile.Request {
				return reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&n),
				}
			})
		}
		return requests
	})
}

// NodeEventHandler is a watcher on v1.Node that maps Nodes to NodeClaims based on provider ids
// and enqueues reconcile.Requests for the NodeClaims
func NodeEventHandler(ctx context.Context, c client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
		node := o.(*v1.Node)
		nodeClaimList := &v1beta1.NodeClaimList{}
		if err := c.List(ctx, nodeClaimList, client.MatchingFields{"status.providerID": node.Spec.ProviderID}); err != nil {
			return []reconcile.Request{}
		}
		return lo.Map(nodeClaimList.Items, func(n v1beta1.NodeClaim, _ int) reconcile.Request {
			return reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&n),
			}
		})
	})
}

// NodePoolEventHandler is a watcher on v1beta1.NodeClaim that maps Provisioner to NodeClaims based
// on the v1beta1.NodePoolLabelKey and enqueues reconcile.Requests for the NodeClaim
func NodePoolEventHandler(ctx context.Context, c client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(o client.Object) (requests []reconcile.Request) {
		nodeClaimList := &v1beta1.NodeClaimList{}
		if err := c.List(ctx, nodeClaimList, client.MatchingLabels(map[string]string{v1beta1.NodePoolLabelKey: o.GetName()})); err != nil {
			return requests
		}
		return lo.Map(nodeClaimList.Items, func(n v1beta1.NodeClaim, _ int) reconcile.Request {
			return reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&n),
			}
		})
	})
}

// NodeNotFoundError is an error returned when no v1.Nodes are found matching the passed providerID
type NodeNotFoundError struct {
	ProviderID string
}

func (e *NodeNotFoundError) Error() string {
	return fmt.Sprintf("no nodes found for provider id '%s'", e.ProviderID)
}

func IsNodeNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	nnfErr := &NodeNotFoundError{}
	return errors.As(err, &nnfErr)
}

func IgnoreNodeNotFoundError(err error) error {
	if !IsNodeNotFoundError(err) {
		return err
	}
	return nil
}

// DuplicateNodeError is an error returned when multiple v1.Nodes are found matching the passed providerID
type DuplicateNodeError struct {
	ProviderID string
}

func (e *DuplicateNodeError) Error() string {
	return fmt.Sprintf("multiple found for provider id '%s'", e.ProviderID)
}

func IsDuplicateNodeError(err error) bool {
	if err == nil {
		return false
	}
	dnErr := &DuplicateNodeError{}
	return errors.As(err, &dnErr)
}

func IgnoreDuplicateNodeError(err error) error {
	if !IsDuplicateNodeError(err) {
		return err
	}
	return nil
}

// NodeForNodeClaim is a helper function that takes a v1beta1.NodeClaim and attempts to find the matching v1.Node by its providerID
// This function will return errors if:
//  1. No v1.Nodes match the v1beta1.NodeClaim providerID
//  2. Multiple v1.Nodes match the v1beta1.NodeClaim providerID
func NodeForNodeClaim(ctx context.Context, c client.Client, nodeClaim *v1beta1.NodeClaim) (*v1.Node, error) {
	nodes, err := AllNodesForNodeClaim(ctx, c, nodeClaim)
	if err != nil {
		return nil, err
	}
	if len(nodes) > 1 {
		return nil, &DuplicateNodeError{ProviderID: nodeClaim.Status.ProviderID}
	}
	if len(nodes) == 0 {
		return nil, &NodeNotFoundError{ProviderID: nodeClaim.Status.ProviderID}
	}
	return nodes[0], nil
}

// AllNodesForNodeClaim is a helper function that takes a v1beta1.NodeClaim and finds ALL matching v1.Nodes by their providerID
// If the providerID is not resolved for a NodeClaim, then no Nodes will map to it
func AllNodesForNodeClaim(ctx context.Context, c client.Client, nodeClaim *v1beta1.NodeClaim) ([]*v1.Node, error) {
	// NodeClaims that have no resolved providerID have no nodes mapped to them
	if nodeClaim.Status.ProviderID == "" {
		return nil, nil
	}
	nodeList := v1.NodeList{}
	if err := c.List(ctx, &nodeList, client.MatchingFields{"spec.providerID": nodeClaim.Status.ProviderID}); err != nil {
		return nil, fmt.Errorf("listing nodes, %w", err)
	}
	return lo.ToSlicePtr(nodeList.Items), nil
}

func New(machine *v1alpha5.Machine) *v1beta1.NodeClaim {
	nc := &v1beta1.NodeClaim{
		ObjectMeta: machine.ObjectMeta,
		Spec: v1beta1.NodeClaimSpec{
			Taints:        machine.Spec.Taints,
			StartupTaints: machine.Spec.StartupTaints,
			Requirements:  machine.Spec.Requirements,
			Resources: v1beta1.ResourceRequirements{
				Requests: machine.Spec.Resources.Requests,
			},
			KubeletConfiguration: NewKubeletConfiguration(machine.Spec.Kubelet),
		},
	}
	if machine.Spec.MachineTemplateRef != nil {
		nc.Spec.NodeClass = &v1beta1.NodeClassRef{
			Kind:           machine.Spec.MachineTemplateRef.Kind,
			Name:           machine.Spec.MachineTemplateRef.Name,
			APIVersion:     machine.Spec.MachineTemplateRef.APIVersion,
			IsNodeTemplate: true,
		}
	}
	return nc
}

func NewKubeletConfiguration(kc *v1alpha5.KubeletConfiguration) *v1beta1.KubeletConfiguration {
	if kc == nil {
		return nil
	}
	return &v1beta1.KubeletConfiguration{
		ClusterDNS:                  kc.ClusterDNS,
		ContainerRuntime:            kc.ContainerRuntime,
		MaxPods:                     kc.MaxPods,
		PodsPerCore:                 kc.PodsPerCore,
		SystemReserved:              kc.SystemReserved,
		KubeReserved:                kc.KubeReserved,
		EvictionHard:                kc.EvictionHard,
		EvictionSoft:                kc.EvictionSoft,
		EvictionSoftGracePeriod:     kc.EvictionSoftGracePeriod,
		EvictionMaxPodGracePeriod:   kc.EvictionMaxPodGracePeriod,
		ImageGCHighThresholdPercent: kc.ImageGCHighThresholdPercent,
		ImageGCLowThresholdPercent:  kc.ImageGCLowThresholdPercent,
		CPUCFSQuota:                 kc.CPUCFSQuota,
	}
}

func List(ctx context.Context, c client.Client, opts ...client.ListOption) (*v1beta1.NodeClaimList, error) {
	machineList := &v1alpha5.MachineList{}
	if err := c.List(ctx, machineList, opts...); err != nil {
		return nil, err
	}
	convertedNodeClaims := lo.Map(machineList.Items, func(m v1alpha5.Machine, _ int) v1beta1.NodeClaim {
		return *New(&m)
	})
	nodeClaimList := &v1beta1.NodeClaimList{}
	if err := c.List(ctx, nodeClaimList, opts...); err != nil {
		return nil, err
	}
	nodeClaimList.Items = append(nodeClaimList.Items, convertedNodeClaims...)
	return nodeClaimList, nil
}

func IsExpired(obj client.Object, clock clock.Clock, nodePool *v1beta1.NodePool) bool {
	return clock.Now().After(GetExpirationTime(obj, nodePool))
}

func GetExpirationTime(obj client.Object, nodePool *v1beta1.NodePool) time.Time {
	if nodePool == nil || nodePool.Spec.Deprovisioning.ExpirationTTL == nil || obj == nil {
		// If not defined, return some much larger time.
		return time.Date(5000, 0, 0, 0, 0, 0, 0, time.UTC)
	}
	return obj.GetCreationTimestamp().Add(nodePool.Spec.Deprovisioning.ExpirationTTL.Duration)
}
