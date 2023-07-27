package provisioner

import (
	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
)

func New(nodePool *v1beta1.NodePool) *v1alpha5.Provisioner {
	return &v1alpha5.Provisioner{}
}
