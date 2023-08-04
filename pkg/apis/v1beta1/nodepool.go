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

package v1beta1

import (
	"fmt"
	"sort"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/ptr"
)

// NodePoolSpec is the top level provisioner specification. Provisioners
// launch nodes in response to pods that are unschedulable. A single provisioner
// is capable of managing a diverse set of nodes. Node properties are determined
// from a combination of provisioner and pod scheduling constraints.
type NodePoolSpec struct {
	// Template contains the template of possibilities for the provisioning logic to launch a NodeClaim with.
	// NodeClaims launched from this NodePool will often be further constrained than the template specifies.
	// +optional
	Template NodeClaimTemplate `json:"template,omitempty"`
	// Deprovisioning contains the parameters that relate to Karpenter's deprovisioning logic
	// +kubebuilder:default={"consolidationTTL": "15s","consolidationPolicy": "WhenUnderutilized", "expirationTTL": "90d"}
	// +optional
	Deprovisioning Deprovisioning `json:"deprovisioning" hash:"ignore"`
	// Limits define a set of bounds for provisioning capacity.
	// +optional
	Limits Limits `json:"limits,omitempty" hash:"ignore"`
	// Weight is the priority given to the provisioner during scheduling. A higher
	// numerical weight indicates that this provisioner will be ordered
	// ahead of other provisioners with lower weights. A provisioner with no weight
	// will be treated as if it is a provisioner with a weight of 0.
	// +kubebuilder:validation:Minimum:=1
	// +kubebuilder:validation:Maximum:=100
	// +optional
	Weight *int32 `json:"weight,omitempty" hash:"ignore"`
}

type Deprovisioning struct {
	// ConsolidationTTL is the duration the controller will wait
	// before attempting to terminate nodes that are underutilized.
	// Refer to ConsolidationPolicy for how underutilization is considered.
	// +kubebuilder:default:="15s"
	// +optional
	ConsolidationTTL metav1.Duration `json:"consolidationTTL,omitempty"`
	// ConsolidationPolicy describes which nodes Karpenter can deprovision through its consolidation
	// algorithm. This policy defaults to "WhenUnderutilized" if not specified
	// +kubebuilder:default:="WhenUnderutilized"
	// +kubebuilder:validation:Enum:={Never,WhenEmpty,WhenUnderutilized}
	// +optional
	ConsolidationPolicy ConsolidationPolicy `json:"consolidationPolicy,omitempty"`
	// ExpirationTTL is the duration the controller will wait
	// before terminating a node, measured from when the node is created. This
	// is useful to implement features like eventually consistent node upgrade,
	// memory leak protection, and disruption testing.
	// +kubebuilder:default:="90d"
	// +optional
	ExpirationTTL metav1.Duration `json:"expirationTTL,omitempty"`
	// EmptinessTTL exists for compatibility to allow us to model the v1alpha5 APIs in
	// terms of the v1beta1 APIs. This value is not actually part of the v1beta1 public-facing API
	// TODO @joinnis: Remove this field when v1alpha5 is unsupported in a future version of Karpenter
	EmptinessTTL *metav1.Duration `json:"-"`
}

type ConsolidationPolicy string

const (
	ConsolidationPolicyNever             ConsolidationPolicy = "Never"
	ConsolidationPolicyWhenEmpty         ConsolidationPolicy = "WhenEmpty"
	ConsolidationPolicyWhenUnderutilized ConsolidationPolicy = "WhenUnderutilized"
)

type Limits v1.ResourceList

func (l Limits) ExceededBy(resources v1.ResourceList) error {
	if l == nil {
		return nil
	}
	for resourceName, usage := range resources {
		if limit, ok := l[resourceName]; ok {
			if usage.Cmp(limit) > 0 {
				return fmt.Errorf("%s resource usage of %v exceeds limit of %v", resourceName, usage.AsDec(), limit.AsDec())
			}
		}
	}
	return nil
}

type NodeClaimTemplate struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              NodeClaimSpec `json:"spec,omitempty"`
}

// NodePool is the Schema for the Provisioners API
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=nodepools,scope=Cluster,categories=karpenter,shortName={np,nps}
// +kubebuilder:printcolumn:name="NodeClass",type="string",JSONPath=".spec.template.spec.nodeClass.name",description=""
// +kubebuilder:printcolumn:name="Weight",type="string",JSONPath=".spec.weight",priority=1,description=""
// +kubebuilder:subresource:status
type NodePool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodePoolSpec   `json:"spec,omitempty"`
	Status NodePoolStatus `json:"status,omitempty"`

	// IsProvisioner tells Karpenter whether the in-memory representation of this object
	// is actually referring to a Provisioner object. This value is not actually part of the v1beta1 public-facing API
	// TODO @joinnis: Remove this field when v1alpha5 is unsupported in a future version of Karpenter
	IsProvisioner bool `json:"-" hash:"ignore"`
}

func (in *NodePool) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash(in.Spec, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
}

func (in *NodePool) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash(in.Spec.Template, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
}

// NodePoolList contains a list of NodePool
// +kubebuilder:object:root=true
type NodePoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodePool `json:"items"`
}

// OrderByWeight orders the provisioners in the NodePoolList
// by their priority weight in-place
func (pl *NodePoolList) OrderByWeight() {
	sort.Slice(pl.Items, func(a, b int) bool {
		return ptr.Int32Value(pl.Items[a].Spec.Weight) > ptr.Int32Value(pl.Items[b].Spec.Weight)
	})
}
