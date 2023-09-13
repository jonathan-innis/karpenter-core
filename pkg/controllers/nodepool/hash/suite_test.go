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

package hash_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "knative.dev/pkg/logging/testing"

	"github.com/aws/karpenter-core/pkg/apis"
	"github.com/aws/karpenter-core/pkg/controllers/nodepool/hash"
	"github.com/aws/karpenter-core/pkg/operator/controller"
	"github.com/aws/karpenter-core/pkg/operator/scheme"
	"github.com/aws/karpenter-core/pkg/test"
	nodeclaimutil "github.com/aws/karpenter-core/pkg/utils/nodeclaim"
	nodepoolutil "github.com/aws/karpenter-core/pkg/utils/nodepool"
)

var provisionerController controller.Controller
var nodePoolController controller.Controller
var ctx context.Context
var env *test.Environment

func TestAPIs(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "ProvisionerController")
}

var _ = BeforeSuite(func() {
	nodeclaimutil.EnableNodeClaims = true
	nodepoolutil.EnableNodePools = true
	env = test.NewEnvironment(scheme.Scheme, test.WithCRDs(apis.CRDs...))
	provisionerController = hash.NewProvisionerController(env.Client)
	nodePoolController = hash.NewNodePoolController(env.Client)
})

var _ = AfterSuite(func() {
	Expect(env.Stop()).To(Succeed(), "Failed to stop environment")
})
