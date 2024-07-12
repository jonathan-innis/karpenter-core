/*
Copyright The Kubernetes Authors.

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

package disruption

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"sigs.k8s.io/karpenter/pkg/metrics"
)

func init() {
	crmetrics.Registry.MustRegister(
		ActionEvaluationDuration,
		TotalActions,
		EligibleNodes,
		TotalConsolidationTimeouts,
		AllowedDisruptions,
	)
}

const (
	disruptionSubsystem    = "disruption"
	actionLabel            = "action"
	reasonLabel            = "reason"
	consolidationTypeLabel = "consolidation_type"
)

var (
	ActionEvaluationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metrics.Namespace,
			Subsystem: disruptionSubsystem,
			Name:      "action_evaluation_duration_seconds",
			Help:      "Duration of the disruption action evaluation process in seconds. Labeled by disruption reason.",
			Buckets:   metrics.DurationBuckets(),
		},
		[]string{reasonLabel},
	)
	TotalActions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: disruptionSubsystem,
			Name:      "actions_total",
			Help:      "Total number of disruption decisions made. Labeled by disruption action and reason.",
		},
		[]string{actionLabel, reasonLabel},
	)
	EligibleNodes = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Subsystem: disruptionSubsystem,
			Name:      "eligible_nodes",
			Help:      "Number of nodes eligible for disruption by Karpenter. Labeled by disruption reason.",
		},
		[]string{reasonLabel},
	)
	TotalConsolidationTimeouts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: disruptionSubsystem,
			Name:      "consolidation_timeouts_total",
			Help:      "Number of times the Consolidation algorithm has reached a timeout. Labeled by consolidation type.",
		},
		[]string{consolidationTypeLabel},
	)
	TotalValidationFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metrics.Namespace,
			Subsystem: disruptionSubsystem,
			Name:      "validation_failures_total",
			Help:      "Number of times Karpenter failed validating its disruption action after proposing it. Labeled by disruption reason. Validation happens after an action is proposed but before it is executed.",
		},
		[]string{reasonLabel},
	)
	AllowedDisruptions = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metrics.Namespace,
			Subsystem: "nodepool",
			Name:      "allowed_disruptions",
			Help:      "The number of nodes for a given NodePool that can be disrupted at a point in time. Labeled by NodePool. Note that allowed disruptions can change very rapidly, as new nodes may be created and others may be deleted at any point.",
		},
		[]string{metrics.NodePoolLabel, metrics.ReasonLabel},
	)
)
