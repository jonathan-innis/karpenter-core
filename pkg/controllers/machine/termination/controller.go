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
	"fmt"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/client-go/util/workqueue"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/controllers/machine/termination/terminator"
	terminatorevents "github.com/aws/karpenter-core/pkg/controllers/machine/termination/terminator/events"
	"github.com/aws/karpenter-core/pkg/events"
	corecontroller "github.com/aws/karpenter-core/pkg/operator/controller"
	machineutil "github.com/aws/karpenter-core/pkg/utils/machine"
)

var _ corecontroller.FinalizingTypedController[*v1alpha5.Machine] = (*Controller)(nil)

// Controller is a Machine Controller
type Controller struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
	recorder      events.Recorder
	terminator    *terminator.Terminator
}

// NewController is a constructor for the Machine Controller
func NewController(kubeClient client.Client, cloudProvider cloudprovider.CloudProvider,
	terminator *terminator.Terminator, recorder events.Recorder) corecontroller.Controller {
	return corecontroller.Typed[*v1alpha5.Machine](kubeClient, &Controller{
		kubeClient:    kubeClient,
		cloudProvider: cloudProvider,
		recorder:      recorder,
		terminator:    terminator,
	})
}

func (*Controller) Name() string {
	return "machine"
}

func (c *Controller) Reconcile(_ context.Context, _ *v1alpha5.Machine) (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

func (c *Controller) Finalize(ctx context.Context, machine *v1alpha5.Machine) (reconcile.Result, error) {
	stored := machine.DeepCopy()
	if !controllerutil.ContainsFinalizer(machine, v1alpha5.TerminationFinalizer) {
		return reconcile.Result{}, nil
	}
	if err := c.cleanupNodeForMachine(ctx, machine); err != nil {
		if terminator.IsNodeDrainError(err) {
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("provider-id", machine.Status.ProviderID))
	if machine.Status.ProviderID != "" {
		if err := c.cloudProvider.Delete(ctx, machine); cloudprovider.IgnoreMachineNotFoundError(err) != nil {
			return reconcile.Result{}, fmt.Errorf("terminating cloudprovider instance, %w", err)
		}
	}
	controllerutil.RemoveFinalizer(machine, v1alpha5.TerminationFinalizer)
	if !equality.Semantic.DeepEqual(stored, machine) {
		if err := c.kubeClient.Patch(ctx, machine, client.MergeFrom(stored)); err != nil {
			return reconcile.Result{}, client.IgnoreNotFound(fmt.Errorf("removing machine termination finalizer, %w", err))
		}
		logging.FromContext(ctx).Infof("deleted machine")
	}
	return reconcile.Result{}, nil
}

func (c *Controller) Builder(_ context.Context, m manager.Manager) corecontroller.Builder {
	return corecontroller.Adapt(controllerruntime.
		NewControllerManagedBy(m).
		For(&v1alpha5.Machine{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		WithOptions(controller.Options{
			RateLimiter: workqueue.NewMaxOfRateLimiter(
				workqueue.NewItemExponentialFailureRateLimiter(time.Second, time.Minute),
				// 10 qps, 100 bucket size
				&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
			MaxConcurrentReconciles: 100, // higher concurrency limit since we want fast reaction to node syncing and launch
		}))
}

func (c *Controller) cleanupNodeForMachine(ctx context.Context, machine *v1alpha5.Machine) error {
	node, err := machineutil.NodeForMachine(ctx, c.kubeClient, machine)
	if err != nil {
		// We don't clean the node if we either don't find a node or have violated the single machine to single node invariant
		if machineutil.IsNodeNotFoundError(err) || machineutil.IsDuplicateNodeError(err) {
			return nil
		}
		return err
	}
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("node", node.Name))
	if err = c.terminator.Cordon(ctx, node); err != nil {
		return fmt.Errorf("cordoning node, %w", err)
	}
	if err = c.terminator.Drain(ctx, node); err != nil {
		if terminator.IsNodeDrainError(err) {
			c.recorder.Publish(terminatorevents.NodeFailedToDrain(node, err))
		}
		return fmt.Errorf("draining node, %w", err)
	}
	return client.IgnoreNotFound(c.kubeClient.Delete(ctx, node))
}
