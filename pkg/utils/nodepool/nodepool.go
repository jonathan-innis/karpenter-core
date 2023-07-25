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

package nodepool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
	"github.com/aws/karpenter-core/pkg/apis/v1beta1"
)

func New(provisioner *v1alpha5.Provisioner) *v1beta1.NodePool {
	np := &v1beta1.NodePool{
		ObjectMeta: provisioner.ObjectMeta,
		Spec: v1beta1.NodePoolSpec{
			Template: v1beta1.NodeClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: lo.Assign(provisioner.Annotations, v1beta1.ProviderAnnotation(provisioner.Spec.Provider), v1beta1.KubeletAnnotation(provisioner.Spec.KubeletConfiguration)),
					Labels:      provisioner.Labels,
				},
				Spec: v1beta1.NodeClaimSpec{
					Taints:        provisioner.Spec.Taints,
					StartupTaints: provisioner.Spec.StartupTaints,
					Requirements:  provisioner.Spec.Requirements,
					NodeClass:     NewNodeClassRef(provisioner.Spec.ProviderRef),
				},
			},
			Weight: provisioner.Spec.Weight,
		},
	}
	np.Name = fmt.Sprintf("provisioner/%s", np.Name) // Use this to uniquely identify a Provisioner from a MachineGroup
	if provisioner.Spec.TTLSecondsAfterEmpty != nil {
		np.Spec.ConsolidationTTL = &metav1.Duration{Duration: lo.Must(time.ParseDuration(fmt.Sprintf("%ds", lo.FromPtr[int64](provisioner.Spec.TTLSecondsAfterEmpty))))}
	}
	if provisioner.Spec.TTLSecondsUntilExpired != nil {
		np.Spec.ExpirationTTL = &metav1.Duration{Duration: lo.Must(time.ParseDuration(fmt.Sprintf("%ds", lo.FromPtr[int64](provisioner.Spec.TTLSecondsAfterEmpty))))}
	}
	if provisioner.Spec.Consolidation != nil {
		np.Spec.Consolidation = &v1beta1.Consolidation{
			Enabled: provisioner.Spec.Consolidation.Enabled,
		}
	}
	if provisioner.Spec.Limits != nil {
		np.Spec.Limits = v1beta1.Limits(provisioner.Spec.Limits.Resources)
	}
	return np
}

func NewNodeClassRef(pr *v1alpha5.MachineTemplateRef) *v1beta1.NodeClassRef {
	if pr == nil {
		return nil
	}
	return &v1beta1.NodeClassRef{
		Kind:       pr.Kind,
		Name:       pr.Name,
		APIVersion: pr.APIVersion,
	}
}

func IsProvisioner(name string) bool {
	return strings.HasPrefix(name, "provisioner/")
}

func Name(name string) string {
	return strings.TrimPrefix(name, "provisioner/")
}

func Get(ctx context.Context, c client.Client, name string) (*v1beta1.NodePool, error) {
	if IsProvisioner(name) {
		provisioner := &v1alpha5.Provisioner{}
		if err := c.Get(ctx, types.NamespacedName{Name: Name(name)}, provisioner); err != nil {
			return nil, err
		}
		return New(provisioner), nil
	}
	nodePool := &v1beta1.NodePool{}
	if err := c.Get(ctx, types.NamespacedName{Name: name}, nodePool); err != nil {
		return nil, err
	}
	return nodePool, nil
}

func Owner(ctx context.Context, c client.Client, obj interface{ GetLabels() map[string]string }) (*v1beta1.NodePool, error) {
	if v, ok := obj.GetLabels()[v1beta1.NodePoolLabelKey]; ok {
		nodePool := &v1beta1.NodePool{}
		if err := c.Get(ctx, types.NamespacedName{Name: v}, nodePool); err != nil {
			return nil, err
		}
		return nodePool, nil
	}
	if v, ok := obj.GetLabels()[v1alpha5.ProvisionerNameLabelKey]; ok {
		provisioner := &v1alpha5.Provisioner{}
		if err := c.Get(ctx, types.NamespacedName{Name: v}, provisioner); err != nil {
			return nil, err
		}
		return New(provisioner), nil
	}
	return nil, fmt.Errorf("object has no owner")
}

func OwnerName(obj interface{ GetLabels() map[string]string }) (string, error) {
	if v, ok := obj.GetLabels()[v1beta1.NodePoolLabelKey]; ok {
		return v, nil
	}
	if v, ok := obj.GetLabels()[v1alpha5.ProvisionerNameLabelKey]; ok {
		return v, nil
	}
	return "", fmt.Errorf("object has no owner")
}

func List(ctx context.Context, c client.Client, opts ...client.ListOption) (*v1beta1.NodePoolList, error) {
	provisionerList := &v1alpha5.ProvisionerList{}
	if err := c.List(ctx, provisionerList, opts...); err != nil {
		return nil, err
	}
	convertedNodePools := lo.Map(provisionerList.Items, func(p v1alpha5.Provisioner, _ int) v1beta1.NodePool {
		return *New(&p)
	})
	nodePoolList := &v1beta1.NodePoolList{}
	if err := c.List(ctx, nodePoolList, opts...); err != nil {
		return nil, err
	}
	nodePoolList.Items = append(nodePoolList.Items, convertedNodePools...)
	return nodePoolList, nil
}
