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

package hydration

import (
	"context"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/logging"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha1"
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	corecontroller "github.com/aws/karpenter-core/pkg/operator/controller"
	"github.com/aws/karpenter-core/pkg/operator/scheme"
)

var _ corecontroller.TypedController[*v1.Node] = (*Controller)(nil)

// Controller manages a set of properties on karpenter provisioned nodes, such as
// taints, labels, finalizers.
type Controller struct {
	kubeClient client.Client
}

// NewController constructs a nodeController instance
func NewController(kubeClient client.Client) corecontroller.Controller {
	return corecontroller.Typed[*v1.Node](kubeClient, &Controller{
		kubeClient: kubeClient,
	})
}

func (c *Controller) Name() string {
	return "machine-hydration"
}

// Reconcile executes a machine hydration loop for existing nodes
func (c *Controller) Reconcile(ctx context.Context, node *v1.Node) (reconcile.Result, error) {
	// If the node has already propagated a machine label, we know that it already has a Machine corresponding to it
	// If there is a provisioner name label without the machine label, we know that this node needs to be hydrated. This
	// is because we rely on the fact that these labels will be added at the same time in the node label reconciliation flow.
	if _, ok := node.Labels[v1alpha5.MachineNameLabelKey]; ok {
		return reconcile.Result{}, nil
	}
	provisioner := &v1alpha5.Provisioner{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: node.Labels[v1alpha5.ProvisionerNameLabelKey]}, provisioner); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// We list any machines to see if a Machine already exists for this node
	machineList := &v1alpha1.MachineList{}
	if err := c.kubeClient.List(ctx, machineList, client.MatchingLabels{v1alpha5.NodeNameLabelKey: node.Name}); err != nil {
		return reconcile.Result{}, err
	}

	// TODO joinnis: Check what happens when we get more than one item in this machine list
	// I think if we get more than one item, we should just fail outright
	var machine *v1alpha1.Machine
	if len(machineList.Items) == 0 {
		machine = v1alpha1.MachineFromNode(node)
		machine.Spec.Kubelet = provisioner.Spec.KubeletConfiguration
		lo.Must0(controllerutil.SetOwnerReference(provisioner, machine, scheme.Scheme))
		if provisioner.Spec.ProviderRef != nil {
			machine.Spec.MachineTemplateRef = provisioner.Spec.ProviderRef.ToObjectReference()
		} else if provisioner.Spec.Provider != nil {
			machine.Annotations[v1alpha5.ProviderCompatabilityAnnotationKey] = v1alpha5.ProviderAnnotation(provisioner.Spec.Provider)
		} else {
			logging.FromContext(ctx).Errorf("node contains no valid provider or providerRef data")
			return reconcile.Result{}, nil
		}
		if err := c.kubeClient.Create(ctx, machine); err != nil {
			if !errors.IsAlreadyExists(err) {
				return reconcile.Result{}, err
			}
		}
	} else if len(machineList.Items) == 1 {
		// We use the current version of the machine that we listed
		machine = &machineList.Items[0]
	} else {
		logging.FromContext(ctx).Errorf("more than one machine (%d machines) maps to this node", len(machineList.Items))
		return reconcile.Result{}, nil
	}
	if err := c.kubeClient.Status().Patch(ctx, v1alpha1.MachineFromNode(node), client.MergeFrom(machine)); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	return reconcile.Result{}, nil
}

func (c *Controller) Builder(_ context.Context, m manager.Manager) corecontroller.Builder {
	return corecontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1.Node{}))
}
