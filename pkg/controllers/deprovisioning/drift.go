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

package deprovisioning

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/samber/lo"

	"github.com/aws/karpenter-core/pkg/apis/settings"
	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
	deprovisioningevents "github.com/aws/karpenter-core/pkg/controllers/deprovisioning/events"
	"github.com/aws/karpenter-core/pkg/controllers/provisioning"
	"github.com/aws/karpenter-core/pkg/controllers/state"
	"github.com/aws/karpenter-core/pkg/events"
	"github.com/aws/karpenter-core/pkg/metrics"
)

// Drift is a subreconciler that deletes drifted candidates.
type Drift struct {
	kubeClient  client.Client
	cluster     *state.Cluster
	provisioner *provisioning.Provisioner
	recorder    events.Recorder
}

func NewDrift(kubeClient client.Client, cluster *state.Cluster, provisioner *provisioning.Provisioner, recorder events.Recorder) *Drift {
	return &Drift{
		kubeClient:  kubeClient,
		cluster:     cluster,
		provisioner: provisioner,
		recorder:    recorder,
	}
}

// ShouldDeprovision is a predicate used to filter deprovisionable candidates
func (d *Drift) ShouldDeprovision(ctx context.Context, c *Candidate) bool {
	return settings.FromContext(ctx).DriftEnabled &&
		c.NodeClaim.StatusConditions().GetCondition(v1beta1.Drifted).IsTrue()
}

// SortCandidates orders drifted candidates by when they've drifted
func (d *Drift) filterAndSortCandidates(ctx context.Context, candidates []*Candidate) ([]*Candidate, error) {
	candidates, err := filterCandidates(ctx, d.kubeClient, d.recorder, candidates)
	if err != nil {
		return nil, fmt.Errorf("filtering candidates, %w", err)
	}
	sort.Slice(candidates, func(i int, j int) bool {
		return candidates[i].NodeClaim.StatusConditions().GetCondition(v1beta1.Drifted).LastTransitionTime.Inner.Time.Before(
			candidates[j].NodeClaim.StatusConditions().GetCondition(v1beta1.Drifted).LastTransitionTime.Inner.Time)
	})
	return candidates, nil
}

// ComputeCommand generates a deprovisioning command given deprovisionable candidates
func (d *Drift) ComputeCommand(ctx context.Context, candidates ...*Candidate) (Command, error) {
	candidates, err := d.filterAndSortCandidates(ctx, candidates)
	if err != nil {
		return Command{}, err
	}
	deprovisioningEligibleMachinesGauge.WithLabelValues(d.String()).Set(float64(len(candidates)))

	// Deprovision all empty drifted candidates, as they require no scheduling simulations.
	if empty := lo.Filter(candidates, func(c *Candidate, _ int) bool {
		return len(c.pods) == 0
	}); len(empty) > 0 {
		return Command{
			candidates: empty,
		}, nil
	}

	for _, candidate := range candidates {
		// Check if we need to create any NodeClaims.
		results, err := simulateScheduling(ctx, d.kubeClient, d.cluster, d.provisioner, candidate)
		if err != nil {
			// if a candidate is now deleting, just retry
			if errors.Is(err, errCandidateDeleting) {
				continue
			}
			return Command{}, err
		}
		// Log when all pods can't schedule, as the command will get executed immediately.
		if !results.AllNonPendingPodsScheduled() {
			logging.FromContext(ctx).With(lo.Ternary(candidate.NodeClaim.IsMachine, "machine", "nodeclaim"), candidate.NodeClaim.Name, "node", candidate.Node.Name).Debugf("cannot terminate since scheduling simulation failed to schedule all pods %s", results.NonPendingPodSchedulingErrors())
			d.recorder.Publish(deprovisioningevents.Blocked(candidate.Node, candidate.NodeClaim, "Scheduling simulation failed to schedule all pods")...)
			continue
		}
		if len(results.NewNodeClaims) == 0 {
			return Command{
				candidates: []*Candidate{candidate},
			}, nil
		}
		return Command{
			candidates:   []*Candidate{candidate},
			replacements: results.NewNodeClaims,
		}, nil
	}
	return Command{}, nil
}

// String is the string representation of the deprovisioner
func (d *Drift) String() string {
	return metrics.DriftReason
}
