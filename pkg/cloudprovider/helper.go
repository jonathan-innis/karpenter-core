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

package cloudprovider

import (
	"context"
	"fmt"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"knative.dev/pkg/ptr"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/utils/resources"
)

const (
	EvictionSignalMemoryAvailable = "memory.available"
	EvictionSignalNodeFSAvailable = "nodefs.available"
)

type Helper struct {
	CloudProvider
}

func NewHelper(c CloudProvider) *Helper {
	return &Helper{
		CloudProvider: c,
	}
}

func (h *Helper) GetInstanceTypesWithOverhead(ctx context.Context) ([]*InstanceType, error) {
	instanceTypes, err := h.GetInstanceTypes(ctx)
	if err != nil {
		return nil, err
	}
	instanceTypes = populateOverhead(instanceTypes, nil)
	return instanceTypes, nil
}

func (h *Helper) GetInstanceTypesWithKubelet(ctx context.Context, kc *v1alpha5.KubeletConfiguration) ([]*InstanceType, error) {
	instanceTypes, err := h.GetInstanceTypes(ctx)
	if err != nil {
		return nil, err
	}
	instanceTypes = populateOverhead(instanceTypes, kc)
	return instanceTypes, err
}

func populateOverhead(instanceTypes []*InstanceType, kc *v1alpha5.KubeletConfiguration) []*InstanceType {
	for _, instanceType := range instanceTypes {
		instanceType.Overhead.SystemReserved = systemReservedResources(instanceType, kc)
		instanceType.Overhead.KubeReserved = kubeReservedResources(instanceType, kc)
		instanceType.Overhead.EvictionSoftThreshold = evictionSoftThreshold(instanceType, kc)
		instanceType.Overhead.EvictionHardThreshold = evictionHardThreshold(instanceType, kc)
		instanceType.Capacity[v1.ResourcePods] = pods(instanceType, kc)
	}
	return instanceTypes
}

func systemReservedResources(instanceType *InstanceType, kc *v1alpha5.KubeletConfiguration) v1.ResourceList {
	instanceType.Overhead.SystemReserved = SystemReserved()
	if kc == nil || kc.SystemReserved == nil {
		return instanceType.Overhead.SystemReserved
	}
	return lo.Assign(instanceType.Overhead.SystemReserved, kc.SystemReserved)
}

func kubeReservedResources(instanceType *InstanceType, kc *v1alpha5.KubeletConfiguration) v1.ResourceList {
	instanceType.Overhead.KubeReserved = KubeReserved(pods(instanceType, kc), instanceType.Capacity[v1.ResourceCPU])
	if kc == nil || kc.KubeReserved == nil {
		return instanceType.Overhead.KubeReserved
	}
	return lo.Assign(instanceType.Overhead.KubeReserved, kc.KubeReserved)
}

func evictionHardThreshold(instanceType *InstanceType, kc *v1alpha5.KubeletConfiguration) v1.ResourceList {
	instanceType.Overhead.EvictionHardThreshold = EvictionHardThreshold(instanceType.Capacity[v1.ResourceEphemeralStorage])
	if kc == nil || kc.EvictionHard == nil {
		return instanceType.Overhead.EvictionHardThreshold
	}
	override := v1.ResourceList{}
	if v, ok := kc.EvictionHard[EvictionSignalMemoryAvailable]; ok {
		override[v1.ResourceMemory] = ComputeThreshold(instanceType.Capacity[v1.ResourceMemory], v)
	}
	if v, ok := kc.EvictionHard[EvictionSignalNodeFSAvailable]; ok {
		override[v1.ResourceEphemeralStorage] = ComputeThreshold(instanceType.Capacity[v1.ResourceEphemeralStorage], v)
	}
	// Assign merges maps from left to right so overrides will always be taken last
	return lo.Assign(instanceType.Overhead.EvictionHardThreshold, override)
}

func evictionSoftThreshold(instanceType *InstanceType, kc *v1alpha5.KubeletConfiguration) v1.ResourceList {
	if kc == nil || kc.EvictionSoft == nil {
		return instanceType.Overhead.EvictionSoftThreshold
	}
	override := v1.ResourceList{}
	if v, ok := kc.EvictionSoft[EvictionSignalMemoryAvailable]; ok {
		override[v1.ResourceMemory] = ComputeThreshold(instanceType.Capacity[v1.ResourceMemory], v)
	}
	if v, ok := kc.EvictionSoft[EvictionSignalNodeFSAvailable]; ok {
		override[v1.ResourceEphemeralStorage] = ComputeThreshold(instanceType.Capacity[v1.ResourceEphemeralStorage], v)
	}
	// Assign merges maps from left to right so overrides will always be taken last
	return lo.Assign(instanceType.Overhead.EvictionSoftThreshold, override)
}

func pods(instanceType *InstanceType, kc *v1alpha5.KubeletConfiguration) resource.Quantity {
	if kc == nil {
		return instanceType.Capacity[v1.ResourcePods]
	}
	p := instanceType.Capacity[v1.ResourcePods]
	count := p.Value()
	if kc.MaxPods != nil {
		count = int64(ptr.Int32Value(kc.MaxPods))
	}
	if ptr.Int32Value(kc.PodsPerCore) > 0 {
		cpus := instanceType.Capacity[v1.ResourceCPU]
		count = lo.Min([]int64{int64(ptr.Int32Value(kc.PodsPerCore)) * cpus.Value(), count})
	}
	return lo.FromPtr(resources.Quantity(fmt.Sprint(count)))
}
