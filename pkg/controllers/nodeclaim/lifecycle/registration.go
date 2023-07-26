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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/logging"
	"knative.dev/pkg/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
	"github.com/aws/karpenter-core/pkg/metrics"
	"github.com/aws/karpenter-core/pkg/scheduling"
	nodeclaimutil "github.com/aws/karpenter-core/pkg/utils/nodeclaim"
)

type Registration struct {
	kubeClient client.Client
}

func (r *Registration) Reconcile(ctx context.Context, nodeClaim *v1beta1.NodeClaim) (reconcile.Result, error) {
	if nodeClaim.StatusConditions().GetCondition(v1beta1.NodeRegistered).IsTrue() {
		return reconcile.Result{}, nil
	}
	if !nodeClaim.StatusConditions().GetCondition(v1beta1.NodeLaunched).IsTrue() {
		nodeClaim.StatusConditions().MarkFalse(v1beta1.NodeRegistered, "NodeNotLaunched", "Node is not launched")
		return reconcile.Result{}, nil
	}

	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("provider-id", nodeClaim.Status.ProviderID))
	node, err := nodeclaimutil.NodeForNodeClaim(ctx, r.kubeClient, nodeClaim)
	if err != nil {
		if nodeclaimutil.IsNodeNotFoundError(err) {
			nodeClaim.StatusConditions().MarkFalse(v1beta1.NodeRegistered, "NodeNotFound", "Node not registered with cluster")
			return reconcile.Result{}, nil
		}
		if nodeclaimutil.IsDuplicateNodeError(err) {
			nodeClaim.StatusConditions().MarkFalse(v1beta1.NodeRegistered, "MultipleNodesFound", "Invariant violated, nodeclaim matched multiple nodes")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("getting node for nodeclaim, %w", err)
	}
	ctx = logging.WithLogger(ctx, logging.FromContext(ctx).With("node", node.Name))
	if err = r.syncNode(ctx, nodeClaim, node); err != nil {
		return reconcile.Result{}, fmt.Errorf("syncing node, %w", err)
	}
	logging.FromContext(ctx).Debugf("registered node")
	nodeClaim.StatusConditions().MarkTrue(v1beta1.NodeRegistered)
	nodeClaim.Status.NodeName = node.Name
	metrics.NodeClaimsRegisteredCounter.With(prometheus.Labels{
		metrics.NodePoolLabel: nodeClaim.Labels[v1beta1.NodePoolLabelKey],
	}).Inc()
	metrics.NodesCreatedCounter.With(prometheus.Labels{
		metrics.NodePoolLabel: nodeClaim.Labels[v1beta1.NodePoolLabelKey],
	}).Inc()
	return reconcile.Result{}, nil
}

func (r *Registration) syncNode(ctx context.Context, nodeClaim *v1beta1.NodeClaim, node *v1.Node) error {
	stored := node.DeepCopy()
	controllerutil.AddFinalizer(node, v1beta1.TerminationFinalizer)

	// Remove any provisioner owner references since we own them
	node.OwnerReferences = lo.Reject(node.OwnerReferences, func(o metav1.OwnerReference, _ int) bool {
		return o.Kind == "Provisioner"
	})
	node.OwnerReferences = append(node.OwnerReferences, metav1.OwnerReference{
		APIVersion:         v1beta1.SchemeGroupVersion.String(),
		Kind:               "NodeClaim",
		Name:               nodeClaim.Name,
		UID:                nodeClaim.UID,
		BlockOwnerDeletion: ptr.Bool(true),
	})

	node.Labels = lo.Assign(node.Labels, nodeClaim.Labels, map[string]string{
		v1alpha5.LabelNodeRegistered: "true",
	})
	node.Annotations = lo.Assign(node.Annotations, nodeClaim.Annotations)
	// Sync all taints inside NodeClaim into the NodeClaim taints
	node.Spec.Taints = scheduling.Taints(node.Spec.Taints).Merge(nodeClaim.Spec.Taints)
	node.Spec.Taints = scheduling.Taints(node.Spec.Taints).Merge(nodeClaim.Spec.StartupTaints)

	if !equality.Semantic.DeepEqual(stored, node) {
		if err := r.kubeClient.Patch(ctx, node, client.MergeFrom(stored)); err != nil {
			return fmt.Errorf("syncing node labels, %w", err)
		}
	}
	return nil
}

// backPropagateRegistrationLabel ports the `karpenter.sh/registered` label onto nodes that are registered by the Machine
// but don't have this label on the Node yet
func (r *Registration) backPropagateRegistrationLabel(ctx context.Context, machine *v1alpha5.Machine) error {
	node, err := machineutil.NodeForMachine(ctx, r.kubeClient, machine)
	stored := node.DeepCopy()
	if err != nil {
		return machineutil.IgnoreDuplicateNodeError(machineutil.IgnoreNodeNotFoundError(err))
	}
	node.Labels = lo.Assign(node.Labels, map[string]string{
		v1alpha5.LabelNodeRegistered: "true",
	})
	if !equality.Semantic.DeepEqual(stored, node) {
		if err := r.kubeClient.Patch(ctx, node, client.MergeFrom(stored)); err != nil {
			return fmt.Errorf("syncing node registration label, %w", err)
		}
	}
	return nil
}
