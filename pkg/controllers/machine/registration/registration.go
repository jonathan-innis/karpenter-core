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

package registration

import (
	"context"
	"fmt"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	corecontroller "github.com/aws/karpenter-core/pkg/operator/controller"
	"github.com/aws/karpenter-core/pkg/operator/scheme"
	"github.com/aws/karpenter-core/pkg/scheduling"
	machineutil "github.com/aws/karpenter-core/pkg/utils/machine"
)

type Controller struct {
	kubeClient client.Client
}

// NewController is a constructor for the Machine Controller
func NewController(kubeClient client.Client) corecontroller.Controller {
	return corecontroller.Typed[*v1alpha5.Machine](kubeClient, &Controller{
		kubeClient: kubeClient,
	})
}

func (r *Controller) Name() string {
	return "machine_registration"
}

func (r *Controller) Reconcile(ctx context.Context, machine *v1alpha5.Machine) (reconcile.Result, error) {
	if machine.Status.ProviderID == "" {
		machine.StatusConditions().MarkFalse(v1alpha5.MachineRegistered, "MachineNotLaunched", "Machine isn't launched yet")
		return reconcile.Result{}, nil
	}
	if machine.StatusConditions().GetCondition(v1alpha5.MachineRegistered).IsTrue() {
		return reconcile.Result{}, nil
	}

	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("provider-id", machine.Status.ProviderID))
	node, err := machineutil.NodeForMachine(ctx, r.kubeClient, machine)
	if err != nil {
		if machineutil.IsNodeNotFoundError(err) {
			machine.StatusConditions().MarkFalse(v1alpha5.MachineRegistered, "NodeNotFound", "Node not registered with cluster")
			return reconcile.Result{}, nil
		}
		if machineutil.IsDuplicateNodeError(err) {
			machine.StatusConditions().MarkFalse(v1alpha5.MachineRegistered, "MultipleNodesFound", "Invariant violated, machine matched multiple nodes")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting node for machine, %w", err)
	}
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("node", node.Name))
	if err = r.syncNode(ctx, machine, node); err != nil {
		return reconcile.Result{}, fmt.Errorf("syncing node, %w", err)
	}
	machine.StatusConditions().MarkTrue(v1alpha5.MachineRegistered)
	return reconcile.Result{}, nil
}

func (r *Controller) syncNode(ctx context.Context, machine *v1alpha5.Machine, node *v1.Node) error {
	stored := node.DeepCopy()
	controllerutil.AddFinalizer(node, v1alpha5.TerminationFinalizer)
	lo.Must0(controllerutil.SetOwnerReference(machine, node, scheme.Scheme))
	// Remove any provisioner owner references since we own them
	node.OwnerReferences = lo.Reject(node.OwnerReferences, func(o metav1.OwnerReference, _ int) bool {
		return o.Kind == "Provisioner"
	})

	// If the machine isn't registered as linked, then sync it
	// This prevents us from messing with nodes that already exist and are scheduled
	if _, ok := machine.Annotations[v1alpha5.MachineLinkedAnnotationKey]; !ok {
		node.Labels = lo.Assign(node.Labels, machine.Labels)
		node.Annotations = lo.Assign(node.Annotations, machine.Annotations)
		// Sync all taints inside of Machine into the Machine taints
		node.Spec.Taints = scheduling.Taints(node.Spec.Taints).Merge(machine.Spec.Taints)
	}
	node.Labels[v1alpha5.MachineNameLabelKey] = machine.Labels[v1alpha5.MachineNameLabelKey]
	if !machine.StatusConditions().GetCondition(v1alpha5.MachineRegistered).IsTrue() {
		node.Spec.Taints = scheduling.Taints(node.Spec.Taints).Merge(machine.Spec.StartupTaints)
	}
	if !equality.Semantic.DeepEqual(stored, node) {
		if err := r.kubeClient.Patch(ctx, node, client.MergeFrom(stored)); err != nil {
			return fmt.Errorf("syncing node labels, %w", err)
		}
		logging.FromContext(ctx).Debugf("synced node")
	}
	return nil
}
