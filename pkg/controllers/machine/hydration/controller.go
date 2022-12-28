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
	"fmt"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/storage/names"
	"knative.dev/pkg/logging"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha1"
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	corecontroller "github.com/aws/karpenter-core/pkg/operator/controller"
	"github.com/aws/karpenter-core/pkg/operator/scheme"
	"github.com/aws/karpenter-core/pkg/utils/sets"
)

type Controller struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider) corecontroller.Controller {
	return corecontroller.Typed[*v1.Node](kubeClient, &Controller{
		kubeClient:    kubeClient,
		cloudProvider: cloudProvider,
	})
}

func (c *Controller) Reconcile(ctx context.Context, node *v1.Node) (reconcile.Result, error) {
	if node.Spec.ProviderID == "" {
		return reconcile.Result{}, nil
	}
	provisionerName, ok := node.Labels[v1alpha5.ProvisionerNameLabelKey]
	if !ok {
		return reconcile.Result{}, nil
	}
	machineList := &v1alpha1.MachineList{}
	if err := c.kubeClient.List(ctx, machineList); err != nil {
		return reconcile.Result{}, fmt.Errorf("listing machines, %w", err)
	}
	machineNames := sets.New[string](lo.Map(machineList.Items, func(m v1alpha1.Machine, _ int) string {
		return m.Name
	})...)
	if _, ok = lo.Find(machineList.Items, func(m v1alpha1.Machine) bool {
		return m.Status.ProviderID == node.Spec.ProviderID
	}); ok {
		return reconcile.Result{}, nil
	}
	provisioner := &v1alpha5.Provisioner{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: provisionerName}, provisioner); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting provisioner, %w", err)
	}
	if err := c.hydrate(ctx, node, provisioner, machineNames); err != nil {
		return reconcile.Result{}, fmt.Errorf("hydrating machine from node, %w", err)
	}
	return reconcile.Result{}, nil
}

func (c *Controller) hydrate(ctx context.Context, node *v1.Node, provisioner *v1alpha5.Provisioner, machineNames sets.Set[string]) error {
	machine := v1alpha1.MachineFromNode(node)
	machine.Name = generateMachineName(machineNames, provisioner.Name) // so we know the name before creation
	machine.Labels = lo.Assign(machine.Labels, map[string]string{
		v1alpha5.MachineNameLabelKey: machine.Name,
	})
	machine.Spec.Kubelet = provisioner.Spec.KubeletConfiguration

	logging.WithLogger(ctx, logging.FromContext(ctx).With("machine", machine.Name))

	if provisioner.Spec.Provider == nil && provisioner.Spec.ProviderRef == nil {
		return fmt.Errorf("provisioner '%s' has no 'spec.provider' or 'spec.providerRef'", provisioner.Name)
	}
	if provisioner.Spec.ProviderRef != nil {
		machine.Spec.MachineTemplateRef = provisioner.Spec.ProviderRef.ToObjectReference()
	} else {
		machine.Annotations[v1alpha5.ProviderCompatabilityAnnotationKey] = v1alpha5.ProviderAnnotation(provisioner.Spec.Provider)
	}
	lo.Must0(controllerutil.SetOwnerReference(provisioner, machine, scheme.Scheme)) // shouldn't fail

	// Hydrates the machine with the correct values if the instance exists at the cloudprovider
	if err := c.cloudProvider.HydrateMachine(ctx, machine); err != nil {
		if cloudprovider.IsInstanceNotFound(err) {
			return nil
		}
		return fmt.Errorf("hydrating machine, %w", err)
	}
	if err := c.kubeClient.Create(ctx, machine); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating hydrated machine from node '%s', %w", node.Name, err)
	}
	logging.FromContext(ctx).Debugf("hydrated machine from node")
	return nil
}

func generateMachineName(existingNames sets.Set[string], provisionerName string) string {
	proposed := names.SimpleNameGenerator.GenerateName(provisionerName + "-")
	for existingNames.Has(proposed) {
		proposed = names.SimpleNameGenerator.GenerateName(provisionerName + "-")
	}
	return proposed
}

func (c *Controller) Builder(_ context.Context, m manager.Manager) corecontroller.Builder {
	return corecontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1.Node{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}))
}
