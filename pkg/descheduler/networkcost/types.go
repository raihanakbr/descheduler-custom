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

package networkcost


const (
	// DefaultNetworkGroupLabelKey is the default label key used to identify
	// pods that belong to the same communication group.
	DefaultNetworkGroupLabelKey = "network-group"
)

// TopologyCostConfig holds the cost values for different topology distances.
// Higher values indicate greater network cost (latency proxy).
// +k8s:deepcopy-gen=true
type TopologyCostConfig struct {
	// SameZone is the cost between nodes in the same zone but different hosts.
	SameZone float64 `json:"sameZone,omitempty"`

	// SameRegion is the cost between nodes in the same region but different zones.
	SameRegion float64 `json:"sameRegion,omitempty"`

	// CrossRegion is the cost between nodes in different regions.
	CrossRegion float64 `json:"crossRegion,omitempty"`
}

// LatencyMetricsConfig configures real-time latency collection from Prometheus.
// This type is shared by both NetworkCostEvictor and NodeUtilization plugins.
// +k8s:deepcopy-gen=true
type LatencyMetricsConfig struct {
	// Prometheus holds the PromQL query and label mapping configuration.
	Prometheus *LatencyPrometheusConfig `json:"prometheus,omitempty"`
}

// LatencyPrometheusConfig holds the PromQL query and label mapping for
// extracting node-to-node latency data from Prometheus.
// +k8s:deepcopy-gen=true
type LatencyPrometheusConfig struct {
	// Query is a PromQL expression returning node-to-node latency.
	// The query must produce a vector where each sample has labels
	// identified by SourceNodeLabel and TargetNodeLabel whose values
	// are Kubernetes node names (not IPs).
	//
	// Goldpinger v3 exports 'goldpinger_instance' (source node name) and
	// 'host_ip' (target node IP, NOT name). To get the target node name,
	// join with kube_pod_info from kube-state-metrics:
	//
	//   (avg_over_time(goldpinger_peers_response_time_s_sum[5m])
	//   / avg_over_time(goldpinger_peers_response_time_s_count[5m]))
	//   * on(pod) group_left(node) kube_pod_info{namespace="monitoring"}
	Query string `json:"query"`

	// SourceNodeLabel is the Prometheus label identifying the source node name.
	// For Goldpinger v3 with kube_pod_info join: "goldpinger_instance"
	SourceNodeLabel string `json:"sourceNodeLabel,omitempty"`

	// TargetNodeLabel is the Prometheus label identifying the target node name.
	// For Goldpinger v3 with kube_pod_info join: "node"
	TargetNodeLabel string `json:"targetNodeLabel,omitempty"`
}

const (
	// DefaultSourceNodeLabel is the default Prometheus label for the source node.
	// Matches Goldpinger v3's 'goldpinger_instance' label (= Kubernetes node name).
	DefaultSourceNodeLabel = "goldpinger_instance"
	// DefaultTargetNodeLabel is the default Prometheus label for the target node.
	// Requires a PromQL join with kube_pod_info to produce the 'node' label,
	// since Goldpinger v3 only exposes 'host_ip' for the target (not the node name).
	DefaultTargetNodeLabel = "node"
)

// SetDefaultsLatencyMetrics applies default label names to a LatencyMetricsConfig.
func SetDefaultsLatencyMetrics(c *LatencyMetricsConfig) {
	if c == nil || c.Prometheus == nil {
		return
	}
	if c.Prometheus.SourceNodeLabel == "" {
		c.Prometheus.SourceNodeLabel = DefaultSourceNodeLabel
	}
	if c.Prometheus.TargetNodeLabel == "" {
		c.Prometheus.TargetNodeLabel = DefaultTargetNodeLabel
	}
}

// DeepCopyInto writes a deep copy of LatencyMetricsConfig into out.
func (in *LatencyMetricsConfig) DeepCopyInto(out *LatencyMetricsConfig) {
	*out = *in
	if in.Prometheus != nil {
		in, out := &in.Prometheus, &out.Prometheus
		*out = new(LatencyPrometheusConfig)
		**out = **in
	}
}

// DeepCopy returns a deep copy of LatencyMetricsConfig.
func (in *LatencyMetricsConfig) DeepCopy() *LatencyMetricsConfig {
	if in == nil {
		return nil
	}
	out := new(LatencyMetricsConfig)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto writes a deep copy of LatencyPrometheusConfig into out.
func (in *LatencyPrometheusConfig) DeepCopyInto(out *LatencyPrometheusConfig) {
	*out = *in
}

// DeepCopy returns a deep copy of LatencyPrometheusConfig.
func (in *LatencyPrometheusConfig) DeepCopy() *LatencyPrometheusConfig {
	if in == nil {
		return nil
	}
	out := new(LatencyPrometheusConfig)
	in.DeepCopyInto(out)
	return out
}
