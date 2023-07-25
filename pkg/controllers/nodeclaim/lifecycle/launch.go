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

package lifecycle

import (
	"context"
	"fmt"

	"github.com/patrickmn/go-cache"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/events"
	"github.com/aws/karpenter-core/pkg/metrics"
	"github.com/aws/karpenter-core/pkg/scheduling"
)

type Launch struct {
	kubeClient    client.Client
	cloudProvider cloudprovider.CloudProvider
	cache         *cache.Cache // exists due to eventual consistency on the cache
	recorder      events.Recorder
}

func (l *Launch) Reconcile(ctx context.Context, nodeClaim *v1beta1.NodeClaim) (reconcile.Result, error) {
	if nodeClaim.StatusConditions().GetCondition(v1beta1.NodeLaunched).IsTrue() {
		return reconcile.Result{}, nil
	}

	var err error
	var created *v1beta1.NodeClaim

	// One of the following scenarios can happen with a NodeClaim that isn't marked as launched:
	//  1. It was already launched by the CloudProvider but the client-go cache wasn't updated quickly enough or
	//     patching failed on the status. In this case, we use the in-memory cached value for the created machine.
	//  2. It is a standard machine launch where we should call CloudProvider Create() and fill in details of the launched
	//     machine into the NodeClaim CR.
	if ret, ok := l.cache.Get(string(nodeClaim.UID)); ok {
		created = ret.(*v1beta1.NodeClaim)
	} else {
		created, err = l.launchNode(ctx, nodeClaim)
	}
	// Either the machine launch/linking failed or the machine was deleted due to InsufficientCapacity/NotFound
	if err != nil || created == nil {
		return reconcile.Result{}, err
	}
	l.cache.SetDefault(string(nodeClaim.UID), created)
	PopulateMachineDetails(nodeClaim, created)
	nodeClaim.StatusConditions().MarkTrue(v1beta1.NodeLaunched)
	metrics.NodeClaimsLaunchedCounter.With(prometheus.Labels{
		metrics.ProvisionerLabel: nodeClaim.Labels[v1alpha5.ProvisionerNameLabelKey],
	}).Inc()
	return reconcile.Result{}, nil
}

func (l *Launch) linkMachine(ctx context.Context, machine *v1alpha5.NodeClaim) (*v1beta1.NodeClaim, error) {
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("provider-id", machine.Annotations[v1alpha5.MachineLinkedAnnotationKey]))
	created, err := l.cloudProvider.Get(ctx, machine.Annotations[v1alpha5.MachineLinkedAnnotationKey])
	if err != nil {
		if !cloudprovider.IsMachineNotFoundError(err) {
			machine.StatusConditions().MarkFalse(v1alpha5.MachineLaunched, "LinkFailed", truncateMessage(err.Error()))
			return nil, fmt.Errorf("linking machine, %w", err)
		}
		if err = l.kubeClient.Delete(ctx, machine); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		logging.FromContext(ctx).Debugf("garbage collected machine with no cloudprovider representation")
		metrics.NodeClaimsTerminatedCounter.With(prometheus.Labels{
			metrics.ReasonLabel:      "garbage_collected",
			metrics.ProvisionerLabel: machine.Labels[v1alpha5.ProvisionerNameLabelKey],
		}).Inc()
		return nil, nil
	}
	logging.FromContext(ctx).With(
		"provider-id", created.Status.ProviderID,
		"instance-type", created.Labels[v1.LabelInstanceTypeStable],
		"zone", created.Labels[v1.LabelTopologyZone],
		"capacity-type", created.Labels[v1alpha5.LabelCapacityType],
		"allocatable", created.Status.Allocatable).Infof("linked machine")
	return created, nil
}

func (l *Launch) launchNode(ctx context.Context, nodeClaim *v1beta1.NodeClaim) (*v1beta1.NodeClaim, error) {
	created, err := l.cloudProvider.Create(ctx, nodeClaim)
	if err != nil {
		switch {
		case cloudprovider.IsInsufficientCapacityError(err):
			l.recorder.Publish(events.Event{
				InvolvedObject: nodeClaim,
				Type:           v1.EventTypeWarning,
				Reason:         "InsufficientCapacityError",
				Message:        fmt.Sprintf("NodeClaim %s event: %s", nodeClaim.Name, err),
				DedupeValues:   []string{nodeClaim.Name},
			})
			logging.FromContext(ctx).Error(err)
			if err = l.kubeClient.Delete(ctx, nodeClaim); err != nil {
				return nil, client.IgnoreNotFound(err)
			}
			metrics.NodeClaimsTerminatedCounter.With(prometheus.Labels{
				metrics.ReasonLabel:      "insufficient_capacity",
				metrics.ProvisionerLabel: nodeClaim.Labels[v1alpha5.ProvisionerNameLabelKey],
			}).Inc()
			return nil, nil
		default:
			nodeClaim.StatusConditions().MarkFalse(v1beta1.NodeLaunched, "LaunchFailed", truncateMessage(err.Error()))
			return nil, fmt.Errorf("creating machine, %w", err)
		}
	}
	logging.FromContext(ctx).With(
		"provider-id", created.Status.ProviderID,
		"instance-type", created.Labels[v1.LabelInstanceTypeStable],
		"zone", created.Labels[v1.LabelTopologyZone],
		"capacity-type", created.Labels[v1alpha5.LabelCapacityType],
		"allocatable", created.Status.Allocatable).Infof("launched nodeClaim")
	return created, nil
}

func PopulateMachineDetails(machine, retrieved *v1beta1.NodeClaim) {
	// These are ordered in priority order so that user-defined machine labels and requirements trump retrieved labels
	// or the static machine labels
	machine.Labels = lo.Assign(
		retrieved.Labels, // CloudProvider-resolved labels
		scheduling.NewNodeSelectorRequirements(machine.Spec.Requirements...).Labels(), // Single-value requirement resolved labels
		machine.Labels, // User-defined labels
	)
	machine.Annotations = lo.Assign(machine.Annotations, retrieved.Annotations)
	machine.Status.ProviderID = retrieved.Status.ProviderID
	machine.Status.Allocatable = retrieved.Status.Allocatable
	machine.Status.Capacity = retrieved.Status.Capacity
}

func truncateMessage(msg string) string {
	if len(msg) < 300 {
		return msg
	}
	return msg[:300] + "..."
}
