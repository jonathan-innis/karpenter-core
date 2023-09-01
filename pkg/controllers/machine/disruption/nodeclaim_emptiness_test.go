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

package disruption_test

import (
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
	"github.com/aws/karpenter-core/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/aws/karpenter-core/pkg/test/expectations"
)

var _ = Describe("NodeClaim/Emptiness", func() {
	var nodePool *v1beta1.NodePool
	var nodeClaim *v1beta1.NodeClaim
	var node *v1.Node
	BeforeEach(func() {
		nodePool = test.NodePool()
		nodePool.Spec.Deprovisioning.ConsolidationPolicy = v1beta1.ConsolidationPolicyWhenEmpty
		nodePool.Spec.Deprovisioning.ConsolidationTTL.Duration = time.Second * 30
		nodeClaim, node = test.NodeClaimAndNode(v1beta1.NodeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					v1beta1.NodePoolLabelKey:   nodePool.Name,
					v1.LabelInstanceTypeStable: "default-instance-type", // need the instance type for the cluster state update
				},
			},
		})
	})

	It("should mark NodeClaims as empty", func() {
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node)
		ExpectMakeNodeClaimsInitialized(ctx, env.Client, nodeClaim)

		ExpectReconcileSucceeded(ctx, nodeClaimDisruptionController, client.ObjectKeyFromObject(nodeClaim))

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.StatusConditions().GetCondition(v1beta1.NodeEmpty).IsTrue()).To(BeTrue())
	})
	It("should remove the status condition from the nodeClaim when emptiness is disabled", func() {
		nodePool.Spec.Deprovisioning.ConsolidationPolicy = v1beta1.ConsolidationPolicyNever
		nodePool.Spec.Deprovisioning.ConsolidationTTL.Duration = -1
		nodeClaim.StatusConditions().MarkTrue(v1beta1.NodeEmpty)
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node)
		ExpectMakeNodeClaimsInitialized(ctx, env.Client, nodeClaim)

		ExpectReconcileSucceeded(ctx, nodeClaimDisruptionController, client.ObjectKeyFromObject(nodeClaim))

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.StatusConditions().GetCondition(v1beta1.NodeEmpty)).To(BeNil())
	})
	It("should remove the status condition from the nodeClaim when the nodeClaim initialization condition is false", func() {
		nodeClaim.StatusConditions().MarkTrue(v1beta1.NodeEmpty)
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node)
		ExpectMakeNodeClaimsInitialized(ctx, env.Client, nodeClaim)
		nodeClaim.StatusConditions().MarkFalse(v1beta1.NodeInitialized, "", "")
		ExpectApplied(ctx, env.Client, nodeClaim)

		ExpectReconcileSucceeded(ctx, nodeClaimDisruptionController, client.ObjectKeyFromObject(nodeClaim))

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.StatusConditions().GetCondition(v1beta1.NodeEmpty)).To(BeNil())
	})
	It("should remove the status condition from the nodeClaim when the nodeClaim initialization condition doesn't exist", func() {
		nodeClaim.StatusConditions().MarkTrue(v1beta1.NodeEmpty)
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node)
		ExpectMakeNodeClaimsInitialized(ctx, env.Client, nodeClaim)
		nodeClaim.Status.Conditions = lo.Reject(nodeClaim.Status.Conditions, func(s apis.Condition, _ int) bool {
			return s.Type == v1beta1.NodeInitialized
		})
		ExpectApplied(ctx, env.Client, nodeClaim)

		ExpectReconcileSucceeded(ctx, nodeClaimDisruptionController, client.ObjectKeyFromObject(nodeClaim))

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.StatusConditions().GetCondition(v1beta1.NodeEmpty)).To(BeNil())
	})
	It("should remove the status condition from the nodeClaim when the node doesn't exist", func() {
		nodeClaim.StatusConditions().MarkTrue(v1beta1.NodeEmpty)
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim)
		ExpectMakeNodeClaimsInitialized(ctx, env.Client, nodeClaim)

		ExpectReconcileSucceeded(ctx, nodeClaimDisruptionController, client.ObjectKeyFromObject(nodeClaim))

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.StatusConditions().GetCondition(v1beta1.NodeEmpty)).To(BeNil())
	})
	It("should remove the status condition from non-empty NodeClaims", func() {
		nodeClaim.StatusConditions().MarkTrue(v1beta1.NodeEmpty)
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node)
		ExpectMakeNodeClaimsInitialized(ctx, env.Client, nodeClaim)

		ExpectApplied(ctx, env.Client, test.Pod(test.PodOptions{
			NodeName:   node.Name,
			Conditions: []v1.PodCondition{{Type: v1.PodReady, Status: v1.ConditionTrue}},
		}))

		ExpectReconcileSucceeded(ctx, nodeClaimDisruptionController, client.ObjectKeyFromObject(nodeClaim))

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.StatusConditions().GetCondition(v1beta1.NodeEmpty)).To(BeNil())
	})
	It("should remove the status condition when the cluster state node is nominated", func() {
		nodeClaim.StatusConditions().MarkTrue(v1beta1.NodeEmpty)
		ExpectApplied(ctx, env.Client, nodePool, nodeClaim, node)
		ExpectMakeNodeClaimsInitialized(ctx, env.Client, nodeClaim)

		// Add the node to the cluster state and nominate it in the internal cluster state
		Expect(cluster.UpdateNode(ctx, node)).To(Succeed())
		cluster.NominateNodeForPod(ctx, node.Spec.ProviderID)

		result := ExpectReconcileSucceeded(ctx, nodeClaimDisruptionController, client.ObjectKeyFromObject(nodeClaim))
		Expect(result.RequeueAfter).To(Equal(time.Second * 30))

		nodeClaim = ExpectExists(ctx, env.Client, nodeClaim)
		Expect(nodeClaim.StatusConditions().GetCondition(v1beta1.NodeEmpty)).To(BeNil())
	})
})
