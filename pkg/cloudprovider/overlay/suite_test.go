package overlay_test

import (
	"context"
	"math"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	. "knative.dev/pkg/logging/testing"
	"knative.dev/pkg/ptr"

	"github.com/aws/karpenter-core/pkg/apis/config/settings"
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/cloudprovider/fake"
	"github.com/aws/karpenter-core/pkg/cloudprovider/overlay"
	"github.com/aws/karpenter-core/pkg/test"
	"github.com/aws/karpenter-core/pkg/utils/resources"
)

var ctx context.Context
var cloudProvider cloudprovider.CloudProvider

func TestAPIs(t *testing.T) {
	ctx = TestContextWithLogger(t)
	RegisterFailHandler(Fail)
	RunSpecs(t, "Node")
}

var _ = BeforeSuite(func() {
	ctx = settings.ToContext(ctx, test.Settings())
	cloudProvider = fake.NewCloudProvider()
})

var _ = Describe("Provisioner KubeletConfiguration Overrides", func() {
	Context("Reserved Resources", func() {
		It("should override system reserved cpus when specified", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceCPU: resource.MustParse("2"),
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.SystemReserved.Cpu().String()).To(Equal("2"))
			}
		})
		It("should override system reserved memory when specified", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.SystemReserved.Memory().String()).To(Equal("20Gi"))
			}
		})
		It("should override kube reserved when specified", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceCPU:              resource.MustParse("1"),
						v1.ResourceMemory:           resource.MustParse("20Gi"),
						v1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceCPU:              resource.MustParse("2"),
						v1.ResourceMemory:           resource.MustParse("10Gi"),
						v1.ResourceEphemeralStorage: resource.MustParse("2Gi"),
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.KubeReserved.Cpu().String()).To(Equal("2"))
				Expect(instanceType.Overhead.KubeReserved.Memory().String()).To(Equal("10Gi"))
				Expect(instanceType.Overhead.KubeReserved.StorageEphemeral().String()).To(Equal("2Gi"))
			}
		})
	})
	Context("Eviction Thresholds", func() {
		It("should override eviction threshold (hard) when specified as a quantity", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("10Gi"),
					},
					EvictionHard: map[string]string{
						overlay.SignalMemoryAvailable: "500Mi",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.EvictionHardThreshold.Memory().String()).To(Equal("500Mi"))
			}
		})
		It("should override eviction threshold (hard) when specified as a percentage value", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("10Gi"),
					},
					EvictionHard: map[string]string{
						overlay.SignalMemoryAvailable: "10%",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.EvictionHardThreshold.Memory().Value()).To(BeNumerically("~", float64(instanceType.Capacity.Memory().Value())*0.1, 10))
			}
		})
		It("should consider the eviction threshold (hard) disabled when specified as 100%", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("10Gi"),
					},
					EvictionHard: map[string]string{
						overlay.SignalMemoryAvailable: "100%",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.EvictionHardThreshold.Memory().String()).To(Equal("0"))
			}
		})
		It("should maintain default eviction threshold (hard) for memory when evictionHard not specified", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("10Gi"),
					},
					EvictionSoft: map[string]string{
						overlay.SignalMemoryAvailable: "50Mi",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.EvictionHardThreshold.Memory().String()).To(Equal("100Mi"))
			}
		})
		It("should override eviction threshold (soft) when specified as a quantity", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("10Gi"),
					},
					EvictionSoft: map[string]string{
						overlay.SignalMemoryAvailable: "500Mi",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.EvictionSoftThreshold.Memory().String()).To(Equal("500Mi"))
			}
		})
		It("should override eviction threshold (soft) when specified as a percentage value", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("10Gi"),
					},
					EvictionHard: map[string]string{
						overlay.SignalMemoryAvailable: "5%",
					},
					EvictionSoft: map[string]string{
						overlay.SignalMemoryAvailable: "10%",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.EvictionSoftThreshold.Memory().Value()).To(BeNumerically("~", float64(instanceType.Capacity.Memory().Value())*0.1, 10))
			}
		})
		It("should consider the eviction threshold (soft) disabled when specified as 100%", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("10Gi"),
					},
					EvictionSoft: map[string]string{
						overlay.SignalMemoryAvailable: "100%",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				Expect(instanceType.Overhead.EvictionSoftThreshold.Memory().String()).To(Equal("0"))
			}
		})
		It("should take the greater of evictionHard and evictionSoft for overhead as a value", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("20Gi"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("10Gi"),
					},
					EvictionSoft: map[string]string{
						overlay.SignalMemoryAvailable: "3Gi",
					},
					EvictionHard: map[string]string{
						overlay.SignalMemoryAvailable: "1Gi",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				overhead := instanceType.Overhead.Total()
				Expect(overhead.Memory().String()).To(Equal("33Gi"))
			}
		})
		It("should take the greater of evictionHard and evictionSoft for overhead as a value", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("0"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("0"),
					},
					EvictionSoft: map[string]string{
						overlay.SignalMemoryAvailable: "2%",
					},
					EvictionHard: map[string]string{
						overlay.SignalMemoryAvailable: "5%",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				overhead := instanceType.Overhead.Total()
				Expect(overhead.Memory().Value()).To(BeNumerically("~", float64(instanceType.Capacity.Memory().Value())*0.05, 10))
			}
		})
		It("should take the greater of evictionHard and evictionSoft for overhead with mixed percentage/value", func() {
			instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
			Expect(err).To(BeNil())
			provisioner := test.Provisioner(test.ProvisionerOptions{
				Kubelet: &v1alpha5.KubeletConfiguration{
					SystemReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("0"),
					},
					KubeReserved: v1.ResourceList{
						v1.ResourceMemory: resource.MustParse("0"),
					},
					EvictionSoft: map[string]string{
						overlay.SignalMemoryAvailable: "10%",
					},
					EvictionHard: map[string]string{
						overlay.SignalMemoryAvailable: "1Gi",
					},
				},
			})
			instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
			for _, instanceType := range instanceTypes {
				overhead := instanceType.Overhead.Total()
				Expect(overhead.Memory().Value()).To(BeNumerically("~", math.Max(float64(instanceType.Capacity.Memory().Value())*0.1, float64(resources.Quantity("1Gi").Value())), 10))
			}
		})
	})
	It("should set max-pods to user-defined value if specified", func() {
		instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
		Expect(err).To(BeNil())
		provisioner := test.Provisioner(test.ProvisionerOptions{Kubelet: &v1alpha5.KubeletConfiguration{MaxPods: ptr.Int32(10)}})
		instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
		for _, instanceType := range instanceTypes {
			Expect(instanceType.Capacity.Pods().Value()).To(BeNumerically("==", 10))
		}
	})
	It("should override pods-per-core value", func() {
		oldInstanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
		Expect(err).To(BeNil())
		provisioner := test.Provisioner(test.ProvisionerOptions{Kubelet: &v1alpha5.KubeletConfiguration{PodsPerCore: ptr.Int32(1)}})
		instanceTypes := overlay.WithProvisionerOverrides(oldInstanceTypes, provisioner)
		for _, instanceType := range instanceTypes {
			oldInstanceType, found := lo.Find(oldInstanceTypes, func(i *cloudprovider.InstanceType) bool {
				return i.Name == instanceType.Name
			})
			Expect(found).To(BeTrue())
			Expect(instanceType.Capacity.Pods().Value()).To(BeNumerically("==", math.Min(float64(instanceType.Capacity.Cpu().Value()), float64(oldInstanceType.Capacity.Pods().Value()))))
		}
	})
	It("should take the minimum of pods-per-core and max-pods", func() {
		instanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
		Expect(err).To(BeNil())
		provisioner := test.Provisioner(test.ProvisionerOptions{Kubelet: &v1alpha5.KubeletConfiguration{PodsPerCore: ptr.Int32(4), MaxPods: ptr.Int32(20)}})
		instanceTypes = overlay.WithProvisionerOverrides(instanceTypes, provisioner)
		for _, instanceType := range instanceTypes {
			Expect(instanceType.Capacity.Pods().Value()).To(BeNumerically("==", math.Min(float64(instanceType.Capacity.Cpu().Value()*4), 20)))
		}
	})
	It("should take the default pods number when pods-per-core is 0", func() {
		oldInstanceTypes, err := cloudProvider.GetInstanceTypes(ctx)
		Expect(err).To(BeNil())
		provisioner := test.Provisioner(test.ProvisionerOptions{Kubelet: &v1alpha5.KubeletConfiguration{PodsPerCore: ptr.Int32(1)}})
		instanceTypes := overlay.WithProvisionerOverrides(oldInstanceTypes, provisioner)
		for _, instanceType := range instanceTypes {
			oldInstanceType, found := lo.Find(oldInstanceTypes, func(i *cloudprovider.InstanceType) bool {
				return i.Name == instanceType.Name
			})
			Expect(found).To(BeTrue())
			Expect(instanceType.Capacity.Pods().Value()).To(BeNumerically("==", oldInstanceType.Capacity.Pods().Value()))
		}
	})
})
