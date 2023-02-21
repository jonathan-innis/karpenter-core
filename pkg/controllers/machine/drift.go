package machine

import (
	"context"
	"fmt"
	"time"

	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/api/errors"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
)

type Drift struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
	lastChecked   *cache.Cache
}

func (d *Drift) Reconcile(ctx context.Context, machine *v1alpha5.Machine) (reconcile.Result, error) {
	if machine.Status.ProviderID == "" {
		return reconcile.Result{}, nil
	}
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("provider-id", machine.Status.ProviderID))
	node, err := nodeForMachine(ctx, d.kubeClient, machine)
	if err != nil {
		if IsNodeNotFoundError(err) || IsDuplicateNodeError(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, nil
	}
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("node", node.Name))
	if _, expireTime, ok := d.lastChecked.GetWithExpiration(client.ObjectKeyFromObject(machine).String()); ok {
		return reconcile.Result{RequeueAfter: time.Until(expireTime)}, nil
	}

	if _, ok := node.Annotations[v1alpha5.VoluntaryDisruptionAnnotationKey]; ok {
		return reconcile.Result{}, nil
	}
	// TODO: Add Provisioner Drift
	drifted, err := d.cloudProvider.IsMachineDrifted(ctx, machine)
	if err != nil {
		return reconcile.Result{}, cloudprovider.IgnoreMachineNotFoundError(fmt.Errorf("getting drift for node, %w", err))
	}
	d.lastChecked.SetDefault(client.ObjectKeyFromObject(machine).String(), nil)
	if !drifted {
		return reconcile.Result{}, nil
	}
	node.Annotations = lo.Assign(node.Annotations, map[string]string{
		v1alpha5.VoluntaryDisruptionAnnotationKey: v1alpha5.VoluntaryDisruptionDriftedAnnotationValue,
	})
	if err = d.kubeClient.Update(ctx, node); err != nil {
		if errors.IsConflict(err) {
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, err
	}
	// Requeue after 5 minutes for the cache TTL
	return reconcile.Result{RequeueAfter: 5 * time.Minute}, nil
}
