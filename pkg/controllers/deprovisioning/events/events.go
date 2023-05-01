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

package events

import (
	"context"
	"fmt"
	"time"

	"github.com/patrickmn/go-cache"
	v1 "k8s.io/api/core/v1"
	"knative.dev/pkg/logging"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/events"
	"github.com/aws/karpenter-core/pkg/utils/pretty"
)

func Launching(machine *v1alpha5.Machine, reason string) events.Event {
	return events.Event{
		InvolvedObject: machine,
		Type:           v1.EventTypeNormal,
		Reason:         "DeprovisioningLaunching",
		Message:        fmt.Sprintf("Launching machine for %s", reason),
		DedupeValues:   []string{machine.Name, reason},
	}
}

func WaitingOnReadiness(machine *v1alpha5.Machine) events.Event {
	return events.Event{
		InvolvedObject: machine,
		Type:           v1.EventTypeNormal,
		Reason:         "DeprovisioningWaitingReadiness",
		Message:        "Waiting on readiness to continue deprovisioning",
		DedupeValues:   []string{machine.Name},
	}
}

func WaitingOnDeletion(machine *v1alpha5.Machine) events.Event {
	return events.Event{
		InvolvedObject: machine,
		Type:           v1.EventTypeNormal,
		Reason:         "DeprovisioningWaitingDeletion",
		Message:        "Waiting on deletion to continue deprovisioning",
		DedupeValues:   []string{machine.Name},
	}
}

func Terminating(node *v1.Node, machine *v1alpha5.Machine, reason string) []events.Event {
	return []events.Event{
		{
			InvolvedObject: node,
			Type:           v1.EventTypeNormal,
			Reason:         "DeprovisioningTerminating",
			Message:        fmt.Sprintf("Deprovisioning node via %s", reason),
			DedupeValues:   []string{node.Name, reason},
		},
		{
			InvolvedObject: machine,
			Type:           v1.EventTypeNormal,
			Reason:         "DeprovisioningTerminating",
			Message:        fmt.Sprintf("Deprovisioning machine via %s", reason),
			DedupeValues:   []string{machine.Name, reason},
		},
	}
}

func Unconsolidatable(node *v1.Node, machine *v1alpha5.Machine, reason string) []events.Event {
	return []events.Event{
		{
			InvolvedObject: node,
			Type:           v1.EventTypeNormal,
			Reason:         "Unconsolidatable",
			Message:        reason,
			DedupeValues:   []string{node.Name},
			DedupeTimeout:  time.Minute * 15,
		},
		{
			InvolvedObject: machine,
			Type:           v1.EventTypeNormal,
			Reason:         "Unconsolidatable",
			Message:        reason,
			DedupeValues:   []string{machine.Name},
			DedupeTimeout:  time.Minute * 15,
		},
	}
}

var blockedCache = cache.New(time.Minute*30, time.Minute*5)
var blockedChangeMonitor = pretty.NewChangeMonitor(pretty.WithVisibilityTimeout(time.Minute * 15))

func Blocked(ctx context.Context, node *v1.Node, machine *v1alpha5.Machine, reason string) (evts []events.Event) {
	var nodeName, machineName string
	if node != nil {
		nodeName = node.Name
		evts = append(evts, events.Event{
			InvolvedObject: node,
			Type:           v1.EventTypeNormal,
			Reason:         "DeprovisioningBlocked",
			Message:        fmt.Sprintf("Cannot deprovision node due to %s", reason),
			DedupeValues:   []string{node.Name, reason},
		})
	}
	if machine != nil {
		machineName = machine.Name
		evts = append(evts, events.Event{
			InvolvedObject: machine,
			Type:           v1.EventTypeNormal,
			Reason:         "DeprovisioningBlocked",
			Message:        fmt.Sprintf("Cannot deprovision machine due to %s", reason),
			DedupeValues:   []string{machine.Name, reason},
		})
	}

	// Log that a machine/node is blocked if it's been over 15m since we first received the machine/node for evaluation
	key := fmt.Sprintf("%s/%s", nodeName, machineName)
	if v, ok := blockedCache.Get(key); ok {
		blockedCache.SetDefault(key, v)
		if time.Since(v.(time.Time)) > time.Minute*15 {
			if blockedChangeMonitor.HasChanged(key, reason) {
				logging.FromContext(ctx).With("node", nodeName, "machine", machineName).Debugf("deprovisioning blocked for >15m due to %s", reason)
			}
		}
	} else {
		blockedCache.SetDefault(key, time.Now())
	}
	return evts
}
