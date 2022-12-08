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

package state

import (
	"context"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/logging"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha1"
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	corecontroller "github.com/aws/karpenter-core/pkg/operator/controller"
)

// MachineNodeController reconciles machines for the purpose of maintaining state regarding nodes that is expensive to compute.
type MachineNodeController struct {
	kubeClient client.Client
	cluster    *Cluster
}

// NewMachineNodeController constructs a controller instance
func NewMachineNodeController(kubeClient client.Client, cluster *Cluster) corecontroller.Controller {
	return &MachineNodeController{
		kubeClient: kubeClient,
		cluster:    cluster,
	}
}

func (c *MachineNodeController) Name() string {
	return "machine-state"
}

func (c *MachineNodeController) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).Named(c.Name()).With("machine", req.NamespacedName.Name))
	machine := &v1alpha1.Machine{}
	if err := c.kubeClient.Get(ctx, req.NamespacedName, machine); err != nil {
		if errors.IsNotFound(err) {
			c.cluster.DeleteMachineNode(req.Name)
		}
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	var node *v1.Node
	nodeList := &v1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList, client.MatchingLabels{v1alpha5.MachineNameLabelKey: machine.Name}); client.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, err
	}
	if len(nodeList.Items) == 1 {
		node = &nodeList.Items[0]
	}
	if err := c.cluster.UpdateMachineNode(ctx, machine, node); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{Requeue: true, RequeueAfter: stateRetryPeriod}, nil
}

func (c *MachineNodeController) Builder(_ context.Context, m manager.Manager) corecontroller.Builder {
	return corecontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1alpha1.Machine{}).
		Watches(
			&source.Kind{Type: &v1.Node{}},
			handler.EnqueueRequestsFromMapFunc(func(o client.Object) []reconcile.Request {
				if name, ok := o.GetLabels()[v1alpha5.MachineNameLabelKey]; ok {
					return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
				}
				return nil
			}),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}))
}