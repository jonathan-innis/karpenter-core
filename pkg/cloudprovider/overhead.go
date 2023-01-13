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
	"fmt"
	"math"
	"strings"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/aws/karpenter-core/pkg/utils/functional"
)

func ComputeThreshold(base resource.Quantity, v string) resource.Quantity {
	if strings.HasSuffix(v, "%") {
		p := lo.Must(functional.ParsePercentage(v))
		if p == 100 {
			p = 0
		}
		// Calculation is node.capacity * evictionHard[memory.available] if percentage
		// From https://kubernetes.io/docs/concepts/scheduling-eviction/node-pressure-eviction/#eviction-signals
		return resource.MustParse(fmt.Sprint(math.Ceil(base.AsApproximateFloat64() / 100 * p)))
	}
	return resource.MustParse(v)
}

func SystemReserved() v1.ResourceList {
	// default system-reserved resources: https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/#system-reserved
	return v1.ResourceList{
		v1.ResourceCPU:              resource.MustParse("100m"),
		v1.ResourceMemory:           resource.MustParse("100Mi"),
		v1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
	}
}

func KubeReserved(pods, cpus resource.Quantity) v1.ResourceList {
	resources := v1.ResourceList{
		v1.ResourceMemory:           resource.MustParse(fmt.Sprintf("%dMi", (11*pods.Value())+255)),
		v1.ResourceEphemeralStorage: resource.MustParse("1Gi"), // default kube-reserved ephemeral-storage
	}
	// kube-reserved Computed from
	// https://github.com/bottlerocket-os/bottlerocket/pull/1388/files#diff-bba9e4e3e46203be2b12f22e0d654ebd270f0b478dd34f40c31d7aa695620f2fR611
	for _, cpuRange := range []struct {
		start      int64
		end        int64
		percentage float64
	}{
		{start: 0, end: 1000, percentage: 0.06},
		{start: 1000, end: 2000, percentage: 0.01},
		{start: 2000, end: 4000, percentage: 0.005},
		{start: 4000, end: 1 << 31, percentage: 0.0025},
	} {
		if cpu := cpus.MilliValue(); cpu >= cpuRange.start {
			r := float64(cpuRange.end - cpuRange.start)
			if cpu < cpuRange.end {
				r = float64(cpu - cpuRange.start)
			}
			cpuOverhead := resources.Cpu()
			cpuOverhead.Add(*resource.NewMilliQuantity(int64(r*cpuRange.percentage), resource.DecimalSI))
			resources[v1.ResourceCPU] = *cpuOverhead
		}
	}
	return resources
}

func EvictionHardThreshold(storage resource.Quantity) v1.ResourceList {
	return v1.ResourceList{
		v1.ResourceMemory:           resource.MustParse("100Mi"),
		v1.ResourceEphemeralStorage: ComputeThreshold(storage, "10%"), // default evictionHard node.fs is 10%
	}
}
