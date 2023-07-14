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
	"sort"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"knative.dev/pkg/ptr"
)

// NodePoolSpec is the top level provisioner specification. Provisioners
// launch nodes in response to pods that are unschedulable. A single provisioner
// is capable of managing a diverse set of nodes. Node properties are determined
// from a combination of provisioner and pod scheduling constraints.
type NodePoolSpec struct {
	// Template contains the template of possibilities for the provisioning logic to launch a Machine with.
	// Machines launched from this NodePool will often be further constrained than the template specifies.
	// +optional
	Template MachineTemplate `json:"template,omitempty"`
	// TTLSecondsAfterEmpty is the duration the controller will wait
	// before attempting consolidation, measured from when the machine is
	// first seen to be consolidatable.
	//
	// Termination due to under-utilization is disabled if this field is not set.
	// +optional
	TTLUntilConsolidated *metav1.Duration `json:"ttlAfterEmpty,omitempty"`
	// TTLSecondsUntilExpired is the duration the controller will wait
	// before terminating a machine, measured from when the machine is created. This
	// is useful to implement features like eventually consistent machine upgrade,
	// memory leak protection, and disruption testing.
	//
	// Termination due to expiration is disabled if this field is not set.
	// +optional
	TTLUntilExpired *metav1.Duration `json:"ttlUntilExpired,omitempty"`
	// Limits define a set of bounds for provisioning capacity.
	Limits v1.ResourceList `json:"limits,omitempty"`
	// Weight is the priority given to the provisioner during scheduling. A higher
	// numerical weight indicates that this provisioner will be ordered
	// ahead of other provisioners with lower weights. A provisioner with no weight
	// will be treated as if it is a provisioner with a weight of 0.
	// +kubebuilder:validation:Minimum:=1
	// +kubebuilder:validation:Maximum:=100
	// +optional
	Weight *int32 `json:"weight,omitempty"`
}

type MachineTemplate struct {
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MachineSpec `json:"spec,omitempty"`
}

type Consolidation struct {
	// Enabled enables consolidation if it has been set
	Enabled *bool `json:"enabled,omitempty"`
}

// NodePool is the Schema for the Provisioners API
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=nodepools,scope=Cluster,categories=karpenter,shortName="np,nps"
// +kubebuilder:printcolumn:name="Template",type="string",JSONPath=".spec.template.spec.nodeTemplateRef.name",description=""
// +kubebuilder:subresource:status
type NodePool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodePoolSpec   `json:"spec,omitempty"`
	Status NodePoolStatus `json:"status,omitempty"`
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
