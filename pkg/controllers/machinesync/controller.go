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

package machinesync

import (
	"context"
	"fmt"
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/controllers/state"
	"github.com/aws/karpenter-core/pkg/operator/controller"
	machineutil "github.com/aws/karpenter-core/pkg/utils/machine"
	"github.com/aws/karpenter-core/pkg/utils/sets"
)

type Controller struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
	cluster       *state.Cluster
}

func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider, cluster *state.Cluster) *Controller {
	return &Controller{
		kubeClient:    kubeClient,
		cloudProvider: cloudProvider,
		cluster:       cluster,
	}
}

func (c *Controller) Name() string {
	return "machinesync"
}

func (c *Controller) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	machineList := &v1alpha5.MachineList{}
	if err := c.kubeClient.List(ctx, machineList); err != nil {
		return reconcile.Result{}, err
	}
	retrieved, err := c.cloudProvider.List(ctx)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("listing machines, %w", err)
	}

	resolvedMachines := lo.Filter(machineList.Items, func(m v1alpha5.Machine, _ int) bool { return m.Status.ProviderID != "" })
	machineProviderIDs := sets.New[string](lo.Map(resolvedMachines, func(m v1alpha5.Machine, _ int) string { return m.Status.ProviderID })...)
	retrievedProviderIDs := sets.New[string](lo.Map(retrieved, func(m *v1alpha5.Machine, _ int) string { return m.Status.ProviderID })...)

	for i := range resolvedMachines {
		if !retrievedProviderIDs.Has(resolvedMachines[i].Status.ProviderID) {
			if err = c.kubeClient.Delete(ctx, &resolvedMachines[i]); err != nil {
				return reconcile.Result{}, err
			}
		}
	}
	for i := range retrieved {
		if !machineProviderIDs.Has(retrieved[i].Status.ProviderID) && retrieved[i].CreationTimestamp.Add(time.Minute).Before(time.Now()) {
			provisionerName := retrieved[i].Labels[v1alpha5.ProvisionerNameLabelKey]
			provisioner := &v1alpha5.Provisioner{}
			if err = c.kubeClient.Get(ctx, types.NamespacedName{Name: provisionerName}, provisioner); err != nil {
				if errors.IsNotFound(err) {
					if err = c.cloudProvider.Delete(ctx, retrieved[i]); err != nil {
						return reconcile.Result{}, err
					}
					continue
				}
				return reconcile.Result{}, err
			}
			machine := machineutil.New(&v1.Node{}, provisioner)
			machine.GenerateName = fmt.Sprintf("%s-", provisionerName)
			machine.Annotations = lo.Assign(machine.Annotations, map[string]string{
				v1alpha5.MachineLinkedAnnotationKey: retrieved[i].Status.ProviderID,
			})
			if _, ok := retrieved[i].Labels[v1alpha5.OwnedLabelKey]; ok {
				machine.Annotations[v1alpha5.InvoluntaryDisruptionAnnotationKey] = v1alpha5.InvoluntaryDisruptionOverprovisionedAnnotationValue
			}
			if err = c.kubeClient.Create(ctx, machine); err != nil {
				return reconcile.Result{}, err
			}
		}
	}
	return reconcile.Result{RequeueAfter: time.Minute * 2}, err
}

func (c *Controller) Builder(_ context.Context, m manager.Manager) controller.Builder {
	return controller.NewSingletonManagedBy(m)
}
