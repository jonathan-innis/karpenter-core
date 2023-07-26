package machine

import (
	"context"
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"knative.dev/pkg/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/scheduling"
)

// PodEventHandler is a watcher on v1.Pods that maps Pods to NodeClaim based on the node names
// and enqueues reconcile.Requests for the Machines
func PodEventHandler(ctx context.Context, c client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(o client.Object) (requests []reconcile.Request) {
		if name := o.(*v1.Pod).Spec.NodeName; name != "" {
			node := &v1.Node{}
			if err := c.Get(ctx, types.NamespacedName{Name: name}, node); err != nil {
				return []reconcile.Request{}
			}
			machineList := &v1alpha5.MachineList{}
			if err := c.List(ctx, machineList, client.MatchingFields{"status.providerID": node.Spec.ProviderID}); err != nil {
				return []reconcile.Request{}
			}
			return lo.Map(machineList.Items, func(m v1alpha5.Machine, _ int) reconcile.Request {
				return reconcile.Request{
					NamespacedName: client.ObjectKeyFromObject(&m),
				}
			})
		}
		return requests
	})
}

// NodeEventHandler is a watcher on v1.Node that maps Nodes to Machines based on provider ids
// and enqueues reconcile.Requests for the Machines
func NodeEventHandler(ctx context.Context, c client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
		node := o.(*v1.Node)
		machineList := &v1alpha5.MachineList{}
		if err := c.List(ctx, machineList, client.MatchingFields{"status.providerID": node.Spec.ProviderID}); err != nil {
			return []reconcile.Request{}
		}
		return lo.Map(machineList.Items, func(m v1alpha5.Machine, _ int) reconcile.Request {
			return reconcile.Request{
				NamespacedName: client.ObjectKeyFromObject(&m),
			}
		})
	})
}

// ProvisionerEventHandler is a watcher on v1alpha5.Machine that maps Provisioner to Machines based
// on the v1alpha5.ProvsionerNameLabelKey and enqueues reconcile.Requests for the NodeClaim
func ProvisionerEventHandler(ctx context.Context, c client.Client) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(o client.Object) (requests []reconcile.Request) {
		machineList := &v1alpha5.MachineList{}
		if err := c.List(ctx, machineList, client.MatchingLabels(map[string]string{v1alpha5.ProvisionerNameLabelKey: o.GetName()})); err != nil {
			return requests
		}
		return lo.Map(machineList.Items, func(machine v1alpha5.Machine, _ int) reconcile.Request {
			return reconcile.Request{NamespacedName: types.NamespacedName{Name: machine.Name}}
		})
	})
}

// New converts a node into a Machine using known values from the node and provisioner spec values
// Deprecated: This Machine generator function can be removed when v1beta1 migration has completed.
func New(node *v1.Node, provisioner *v1alpha5.Provisioner) *v1alpha5.Machine {
	machine := NewFromNode(node)
	machine.Annotations = lo.Assign(provisioner.Annotations, v1alpha5.ProviderAnnotation(provisioner.Spec.Provider))
	machine.Labels = lo.Assign(provisioner.Labels, map[string]string{v1alpha5.ProvisionerNameLabelKey: provisioner.Name})
	machine.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion:         v1alpha5.SchemeGroupVersion.String(),
			Kind:               "Provisioner",
			Name:               provisioner.Name,
			UID:                provisioner.UID,
			BlockOwnerDeletion: ptr.Bool(true),
		},
	}
	machine.Spec.Kubelet = provisioner.Spec.KubeletConfiguration
	machine.Spec.Taints = provisioner.Spec.Taints
	machine.Spec.StartupTaints = provisioner.Spec.StartupTaints
	machine.Spec.Requirements = provisioner.Spec.Requirements
	machine.Spec.MachineTemplateRef = provisioner.Spec.ProviderRef
	return machine
}

// NewFromNode converts a node into a pseudo-Machine using known values from the node
// Deprecated: This Machine generator function can be removed when v1beta1 migration has completed.
func NewFromNode(node *v1.Node) *v1alpha5.Machine {
	m := &v1alpha5.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:        node.Name,
			Annotations: node.Annotations,
			Labels:      node.Labels,
			Finalizers:  []string{v1alpha5.TerminationFinalizer},
		},
		Spec: v1alpha5.MachineSpec{
			Taints:       node.Spec.Taints,
			Requirements: scheduling.NewLabelRequirements(node.Labels).NodeSelectorRequirements(),
			Resources: v1alpha5.ResourceRequirements{
				Requests: node.Status.Allocatable,
			},
		},
		Status: v1alpha5.MachineStatus{
			NodeName:    node.Name,
			ProviderID:  node.Spec.ProviderID,
			Capacity:    node.Status.Capacity,
			Allocatable: node.Status.Allocatable,
		},
	}
	if _, ok := node.Labels[v1alpha5.LabelNodeInitialized]; ok {
		m.StatusConditions().MarkTrue(v1alpha5.MachineInitialized)
	}
	m.StatusConditions().MarkTrue(v1alpha5.MachineLaunched)
	m.StatusConditions().MarkTrue(v1alpha5.MachineRegistered)
	return m
}

func IsExpired(obj client.Object, clock clock.Clock, provisioner *v1alpha5.Provisioner) bool {
	return clock.Now().After(GetExpirationTime(obj, provisioner))
}

func GetExpirationTime(obj client.Object, provisioner *v1alpha5.Provisioner) time.Time {
	if provisioner == nil || provisioner.Spec.TTLSecondsUntilExpired == nil || obj == nil {
		// If not defined, return some much larger time.
		return time.Date(5000, 0, 0, 0, 0, 0, 0, time.UTC)
	}
	expirationTTL := time.Duration(ptr.Int64Value(provisioner.Spec.TTLSecondsUntilExpired)) * time.Second
	return obj.GetCreationTimestamp().Add(expirationTTL)
}

func IsPastEmptinessTTL(nodeClaim *v1alpha5.Machine, clock clock.Clock, provisioner *v1alpha5.Provisioner) bool {
	return nodeClaim.StatusConditions().GetCondition(v1alpha5.MachineEmpty) != nil &&
		nodeClaim.StatusConditions().GetCondition(v1alpha5.MachineEmpty).IsTrue() &&
		!clock.Now().Before(nodeClaim.StatusConditions().GetCondition(v1alpha5.MachineEmpty).LastTransitionTime.Inner.Add(time.Duration(lo.FromPtr(provisioner.Spec.TTLSecondsAfterEmpty))*time.Second))
}
