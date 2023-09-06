package scheduling_test

import (
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"

	"github.com/aws/karpenter-core/pkg/scheduling"
)

func BenchmarkRequirementsOld(b *testing.B) {
	reqs := scheduling.NewRequirements()
	reqs2 := scheduling.NewRequirements()
	for i := 0; i < 100; i++ {
		reqs.Add(scheduling.NewRequirement(fmt.Sprintf("key%d", i), v1.NodeSelectorOpIn, "value"))
		reqs2.Add(scheduling.NewRequirement(fmt.Sprintf("key%d", i), v1.NodeSelectorOpIn, "value"))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqs.CompatibleOld(reqs2)
	}
}

func BenchmarkRequirementsNew(b *testing.B) {
	reqs := scheduling.NewRequirements()
	reqs2 := scheduling.NewRequirements()
	for i := 0; i < 100; i++ {
		reqs.Add(scheduling.NewRequirement(fmt.Sprintf("key%d", i), v1.NodeSelectorOpIn, "value"))
		reqs2.Add(scheduling.NewRequirement(fmt.Sprintf("key%d", i), v1.NodeSelectorOpIn, "value"))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reqs.Compatible(reqs2, scheduling.AllowUndefinedWellKnownLabelsV1Alpha5)
	}
}
