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

	"go.uber.org/multierr"
	"golang.org/x/time/rate"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
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
	kubeClient client.Client

	registration   *Registration
	initialization *Initialization
	timeout        *Timeout
}

// NewController is a constructor for the Machine Controller
func NewController(clk clock.Clock, kubeClient client.Client) corecontroller.Controller {
	return corecontroller.Typed[*v1alpha5.Machine](kubeClient, &Controller{
		kubeClient: kubeClient,

		registration:   &Registration{kubeClient: kubeClient},
		initialization: &Initialization{kubeClient: kubeClient},
		timeout:        &Timeout{clock: clk, kubeClient: kubeClient},
	})
}

func (*Controller) Name() string {
	return "machine_monitor"
}

func (c *Controller) Reconcile(ctx context.Context, machine *v1alpha5.Machine) (reconcile.Result, error) {
	if !machine.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}

	stored := machine.DeepCopy()
	var results []reconcile.Result
	var errs error
	for _, reconciler := range []machineReconciler{
		c.registration,
		c.initialization,
		c.timeout, // we check liveness last, since we don't want to delete the machine, and then still launch
	} {
		res, err := reconciler.Reconcile(ctx, machine)
		errs = multierr.Append(errs, err)
		results = append(results, res)
	}
	if !equality.Semantic.DeepEqual(stored, machine) {
		if err := c.kubeClient.Status().Patch(ctx, machine, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(multierr.Append(errs, err))
		}
	}
	return result.Min(results...), errs
}

func (c *Controller) Builder(ctx context.Context, m manager.Manager) corecontroller.Builder {
	return corecontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1alpha5.Machine{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool { return false }))).
		Watches(
			&source.Kind{Type: &v1.Node{}},
			machineutil.NodeEventHandler(ctx, c.kubeClient),
		).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewMaxOfRateLimiter(
				workqueue.NewItemExponentialFailureRateLimiter(time.Second, time.Minute),
				// 10 qps, 100 bucket size
				&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			MaxConcurrentReconciles: 10, // higher concurrency limit since we want fast reaction to node syncing and launch
		}))
}
