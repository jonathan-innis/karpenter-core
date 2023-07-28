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
	"encoding/json"

	"github.com/samber/lo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/karpenter-core/pkg/apis/v1alpha5"
)

// NodeClaimSpec describes the desired state of the NodeClaim
type NodeClaimSpec struct {
	// Taints will be applied to the machine's node.
	// +optional
	Taints []v1.Taint `json:"taints,omitempty"`
	// StartupTaints are taints that are applied to nodes upon startup which are expected to be removed automatically
	// within a short period of time, typically by a DaemonSet that tolerates the taint. These are commonly used by
	// daemonsets to allow initialization and enforce startup ordering.  StartupTaints are ignored for provisioning
	// purposes in that pods are not required to tolerate a StartupTaint in order to have nodes provisioned for them.
	// +optional
	StartupTaints []v1.Taint `json:"startupTaints,omitempty"`
	// Requirements are layered with GetLabels and applied to every node.
	// +optional
	Requirements []v1.NodeSelectorRequirement `json:"requirements,omitempty"`
	// Resources models the resource requirements for the NodeClaim to launch
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`
	// KubeletConfiguration are options passed to the kubelet when provisioning nodes
	// +optional
	KubeletConfiguration *KubeletConfiguration `json:"kubeletConfiguration,omitempty"`
	// NodeClass is a reference to an object that defines provider specific configuration
	// +required
	NodeClass *NodeClassRef `json:"nodeClass"`
	// TODO @joinnis: Add a little write-up here on what to do
	Provider *Provider `json:"-"`
}

func KubeletAnnotation(k *v1alpha5.KubeletConfiguration) map[string]string {
	if k == nil {
		return nil
	}
	raw := lo.Must(json.Marshal(k))
	return map[string]string{KubeletCompatabilityAnnotationKey: string(raw)}
}

func ProviderAnnotation(p *v1alpha5.Provider) map[string]string {
	if p == nil {
		return nil
	}
	raw := lo.Must(json.Marshal(p)) // Provider should already have been validated so this shouldn't fail
	return map[string]string{ProviderCompatabilityAnnotationKey: string(raw)}
}

// KubeletConfiguration defines args to be used when configuring kubelet on provisioned nodes.
// They are a subset of the upstream types, recognizing not all options may be supported.
// Wherever possible, the types and names should reflect the upstream kubelet types.
// https://pkg.go.dev/k8s.io/kubelet/config/v1beta1#KubeletConfiguration
// https://github.com/kubernetes/kubernetes/blob/9f82d81e55cafdedab619ea25cabf5d42736dacf/cmd/kubelet/app/options/options.go#L53
type KubeletConfiguration struct {
	// clusterDNS is a list of IP addresses for the cluster DNS server.
	// Note that not all providers may use all addresses.
	//+optional
	ClusterDNS []string `json:"clusterDNS,omitempty"`
	// ContainerRuntime is the container runtime to be used with your worker nodes.
	// +optional
	ContainerRuntime *string `json:"containerRuntime,omitempty"`
	// MaxPods is an override for the maximum number of pods that can run on
	// a worker node instance.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	MaxPods *int32 `json:"maxPods,omitempty"`
	// PodsPerCore is an override for the number of pods that can run on a worker node
	// instance based on the number of cpu cores. This value cannot exceed MaxPods, so, if
	// MaxPods is a lower value, that value will be used.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	PodsPerCore *int32 `json:"podsPerCore,omitempty"`
	// SystemReserved contains resources reserved for OS system daemons and kernel memory.
	// +optional
	SystemReserved v1.ResourceList `json:"systemReserved,omitempty"`
	// KubeReserved contains resources reserved for Kubernetes system components.
	// +optional
	KubeReserved v1.ResourceList `json:"kubeReserved,omitempty"`
	// EvictionHard is the map of signal names to quantities that define hard eviction thresholds
	// +optional
	EvictionHard map[string]string `json:"evictionHard,omitempty"`
	// EvictionSoft is the map of signal names to quantities that define soft eviction thresholds
	// +optional
	EvictionSoft map[string]string `json:"evictionSoft,omitempty"`
	// EvictionSoftGracePeriod is the map of signal names to quantities that define grace periods for each eviction signal
	// +optional
	EvictionSoftGracePeriod map[string]metav1.Duration `json:"evictionSoftGracePeriod,omitempty"`
	// EvictionMaxPodGracePeriod is the maximum allowed grace period (in seconds) to use when terminating pods in
	// response to soft eviction thresholds being met.
	// +optional
	EvictionMaxPodGracePeriod *int32 `json:"evictionMaxPodGracePeriod,omitempty"`
	// ImageGCHighThresholdPercent is the percent of disk usage after which image
	// garbage collection is always run. The percent is calculated by dividing this
	// field value by 100, so this field must be between 0 and 100, inclusive.
	// When specified, the value must be greater than ImageGCLowThresholdPercent.
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:validation:Maximum:=100
	// +optional
	ImageGCHighThresholdPercent *int32 `json:"imageGCHighThresholdPercent,omitempty"`
	// ImageGCLowThresholdPercent is the percent of disk usage before which image
	// garbage collection is never run. Lowest disk usage to garbage collect to.
	// The percent is calculated by dividing this field value by 100,
	// so the field value must be between 0 and 100, inclusive.
	// When specified, the value must be less than imageGCHighThresholdPercent
	// +kubebuilder:validation:Minimum:=0
	// +kubebuilder:validation:Maximum:=100
	// +optional
	ImageGCLowThresholdPercent *int32 `json:"imageGCLowThresholdPercent,omitempty"`
	// CPUCFSQuota enables CPU CFS quota enforcement for containers that specify CPU limits.
	// +optional
	CPUCFSQuota *bool `json:"cpuCFSQuota,omitempty"`
}

type NodeClassRef struct {
	// Kind of the referent; More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds"
	// +optional
	Kind string `json:"kind,omitempty"`
	// Name of the referent; More info: http://kubernetes.io/docs/user-guide/identifiers#names
	// +required
	Name string `json:"name"`
	// API version of the referent
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
	// IsNodeTemplate tells Karpenter whether the in-memory representation of this object
	// is actually referring to a NodeTemplate object. This value is not actually part of the v1beta1 public-facing API
	// TODO @joinnis: Remove this field when v1alpha5 is unsupported in a future version of Karpenter
	IsNodeTemplate bool `json:"-"`
}

// ResourceRequirements models the required resources for the NodeClaim to launch
// Ths will eventually be transformed into v1.ResourceRequirements when we support resources.limits
type ResourceRequirements struct {
	// Requests describes the minimum required resources for the NodeClaim to launch
	// +optional
	Requests v1.ResourceList `json:"requests,omitempty"`
}

// NodeClaim is the Schema for the NodeClaims API
// +kubebuilder:object:root=true
// +kubebuilder:resource:path=nodeclaims,scope=Cluster,categories=karpenter,shortName={nc,ncs}
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Type",type="string",JSONPath=".metadata.labels.node\\.kubernetes\\.io/instance-type",description=""
// +kubebuilder:printcolumn:name="Zone",type="string",JSONPath=".metadata.labels.topology\\.kubernetes\\.io/zone",description=""
// +kubebuilder:printcolumn:name="Node",type="string",JSONPath=".status.nodeName",description=""
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""
// +kubebuilder:printcolumn:name="Capacity",type="string",JSONPath=".metadata.labels.karpenter\\.sh/capacity-type",priority=1,description=""
// +kubebuilder:printcolumn:name="NodePool",type="string",JSONPath=".metadata.labels.karpenter\\.sh/nodepool",priority=1,description=""
// +kubebuilder:printcolumn:name="NodeClass",type="string",JSONPath=".spec.nodeClass.name",priority=1,description=""
type NodeClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeClaimSpec   `json:"spec,omitempty"`
	Status NodeClaimStatus `json:"status,omitempty"`

	// IsMachine tells Karpenter whether the in-memory representation of this object
	// is actually referring to a NodeClaim object. This value is not actually part of the v1beta1 public-facing API
	// TODO @joinnis: Remove this field when v1alpha5 is unsupported in a future version of Karpenter
	IsMachine bool `json:"-"`
}

// NodeClaimList contains a list of NodeClaims
// +kubebuilder:object:root=true
type NodeClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeClaim `json:"items"`
}
