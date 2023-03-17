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

package monitor

import (
	"context"
	"time"

	"github.com/patrickmn/go-cache"
	"go.uber.org/multierr"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/controllers/machine/garbagecollect"
	initialization2 "github.com/aws/karpenter-core/pkg/controllers/machine/initialization"
	liveness2 "github.com/aws/karpenter-core/pkg/controllers/machine/liveness"
	registration2 "github.com/aws/karpenter-core/pkg/controllers/machine/registration"
	"github.com/aws/karpenter-core/pkg/events"
	corecontroller "github.com/aws/karpenter-core/pkg/operator/controller"
	machineutil "github.com/aws/karpenter-core/pkg/utils/machine"
	"github.com/aws/karpenter-core/pkg/utils/result"
)

type machineReconciler interface {
	Reconcile(context.Context, *v1alpha5.Machine) (reconcile.Result, error)
}

var _ corecontroller.TypedController[*v1alpha5.Machine] = (*Controller)(nil)

// Controller is a Machine Controller
type Controller struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
	recorder      events.Recorder

	garbageCollect *garbagecollect.GarbageCollect
	registration   *registration2.Registration
	initialization *initialization2.Initialization
	liveness       *liveness2.Liveness
}

// NewController is a constructor for the Machine Controller
func NewController(clk clock.Clock, kubeClient client.Client, cloudProvider cloudprovider.CloudProvider, recorder events.Recorder) corecontroller.Controller {
	return corecontroller.Typed[*v1alpha5.Machine](kubeClient, &Controller{
		kubeClient:    kubeClient,
		cloudProvider: cloudProvider,
		recorder:      recorder,

		garbageCollect: &garbagecollect.GarbageCollect{kubeClient: kubeClient, cloudProvider: cloudProvider, lastChecked: cache.New(time.Minute*10, time.Second*10)},
		registration:   &registration2.Registration{kubeClient: kubeClient},
		initialization: &initialization2.Initialization{kubeClient: kubeClient},
		liveness:       &liveness2.Liveness{clock: clk, kubeClient: kubeClient},
	})
}

func (*Controller) Name() string {
	return "machine"
}

func (c *Controller) Reconcile(ctx context.Context, machine *v1alpha5.Machine) (reconcile.Result, error) {
	// Add the finalizer immediately since we shouldn't launch if we don't yet have the finalizer.
	// Otherwise, we could leak resources
	stored := machine.DeepCopy()
	controllerutil.AddFinalizer(machine, v1alpha5.TerminationFinalizer)
	if !equality.Semantic.DeepEqual(machine, stored) {
		if err := c.kubeClient.Patch(ctx, machine, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
	}

	stored = machine.DeepCopy()
	var results []reconcile.Result
	var errs error
	for _, reconciler := range []machineReconciler{
		c.garbageCollect,
		c.registration,
		c.initialization,
		c.liveness, // we check liveness last, since we don't want to delete the machine, and then still launch
	} {
		res, err := reconciler.Reconcile(ctx, machine)
		errs = multierr.Append(errs, err)
		results = append(results, res)
	}
	if !equality.Semantic.DeepEqual(stored, machine) {
		statusCopy := machine.DeepCopy()
		if err := c.kubeClient.Patch(ctx, machine, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(multierr.Append(errs, err))
		}
		if err := c.kubeClient.Status().Patch(ctx, statusCopy, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(multierr.Append(errs, err))
		}
	}
	return result.Min(results...), errs
}

func (c *Controller) Builder(ctx context.Context, m manager.Manager) corecontroller.Builder {
	return corecontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1alpha5.Machine{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(
			&source.Kind{Type: &v1.Node{}},
			machineutil.NodeEventHandler(ctx, c.kubeClient),
		).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100, // higher concurrency limit since we want fast reaction to node syncing and launch
		}))
}
