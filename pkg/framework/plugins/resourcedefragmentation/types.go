/*
Copyright 2026 The Kubernetes Authors.

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

package resourcedefragmentation

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/descheduler/pkg/api"
)

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type ResourceDefragmentationArgs struct {
	metav1.TypeMeta `json:",inline"`

	Namespaces *api.Namespaces `json:"namespaces,omitempty"`

	// UsageMode controls which resource signal ResourceDefragmentation uses for
	// the bin score / TOPSIS. Supported values: requests, actual-raw, actual-ewma,
	// actual-ewma-persisted, published-ewma. Empty keeps legacy auto behavior:
	// requests without a metrics collector, actual-ewma with one.
	UsageMode string `json:"usageMode,omitempty"`

	// EWMABeta controls persisted EWMA smoothing for usageMode actual-ewma-persisted.
	// Empty/invalid values default to 0.9.
	EWMABeta float64 `json:"ewmaBeta,omitempty"`

	// PublishedUsageMaxAgeSeconds rejects loose-agent published usage older than this value.
	// Zero disables stale-state rejection.
	PublishedUsageMaxAgeSeconds int64 `json:"publishedUsageMaxAgeSeconds,omitempty"`

	MaxEvictions int `json:"maxEvictions,omitempty"`

	// ConsolidationThreshold defines "under-utilized". A worker node whose
	// utilization is below this value is a candidate to be emptied. Range [0, 1].
	// Default 0.40.
	ConsolidationThreshold float64 `json:"consolidationThreshold,omitempty"`

	// ConsolidationTarget is the packing ceiling for a destination node: an
	// evicted pod will only be relocated onto a node whose projected request
	// utilization stays at or below this value, leaving headroom. Range [0, 1].
	// Default 0.90.
	ConsolidationTarget float64 `json:"consolidationTarget,omitempty"`

	// BalancePenaltyWeight (λ, range [0,1]) gates relocation targets by how much a
	// placement would worsen the destination's cpu:mem balance. A target is
	// rejected if it would reduce that node's balance (1 − |cpuFrac − memFrac|) by
	// more than (1 − λ). This stops a skewed pod from being poured onto a clean
	// balanced node — which only relocates stranding instead of reducing it — while
	// always admitting complementary moves (which raise balance). λ=0 disables the
	// gate (legacy behavior); higher λ is stricter. Default 0 (off).
	BalancePenaltyWeight float64 `json:"balancePenaltyWeight,omitempty"`
}
