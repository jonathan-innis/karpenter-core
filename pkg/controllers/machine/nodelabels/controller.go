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
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/logging"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha1"
	corecontroller "github.com/aws/karpenter-core/pkg/operator/controller"
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
	return "machine-node-labels"
}

// Reconcile executes reconciling the node labels and annotations to match the machine labels and annotations
func (c *Controller) Reconcile(ctx context.Context, node *v1.Node) (reconcile.Result, error) {
	// TODO joinnis: Re-enable this feature flag before merging this into the repo
	//if !settings.FromContext(ctx).MachineEnabled {
	//	return reconcile.Result{RequeueAfter: time.Minute}, nil
	//}
	if node.Spec.ProviderID == "" {
		return reconcile.Result{}, nil
	}
	machineList := &v1alpha1.MachineList{}
	if err := c.kubeClient.List(ctx, machineList, client.MatchingFields{"status.providerID": node.Spec.ProviderID}); err != nil {
		return reconcile.Result{}, err
	}
	if len(machineList.Items) == 0 {
		return reconcile.Result{}, nil
	}
	if len(machineList.Items) > 1 {
		logging.FromContext(ctx).Errorf("node has multiple machines which map to it")
		return reconcile.Result{}, nil
	}
	machine := machineList.Items[0]
	node.Labels = lo.Assign(node.Labels, machine.Labels)
	node.Annotations = lo.Assign(node.Annotations, machine.Annotations)
	return reconcile.Result{}, nil
}

func (c *Controller) Builder(ctx context.Context, m manager.Manager) corecontroller.Builder {
	return corecontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1.Node{}).
		Watches(
			&source.Kind{Type: &v1alpha1.Machine{}},
			handler.EnqueueRequestsFromMapFunc(func(o client.Object) (requests []reconcile.Request) {
				if id := o.(*v1alpha1.Machine).Status.ProviderID; id != "" {
					nodeList := &v1.NodeList{}
					if err := c.kubeClient.List(ctx, nodeList, client.MatchingFields{"spec.providerID": id}); err != nil {
						for _, node := range nodeList.Items {
							requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: node.Name}})
						}
					}
				}
				return requests
			}),
		),
	)
}
