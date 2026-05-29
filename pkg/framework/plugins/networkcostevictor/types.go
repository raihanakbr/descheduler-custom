/*
Copyright 2025 The Kubernetes Authors.

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

package networkcostevictor

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/descheduler/pkg/descheduler/networkcost"
)

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NetworkCostEvictorArgs holds arguments used to configure the NetworkCostEvictor plugin.
type NetworkCostEvictorArgs struct {
	metav1.TypeMeta `json:",inline"`

	// NetworkGroupLabelKey is the label key used to identify pods that belong
	// to the same communication group. Pods with the same value for this key
	// are considered to communicate frequently.
	// Default: "network-group"
	NetworkGroupLabelKey string `json:"networkGroupLabelKey,omitempty"`

	// LatencyMetrics configures real-time latency collection from Prometheus
	// (via Goldpinger). When set, the plugin uses measured node-to-node latency
	// instead of hardcoded topology costs.
	// When nil, hardcoded topology costs (1/5/10) are used.
	LatencyMetrics *networkcost.LatencyMetricsConfig `json:"latencyMetrics,omitempty"`

	// MinBetterCandidatesPercent is the minimum percentage of candidate nodes
	// that must offer lower network cost than the current node before eviction
	// is allowed. Higher values increase the probability that the scheduler
	// places the pod on a better node, but may block more evictions.
	// Default: 50, Range: 1-100
	MinBetterCandidatesPercent int `json:"minBetterCandidatesPercent,omitempty"`

	// ExcludeSameOwner controls whether pods owned by the same controller
	// (e.g. replicas of the same Deployment) are excluded from dependency
	// pod discovery. Set to true for typical microservice patterns where
	// replicas don't communicate with each other. Set to false for
	// distributed systems (e.g. Cassandra, Redis Cluster) where replicas
	// do communicate via gossip/replication.
	// Default: true
	ExcludeSameOwner *bool `json:"excludeSameOwner,omitempty"`
}
