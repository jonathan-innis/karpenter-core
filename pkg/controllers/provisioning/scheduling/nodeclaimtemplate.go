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

package scheduling

import (
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
	"github.com/aws/karpenter-core/pkg/cloudprovider"
	"github.com/aws/karpenter-core/pkg/operator/scheme"
	"github.com/aws/karpenter-core/pkg/scheduling"
	nodepoolutil "github.com/aws/karpenter-core/pkg/utils/nodepool"
)

// NodeClaimTemplate encapsulates the fields required to create a node and mirrors
// the fields in Provisioner. These structs are maintained separately in order
// for fields like Requirements to be able to be stored more efficiently.
type NodeClaimTemplate struct {
	v1beta1.NodeClaimTemplate

	NodePoolName        string
	InstanceTypeOptions cloudprovider.InstanceTypes
	Requirements        scheduling.Requirements
}

func NewNodeClaimTemplate(nodePool *v1beta1.NodePool) *NodeClaimTemplate {
	mt := &NodeClaimTemplate{
		NodeClaimTemplate: nodePool.Spec.Template,
		NodePoolName:      nodePool.Name,
		Requirements:      scheduling.NewRequirements(),
	}
	if nodepoolutil.IsProvisioner(nodePool.Name) {
		mt.Labels = lo.Assign(mt.Labels, map[string]string{v1alpha5.ProvisionerNameLabelKey: nodepoolutil.Name(nodePool.Name)})
	} else {
		mt.Labels = lo.Assign(mt.Labels, map[string]string{v1beta1.NodePoolLabelKey: nodepoolutil.Name(nodePool.Name)})
	}
	mt.Requirements.Add(scheduling.NewNodeSelectorRequirements(nodePool.Spec.Template.Spec.Requirements...).Values()...)
	mt.Requirements.Add(scheduling.NewLabelRequirements(nodePool.Spec.Template.Labels).Values()...)
	return mt
}

// TODO @joinis: Be able to create either a v1alpha5.Machine or a v1beta1.NodeClaim based on whether we are using a Provisioner or a NodePool
func (i *NodeClaimTemplate) ToNodeClaim(owner *v1beta1.NodePool) *v1beta1.NodeClaim {
	// Order the instance types by price and only take the first 100 of them to decrease the instance type size in the requirements
	instanceTypes := lo.Slice(i.InstanceTypeOptions.OrderByPrice(i.Requirements), 0, 100)
	i.Requirements.Add(scheduling.NewRequirement(v1.LabelInstanceTypeStable, v1.NodeSelectorOpIn, lo.Map(instanceTypes, func(i *cloudprovider.InstanceType, _ int) string {
		return i.Name
	})...))
	m := &v1beta1.NodeClaim{
		ObjectMeta: i.ObjectMeta,
		Spec:       i.Spec,
	}
	map[string]string{v1alpha5.ProvisionerHashAnnotationKey: provisionerDriftHash},
		m.Spec.Requirements = i.Requirements.NodeSelectorRequirements()
	lo.Must0(controllerutil.SetOwnerReference(owner, m, scheme.Scheme))
	return m
}
