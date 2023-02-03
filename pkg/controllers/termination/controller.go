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

package termination

import (
	"context"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"knative.dev/pkg/logging"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	corecontroller "github.com/aws/karpenter-core/pkg/operator/controller"
)

var _ corecontroller.FinalizingTypedController[*v1.Node] = (*Controller)(nil)

// Controller for the resource
type Controller struct {
	kubeClient client.Client
}

// NewController constructs a controller instance
func NewController(kubeClient client.Client) corecontroller.Controller {
	return corecontroller.Typed[*v1.Node](kubeClient, &Controller{
		kubeClient: kubeClient,
	})
}

func (c *Controller) Name() string {
	return "termination"
}

func (c *Controller) Reconcile(_ context.Context, _ *v1.Node) (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

func (c *Controller) Finalize(ctx context.Context, node *v1.Node) (reconcile.Result, error) {
	if !controllerutil.ContainsFinalizer(node, v1alpha5.TerminationFinalizer) {
		return reconcile.Result{}, nil
	}
	machineList := &v1alpha5.MachineList{}
	if err := c.kubeClient.List(ctx, machineList, client.MatchingFields{"status.providerID": node.Spec.ProviderID}); err != nil {
		return reconcile.Result{}, err
	}
	// If there is no longer a machine for this node, remove the finalizer and delete the node
	if len(machineList.Items) == 0 {
		stored := node.DeepCopy()
		controllerutil.RemoveFinalizer(node, v1alpha5.TerminationFinalizer)
		if !equality.Semantic.DeepEqual(stored, node) {
			if err := c.kubeClient.Patch(ctx, node, client.MergeFrom(stored)); err != nil {
				return reconcile.Result{}, client.IgnoreNotFound(err)
			}
			logging.FromContext(ctx).Infof("deleted node")
		}
		return reconcile.Result{}, nil
	}
	for i := range machineList.Items {
		if err := c.kubeClient.Delete(ctx, &machineList.Items[i]); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (c *Controller) Builder(ctx context.Context, m manager.Manager) corecontroller.Builder {
	return corecontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1.Node{}).
		Watches(
			&source.Kind{Type: &v1alpha5.Machine{}},
			handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
				machine := o.(*v1alpha5.Machine)
				nodeList := &v1.NodeList{}
				if machine.Status.ProviderID == "" {
					return nil
				}
				if err := c.kubeClient.List(ctx, nodeList, client.MatchingFields{"spec.providerID": machine.Status.ProviderID}); err != nil {
					return nil
				}
				return lo.Map(nodeList.Items, func(n v1.Node, _ int) reconcile.Request {
					return reconcile.Request{
						NamespacedName: client.ObjectKeyFromObject(&n),
					}
				})
			}),
		).
		WithOptions(
			controller.Options{
				MaxConcurrentReconciles: 10,
			},
		))
}
