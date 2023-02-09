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

package adoption

import (
	"context"
	"fmt"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"knative.dev/pkg/logging"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrl "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/operator/scheme"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/operator/controller"
	machineutil "github.com/aws/karpenter-core/pkg/utils/machine"
)

type Controller struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
	cache         *cache.Cache // this cache is used because the watcher cache is eventually consistent
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider) controller.Controller {
	return controller.Typed[*v1.Node](kubeClient, &Controller{
		kubeClient:    kubeClient,
		cloudProvider: cloudProvider,
		cache:         cache.New(time.Minute*5, time.Second*10),
	})
}

func (c *Controller) Name() string {
	return "machineadoption"
}

func (c *Controller) Reconcile(ctx context.Context, node *v1.Node) (reconcile.Result, error) {
	if node.Spec.ProviderID == "" {
		return reconcile.Result{}, nil
	}
	if node.Labels[v1alpha5.MachineNameLabelKey] != "" {
		return reconcile.Result{}, nil
	}
	machineList := &v1alpha5.MachineList{}
	if err := c.kubeClient.List(ctx, machineList, client.Limit(1), client.MatchingFields{"status.providerID": node.Spec.ProviderID}); err != nil {
		return reconcile.Result{}, err
	}
	// We have a machine registered for this node so no need to adopt it
	if len(machineList.Items) > 0 {
		return reconcile.Result{}, nil
	}
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("provider-id", node.Spec.ProviderID))

	retrieved, err := c.cloudProvider.Get(ctx, node.Spec.ProviderID)
	if err != nil {
		if cloudprovider.IsMachineNotOwnedError(err) {
			return reconcile.Result{}, nil
		}
		if cloudprovider.IsMachineNotFoundError(err) {
			if err = c.kubeClient.Delete(ctx, node); err != nil {
				return reconcile.Result{}, client.IgnoreNotFound(err)
			}
		}
		return reconcile.Result{}, fmt.Errorf("resolving cloudprovider instance type, %w", err)
	}
	provisionerName, ok := retrieved.Labels[v1alpha5.ProvisionerNameLabelKey]
	if !ok {
		return reconcile.Result{}, nil
	}
	provisioner := &v1alpha5.Provisioner{}
	if err = c.kubeClient.Get(ctx, types.NamespacedName{Name: provisionerName}, provisioner); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if err = c.adopt(ctx, node, provisioner, retrieved.Labels[v1alpha5.OwnedLabelKey] != ""); err != nil {
		return reconcile.Result{}, fmt.Errorf("hydrating machine from node, %w", err)
	}
	return reconcile.Result{}, nil
}

func (c *Controller) adopt(ctx context.Context, node *v1.Node, provisioner *v1alpha5.Provisioner, overProvisioned bool) error {
	machineList := &v1alpha5.MachineList{}
	if err := c.kubeClient.List(ctx, machineList, client.MatchingLabels{
		v1alpha5.AdoptingLabelKey: node.Name,
	}, client.Limit(1)); err != nil {
		return err
	}

	var machine *v1alpha5.Machine
	if len(machineList.Items) > 0 {
		machine = &machineList.Items[0]
	} else if ret, ok := c.cache.Get(client.ObjectKeyFromObject(node).String()); ok {
		machine = ret.(*v1alpha5.Machine)
	} else {
		machine = machineutil.New(node, provisioner)
		machine.Name = ""
		machine.GenerateName = fmt.Sprintf("%s-", provisioner.Name)
		machine.Labels = lo.Assign(machine.Labels, map[string]string{
			v1alpha5.AdoptingLabelKey: node.Name, // Keep track of which node this is linked to
		})
		if err := c.kubeClient.Create(ctx, machine); err != nil {
			return err
		}
		c.cache.SetDefault(client.ObjectKeyFromObject(node).String(), machine)
	}
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("machine", machine.Name))

	// Update the status to match the status details of the node
	statusCopy := machineutil.New(node, provisioner)
	stored := machine.DeepCopy()
	machine.Status = statusCopy.Status
	if err := c.kubeClient.Status().Patch(ctx, machine, client.MergeFrom(stored)); err != nil {
		return client.IgnoreNotFound(err)
	}

	// If we know that this machine came from a re-launch (because it doesn't have a machine owner,
	// then we should mark the node for de-provisioning
	if overProvisioned {
		storedNode := node.DeepCopy()
		lo.Must0(controllerutil.SetOwnerReference(machine, node, scheme.Scheme))
		node.Annotations = lo.Assign(node.Annotations, map[string]string{
			v1alpha5.InvoluntaryDisruptionAnnotationKey: v1alpha5.InvoluntaryDisruptionOverprovisionedAnnotationValue,
		})
		if err := c.kubeClient.Patch(ctx, node, client.MergeFrom(storedNode)); err != nil {
			return client.IgnoreNotFound(err)
		}
	}

	// Clean-up the blocking label
	stored = machine.DeepCopy()
	delete(machine.Labels, v1alpha5.AdoptingLabelKey)
	if err := c.kubeClient.Patch(ctx, machine, client.MergeFrom(stored)); err != nil {
		return client.IgnoreNotFound(err)
	}
	logging.FromContext(ctx).Debugf("adopted machine from node")
	return nil
}

func (c *Controller) Builder(_ context.Context, m manager.Manager) controller.Builder {
	return controller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1.Node{}).
		WithEventFilter(predicate.Funcs{
			CreateFunc: func(_ event.CreateEvent) bool { return true },
		}).
		WithOptions(ctrl.Options{MaxConcurrentReconciles: 10}))
}
