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

package garbagecollect_test

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/controllers/machine/monitor"
	"github.com/aws/karpenter-core/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/aws/karpenter-core/pkg/test/expectations"
)

var _ = Describe("GarbageCollection", func() {
	var provisioner *v1alpha5.Provisioner

	BeforeEach(func() {
		provisioner = test.Provisioner()
	})
	It("should delete the Machine when the Node never appears and the instance is gone", func() {
		machine := test.Machine(v1alpha5.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					v1alpha5.ProvisionerNameLabelKey: provisioner.Name,
				},
			},
		})
		ExpectApplied(monitor.ctx, monitor.env.Client, provisioner, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)

		// Delete the machine from the cloudprovider
		Expect(monitor.cloudProvider.Delete(monitor.ctx, machine)).To(Succeed())

		// Wait for the cache expiration to complete
		time.Sleep(time.Second)

		// Expect the Machine to be removed now that the Instance is gone
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine)) // Reconcile again to handle termination flow
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)
	})
	It("shouldn't delete the Machine when the Node isn't there but the instance is there", func() {
		machine := test.Machine(v1alpha5.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{
					v1alpha5.ProvisionerNameLabelKey: provisioner.Name,
				},
			},
		})
		ExpectApplied(monitor.ctx, monitor.env.Client, provisioner, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)

		// Wait for the cache expiration to complete
		time.Sleep(time.Second)

		// Reconcile the Machine. It should not be deleted by this flow since it has never been registered
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectExists(monitor.ctx, monitor.env.Client, machine)
	})
})
