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

package machine_test

import (
	"fmt"
	"sync"
	"time"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider/fake"
	"github.com/aws/karpenter-core/pkg/controllers/machine/monitor"
	"github.com/aws/karpenter-core/pkg/controllers/machine/terminator"
	"github.com/aws/karpenter-core/pkg/test"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "github.com/aws/karpenter-core/pkg/test/expectations"
)

var defaultOwnerRefs = []metav1.OwnerReference{{Kind: "ReplicaSet", APIVersion: "appsv1", Name: "rs", UID: "1234567890"}}

var _ = Describe("Termination", func() {
	var provisioner *v1alpha5.Provisioner
	var machine *v1alpha5.Machine
	var node *v1.Node

	BeforeEach(func() {
		provisioner = test.Provisioner()
		machine, node = test.MachineAndNode(v1alpha5.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Finalizers: []string{
					v1alpha5.TerminationFinalizer,
				},
				Labels: map[string]string{
					v1alpha5.ProvisionerNameLabelKey: provisioner.Name,
				},
			},
			Spec: v1alpha5.MachineSpec{
				Resources: v1alpha5.ResourceRequirements{
					Requests: v1.ResourceList{
						v1.ResourceCPU:          resource.MustParse("2"),
						v1.ResourceMemory:       resource.MustParse("50Mi"),
						v1.ResourcePods:         resource.MustParse("5"),
						fake.ResourceGPUVendorA: resource.MustParse("1"),
					},
				},
			},
			Status: v1alpha5.MachineStatus{
				ProviderID: test.RandomProviderID(),
				Capacity: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("10"),
					v1.ResourceMemory: resource.MustParse("100Mi"),
					v1.ResourcePods:   resource.MustParse("110"),
				},
				Allocatable: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("8"),
					v1.ResourceMemory: resource.MustParse("80Mi"),
					v1.ResourcePods:   resource.MustParse("110"),
				},
			},
		})
		node.Finalizers = []string{v1alpha5.TerminationFinalizer}
	})
	It("should cordon, drain, and delete the Machine on terminate", func() {
		pods := []*v1.Pod{
			test.Pod(test.PodOptions{NodeName: node.Name, ObjectMeta: metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs}}),
			test.Pod(test.PodOptions{NodeName: node.Name, ObjectMeta: metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs}}),
		}
		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, pods[0], pods[1])

		// Trigger Finalization Flow
		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Expect the pods to be evicted
		ExpectEvicted(monitor.env.Client, pods[0], pods[1])

		// Expect node to exist and be draining, but not deleted
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNodeDraining(monitor.env.Client, node.Name)

		ExpectDeleted(monitor.ctx, monitor.env.Client, pods[1])

		// Expect node to exist and be draining, but not deleted
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNodeDraining(monitor.env.Client, node.Name)

		ExpectDeleted(monitor.ctx, monitor.env.Client, pods[0])

		// Reconcile to delete node
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)
	})
	It("should not race if deleting machines in parallel", func() {
		var machines []*v1alpha5.Machine
		for i := 0; i < 10; i++ {
			machine, node = test.MachineAndNode(v1alpha5.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Finalizers: []string{
						v1alpha5.TerminationFinalizer,
					},
					Labels: map[string]string{
						v1alpha5.ProvisionerNameLabelKey: provisioner.Name,
					},
				},
				Spec: v1alpha5.MachineSpec{
					Resources: v1alpha5.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:          resource.MustParse("2"),
							v1.ResourceMemory:       resource.MustParse("50Mi"),
							v1.ResourcePods:         resource.MustParse("5"),
							fake.ResourceGPUVendorA: resource.MustParse("1"),
						},
					},
				},
				Status: v1alpha5.MachineStatus{
					ProviderID: test.RandomProviderID(),
					Capacity: v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("10"),
						v1.ResourceMemory: resource.MustParse("100Mi"),
						v1.ResourcePods:   resource.MustParse("110"),
					},
					Allocatable: v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse("8"),
						v1.ResourceMemory: resource.MustParse("80Mi"),
						v1.ResourcePods:   resource.MustParse("110"),
					},
				},
			})
			ExpectApplied(monitor.ctx, monitor.env.Client, machine, node)
			Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
			machines = append(machines, machine)
		}

		var wg sync.WaitGroup
		// this is enough to trip the race detector
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(machine *v1alpha5.Machine) {
				defer GinkgoRecover()
				defer wg.Done()
				ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
			}(machines[i])
		}
		wg.Wait()
		ExpectNotFound(monitor.ctx, monitor.env.Client, lo.Map(machines, func(m *v1alpha5.Machine, _ int) client.Object { return m })...)
	})
	It("should exclude machines from load balancers when terminating", func() {
		// This is a kludge to prevent the node from being deleted before we can
		// inspect its labels
		podNoEvict := test.Pod(test.PodOptions{
			NodeName: node.Name,
			ObjectMeta: metav1.ObjectMeta{
				Annotations:     map[string]string{v1alpha5.DoNotEvictPodAnnotationKey: "true"},
				OwnerReferences: defaultOwnerRefs,
			},
		})

		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, podNoEvict)

		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Node should now have the nodeExcludeLoadBalancer label
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		node = ExpectExists(monitor.ctx, monitor.env.Client, node)
		Expect(node.Labels[v1.LabelNodeExcludeBalancers]).Should(Equal("karpenter"))
	})
	It("should not evict pods that tolerate unschedulable taint", func() {
		podEvict := test.Pod(test.PodOptions{NodeName: node.Name, ObjectMeta: metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs}})
		podSkip := test.Pod(test.PodOptions{
			NodeName:    node.Name,
			Tolerations: []v1.Toleration{{Key: v1.TaintNodeUnschedulable, Operator: v1.TolerationOpExists, Effect: v1.TaintEffectNoSchedule}},
			ObjectMeta:  metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs},
		})
		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, podEvict, podSkip)

		// Trigger Finalization Flow
		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Expect node to exist and be draining
		ExpectNodeDraining(monitor.env.Client, node.Name)

		// Expect podEvict to be evicting, and delete it
		ExpectEvicted(monitor.env.Client, podEvict)
		ExpectDeleted(monitor.ctx, monitor.env.Client, podEvict)

		// Reconcile to delete the machine
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)
	})
	It("should delete machines that have pods without an ownerRef", func() {
		pod := test.Pod(test.PodOptions{
			NodeName: node.Name,
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: nil,
			},
		})

		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, pod)

		// Trigger Finalization Flow
		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Expect pod with no owner ref to be enqueued for eviction
		ExpectEvicted(monitor.env.Client, pod)

		// Expect node to exist and be draining
		ExpectNodeDraining(monitor.env.Client, node.Name)

		// Delete no owner refs pod to simulate successful eviction
		ExpectDeleted(monitor.ctx, monitor.env.Client, pod)

		// Reconcile node to evict pod
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Reconcile to delete machine
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)
	})
	It("should delete machines with terminal pods", func() {
		podEvictPhaseSucceeded := test.Pod(test.PodOptions{
			NodeName: node.Name,
			Phase:    v1.PodSucceeded,
		})
		podEvictPhaseFailed := test.Pod(test.PodOptions{
			NodeName: node.Name,
			Phase:    v1.PodFailed,
		})
		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, podEvictPhaseSucceeded, podEvictPhaseFailed)

		// Trigger Finalization Flow
		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)

		// Trigger Finalization Flow, which should ignore these pods and delete the node
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)
	})
	It("should fail to evict pods that violate a PDB", func() {
		minAvailable := intstr.FromInt(1)
		labelSelector := map[string]string{test.RandomName(): test.RandomName()}
		pdb := test.PodDisruptionBudget(test.PDBOptions{
			Labels: labelSelector,
			// Don't let any pod evict
			MinAvailable: &minAvailable,
		})
		podNoEvict := test.Pod(test.PodOptions{
			NodeName: node.Name,
			ObjectMeta: metav1.ObjectMeta{
				Labels:          labelSelector,
				OwnerReferences: defaultOwnerRefs,
			},
			Phase: v1.PodRunning,
		})

		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, podNoEvict, pdb)

		// Trigger Finalization Flow
		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Expect node to exist and be draining
		ExpectNodeDraining(monitor.env.Client, node.Name)

		// Expect podNoEvict to fail eviction due to PDB, and be retried
		Eventually(func() int {
			return monitor.evictionQueue.NumRequeues(client.ObjectKeyFromObject(podNoEvict))
		}).Should(BeNumerically(">=", 1))

		// Delete pod to simulate successful eviction
		ExpectDeleted(monitor.ctx, monitor.env.Client, podNoEvict)
		ExpectNotFound(monitor.ctx, monitor.env.Client, podNoEvict)

		// Reconcile to delete node
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)
	})
	It("should evict non-critical pods first", func() {
		podEvict := test.Pod(test.PodOptions{NodeName: node.Name, ObjectMeta: metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs}})
		podNodeCritical := test.Pod(test.PodOptions{NodeName: node.Name, PriorityClassName: "system-node-critical", ObjectMeta: metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs}})
		podClusterCritical := test.Pod(test.PodOptions{NodeName: node.Name, PriorityClassName: "system-cluster-critical", ObjectMeta: metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs}})

		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, podEvict, podNodeCritical, podClusterCritical)

		// Trigger Finalization Flow
		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Expect node to exist and be draining
		ExpectNodeDraining(monitor.env.Client, node.Name)

		// Expect podEvict to be evicting, and delete it
		ExpectEvicted(monitor.env.Client, podEvict)
		ExpectDeleted(monitor.ctx, monitor.env.Client, podEvict)

		// Expect the critical pods to be evicted and deleted
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectEvicted(monitor.env.Client, podNodeCritical)
		ExpectDeleted(monitor.ctx, monitor.env.Client, podNodeCritical)
		ExpectEvicted(monitor.env.Client, podClusterCritical)
		ExpectDeleted(monitor.ctx, monitor.env.Client, podClusterCritical)

		// Reconcile to delete node
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)
	})
	It("should not evict static pods", func() {
		podEvict := test.Pod(test.PodOptions{NodeName: node.Name, ObjectMeta: metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs}})
		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, podEvict)

		podNoEvict := test.Pod(test.PodOptions{
			NodeName: node.Name,
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "v1",
					Kind:       "Node",
					Name:       node.Name,
					UID:        node.UID,
				}},
			},
		})
		ExpectApplied(monitor.ctx, monitor.env.Client, podNoEvict)

		// Trigger Finalization Flow
		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Expect mirror pod to not be queued for eviction
		ExpectNotEnqueuedForEviction(monitor.evictionQueue, podNoEvict)

		// Expect podEvict to be enqueued for eviction then be successful
		ExpectEvicted(monitor.env.Client, podEvict)

		// Expect node to exist and be draining
		ExpectNodeDraining(monitor.env.Client, node.Name)

		// Reconcile node to evict pod
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))

		// Delete pod to simulate successful eviction
		ExpectDeleted(monitor.ctx, monitor.env.Client, podEvict)

		// Reconcile to delete node
		machine = ExpectExists(monitor.ctx, monitor.env.Client, machine)
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)

	})
	It("should wait for pods to terminate", func() {
		pod := test.Pod(test.PodOptions{NodeName: node.Name, ObjectMeta: metav1.ObjectMeta{OwnerReferences: defaultOwnerRefs}})
		monitor.fakeClock.SetTime(time.Now()) // make our fake clock match the pod creation time
		ExpectApplied(monitor.ctx, monitor.env.Client, machine, node, pod)

		// Before grace period, node should not delete
		Expect(monitor.env.Client.Delete(monitor.ctx, machine)).To(Succeed())
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNodeExists(monitor.ctx, monitor.env.Client, node.Name)
		ExpectEvicted(monitor.env.Client, pod)

		// After grace period, node should delete. The deletion timestamps are from etcd which we can't control, so
		// to eliminate test-flakiness we reset the time to current time + 90 seconds instead of just advancing
		// the clock by 90 seconds.
		monitor.fakeClock.SetTime(time.Now().Add(90 * time.Second))
		ExpectReconcileSucceeded(monitor.ctx, monitor.machineController, client.ObjectKeyFromObject(machine))
		ExpectNotFound(monitor.ctx, monitor.env.Client, machine)
	})
})

func ExpectNotEnqueuedForEviction(e *terminator.EvictionQueue, pods ...*v1.Pod) {
	for _, pod := range pods {
		ExpectWithOffset(1, e.Contains(client.ObjectKeyFromObject(pod))).To(BeFalse())
	}
}

func ExpectEvicted(c client.Client, pods ...*v1.Pod) {
	for _, pod := range pods {
		EventuallyWithOffset(1, func() bool {
			return ExpectPodExists(monitor.ctx, c, pod.Name, pod.Namespace).GetDeletionTimestamp().IsZero()
		}, ReconcilerPropagationTime, RequestInterval).Should(BeFalse(), func() string {
			return fmt.Sprintf("expected %s/%s to be evicting, but it isn't", pod.Namespace, pod.Name)
		})
	}
}

func ExpectNodeDraining(c client.Client, nodeName string) *v1.Node {
	node := ExpectNodeExistsWithOffset(1, monitor.ctx, c, nodeName)
	ExpectWithOffset(1, node.Spec.Unschedulable).To(BeTrue())
	ExpectWithOffset(1, lo.Contains(node.Finalizers, v1alpha5.TerminationFinalizer)).To(BeTrue())
	return node
}
