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

package lifecycle

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/utils/clock"
	"knative.dev/pkg/logging"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/metrics"
)

type LaunchTimeout struct {
	clock      clock.Clock
	kubeClient client.Client
}

// launchTTL is a heuristic time that we expect to succeed with our cloudprovider.Create() call
// If we don't succeed within this time, then we should delete and try again through some other mechanism
const launchTTL = time.Minute * 2

func (l *LaunchTimeout) Reconcile(ctx context.Context, machine *v1alpha5.Machine) (reconcile.Result, error) {
	if machine.StatusConditions().GetCondition(v1alpha5.MachineLaunched).IsTrue() {
		return reconcile.Result{}, nil
	}
	if machine.StatusConditions().GetCondition(v1alpha5.MachineLaunched) == nil {
		return reconcile.Result{Requeue: true}, nil
	}
	if l.clock.Since(machine.StatusConditions().GetCondition(v1alpha5.MachineLaunched).LastTransitionTime.Inner.Time) < launchTTL {
		return reconcile.Result{RequeueAfter: launchTTL - l.clock.Since(machine.StatusConditions().GetCondition(v1alpha5.MachineLaunched).LastTransitionTime.Inner.Time)}, nil
	}
	// Delete the machine if we believe the machine won't create
	removedFinalizer := removeFinalizerBestEffort(ctx, l.kubeClient, machine)
	if err := l.kubeClient.Delete(ctx, machine); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	logging.FromContext(ctx).With("ttl", launchTTL).Debugf("deleting machine since node hasn't created within creation ttl")
	if removedFinalizer {
		logging.FromContext(ctx).Infof("deleted machine")
	}
	metrics.MachinesTerminatedCounter.With(prometheus.Labels{
		metrics.ReasonLabel:      "launch_timeout",
		metrics.ProvisionerLabel: machine.Labels[v1alpha5.ProvisionerNameLabelKey],
	}).Inc()
	return reconcile.Result{}, nil
}
