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

package overlay

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"knative.dev/pkg/ptr"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/utils/resources"
)

const (
	SignalMemoryAvailable = "memory.available"
)

func WithProvisionerOverrides(instanceTypes []*cloudprovider.InstanceType, provisioner *v1alpha5.Provisioner) []*cloudprovider.InstanceType {
	for _, instanceType := range instanceTypes {
		instanceType.Overhead.SystemReserved = systemReservedResources(instanceType, provisioner)
		instanceType.Overhead.KubeReserved = kubeReservedResources(instanceType, provisioner)
		instanceType.Overhead.EvictionSoftThreshold = evictionSoftThreshold(instanceType, provisioner)
		instanceType.Overhead.EvictionHardThreshold = evictionHardThreshold(instanceType, provisioner)
		instanceType.Capacity[v1.ResourcePods] = pods(instanceType, provisioner)
	}
	return instanceTypes
}

func systemReservedResources(instanceType *cloudprovider.InstanceType, provisioner *v1alpha5.Provisioner) v1.ResourceList {
	if provisioner.Spec.KubeletConfiguration == nil || provisioner.Spec.KubeletConfiguration.SystemReserved == nil {
		return instanceType.Overhead.SystemReserved
	}
	return lo.Assign(instanceType.Overhead.SystemReserved, provisioner.Spec.KubeletConfiguration.SystemReserved)
}

func kubeReservedResources(instanceType *cloudprovider.InstanceType, provisioner *v1alpha5.Provisioner) v1.ResourceList {
	if provisioner.Spec.KubeletConfiguration == nil || provisioner.Spec.KubeletConfiguration.KubeReserved == nil {
		return instanceType.Overhead.KubeReserved
	}
	return lo.Assign(instanceType.Overhead.KubeReserved, provisioner.Spec.KubeletConfiguration.KubeReserved)
}

func evictionHardThreshold(instanceType *cloudprovider.InstanceType, provisioner *v1alpha5.Provisioner) v1.ResourceList {
	if provisioner.Spec.KubeletConfiguration == nil || provisioner.Spec.KubeletConfiguration.EvictionHard == nil {
		return instanceType.Overhead.EvictionHardThreshold
	}
	override := v1.ResourceList{}
	if v, ok := provisioner.Spec.KubeletConfiguration.EvictionHard[SignalMemoryAvailable]; ok {
		if strings.HasSuffix(v, "%") {
			p := mustParsePercentage(v)

			// Calculation is node.capacity * evictionHard[memory.available] if percentage
			// From https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/#eviction-signals
			memory := instanceType.Capacity[v1.ResourceMemory]
			override[v1.ResourceMemory] = resource.MustParse(fmt.Sprint(math.Ceil(float64(memory.Value()) / 100 * p)))
		} else {
			override[v1.ResourceMemory] = resource.MustParse(v)
		}
	}
	// Assign merges maps from left to right so overrides will always be taken last
	return lo.Assign(instanceType.Overhead.EvictionHardThreshold, override)
}

func evictionSoftThreshold(instanceType *cloudprovider.InstanceType, provisioner *v1alpha5.Provisioner) v1.ResourceList {
	if provisioner.Spec.KubeletConfiguration == nil || provisioner.Spec.KubeletConfiguration.EvictionSoft == nil {
		return instanceType.Overhead.EvictionSoftThreshold
	}
	override := v1.ResourceList{}
	if v, ok := provisioner.Spec.KubeletConfiguration.EvictionSoft[SignalMemoryAvailable]; ok {
		if strings.HasSuffix(v, "%") {
			p := mustParsePercentage(v)

			// Calculation is node.capacity * evictionHard[memory.available] if percentage
			// From https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/#eviction-signals
			memory := instanceType.Capacity[v1.ResourceMemory]
			override[v1.ResourceMemory] = resource.MustParse(fmt.Sprint(math.Ceil(float64(memory.Value()) / 100 * p)))
		} else {
			override[v1.ResourceMemory] = resource.MustParse(v)
		}
	}
	// Assign merges maps from left to right so overrides will always be taken last
	return lo.Assign(instanceType.Overhead.EvictionSoftThreshold, override)
}

func pods(instanceType *cloudprovider.InstanceType, provisioner *v1alpha5.Provisioner) resource.Quantity {
	if provisioner.Spec.KubeletConfiguration == nil {
		return instanceType.Capacity[v1.ResourcePods]
	}
	p := instanceType.Capacity[v1.ResourcePods]
	count := p.Value()
	if provisioner.Spec.KubeletConfiguration.MaxPods != nil {
		count = int64(ptr.Int32Value(provisioner.Spec.KubeletConfiguration.MaxPods))
	}
	if ptr.Int32Value(provisioner.Spec.KubeletConfiguration.PodsPerCore) > 0 {
		cpus := instanceType.Capacity[v1.ResourceCPU]
		count = lo.Min([]int64{int64(ptr.Int32Value(provisioner.Spec.KubeletConfiguration.PodsPerCore)) * cpus.Value(), count})
	}
	return lo.FromPtr(resources.Quantity(fmt.Sprint(count)))
}

func mustParsePercentage(v string) float64 {
	p, err := strconv.ParseFloat(strings.Trim(v, "%"), 64)
	if err != nil {
		panic(fmt.Sprintf("expected percentage value to be a float but got %s, %v", v, err))
	}
	// Setting percentage value to 100% is considered disabling the threshold according to
	// https://github.com/kubernetes/kubernetes/blob/3e26e104bdf9d0dc3c4046d6350b93557c67f3f4/pkg/kubelet/eviction/helpers.go#L192
	// https://kubernetes.io/docs/reference/config-api/kubelet-config.v1beta1/
	if p == 100 {
		p = 0
	}
	return p
}
