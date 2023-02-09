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

package garbagecollect

import (
	"context"
	"fmt"
	"time"

	"github.com/samber/lo"
	"go.uber.org/multierr"
	v1 "k8s.io/api/core/v1"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/settings"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/controllers/state"
	"github.com/aws/karpenter-core/pkg/operator/controller"
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
	return "garbagecollect"
}

func (c *Controller) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	if settings.FromContext(ctx).TTLAfterNotRegistered == nil {
		return reconcile.Result{}, nil
	}
	nodeList := &v1.NodeList{}
	if err := c.kubeClient.List(ctx, nodeList); err != nil {
		return reconcile.Result{}, err
	}
	machines, err := c.cloudProvider.List(ctx)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("listing machines, %w", err)
	}
	nodeProviderIds := sets.New[string](lo.Map(nodeList.Items, func(n v1.Node, _ int) string { return n.Spec.ProviderID })...)
	for _, machine := range machines {
		if !nodeProviderIds.Has(machine.Status.ProviderID) && machine.CreationTimestamp.Add(settings.FromContext(ctx).TTLAfterNotRegistered.Duration).Before(time.Now()) {
			c.cluster.Delete(machine.Status.ProviderID)
			if e := c.cloudProvider.Delete(ctx, machine); e != nil {
				err = multierr.Append(err, e)
			}
			logging.FromContext(ctx).With("provider-id", machine.Status.ProviderID).Debugf("garbage collecting machine")
		}
	}
	return reconcile.Result{RequeueAfter: time.Minute * 5}, err
}

func (c *Controller) Builder(_ context.Context, m manager.Manager) controller.Builder {
	return controller.NewSingletonManagedBy(m)
}
