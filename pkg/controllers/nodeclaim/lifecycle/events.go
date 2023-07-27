package lifecycle

import (
	"fmt"

	v1 "k8s.io/api/core/v1"

	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
	"github.com/aws/karpenter-core/pkg/events"
	machineutil "github.com/aws/karpenter-core/pkg/utils/machine"
)

func InsufficientCapacityErrorEvent(nodeClaim *v1beta1.NodeClaim, err error) events.Event {
	if nodeClaim.IsMachine {
		machine := machineutil.NewFromNodeClaim(nodeClaim)
		return events.Event{
			InvolvedObject: machine,
			Type:           v1.EventTypeWarning,
			Reason:         "InsufficientCapacityError",
			Message:        fmt.Sprintf("Machine %s event: %s", machine.Name, truncateMessage(err.Error())),
			DedupeValues:   []string{string(machine.UID)},
		}
	} else {
		return events.Event{
			InvolvedObject: nodeClaim,
			Type:           v1.EventTypeWarning,
			Reason:         "InsufficientCapacityError",
			Message:        fmt.Sprintf("NodeClaim %s event: %s", nodeClaim.Name, truncateMessage(err.Error())),
			DedupeValues:   []string{string(nodeClaim.UID)},
		}
	}
}
