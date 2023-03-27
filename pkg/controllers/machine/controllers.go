package machine

import (
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/controllers/machine/garbagecollect"
	"github.com/aws/karpenter-core/pkg/controllers/machine/lifecycle"
	"github.com/aws/karpenter-core/pkg/controllers/machine/termination"
	"github.com/aws/karpenter-core/pkg/operator/controller"
)

func NewControllers(kubeClient client.Client, clock clock.Clock, cloudProvider cloudprovider.CloudProvider) []controller.Controller {
	return []controller.Controller{
		garbagecollect.NewController(kubeClient, cloudProvider, clock),
		lifecycle.NewController(clock, kubeClient, cloudProvider),
		termination.NewController(kubeClient, cloudProvider),
	}
}
