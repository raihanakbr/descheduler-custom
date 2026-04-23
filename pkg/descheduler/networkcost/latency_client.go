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

import (
	"context"
	"fmt"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	"k8s.io/klog/v2"
)

// LatencyMatrix maps source_node → target_node → latency_in_seconds.
// It provides O(1) lookup of the measured latency between any two nodes.
type LatencyMatrix map[string]map[string]float64

// GetLatency returns the latency between the source and target node.
// Returns (latency, true) if found, (0, false) if the pair is not in the matrix.
func (m LatencyMatrix) GetLatency(source, target string) (float64, bool) {
	if targets, ok := m[source]; ok {
		if lat, ok := targets[target]; ok {
			return lat, true
		}
	}
	return 0, false
}

// QueryLatencyMatrix queries Prometheus with the given PromQL query and
// builds a LatencyMatrix from the result vector. Each sample in the vector
// must have labels identified by sourceLabel and targetLabel.
//
// The query should return a vector like:
//
//	goldpinger_host_name="node-a", goldpinger_peer_name="node-b" → 0.002
//	goldpinger_host_name="node-a", goldpinger_peer_name="node-c" → 0.008
func QueryLatencyMatrix(
	ctx context.Context,
	client promapi.Client,
	query string,
	sourceLabel string,
	targetLabel string,
) (LatencyMatrix, error) {
	api := promv1.NewAPI(client)
	result, warnings, err := api.Query(ctx, query, time.Now())
	if err != nil {
		return nil, fmt.Errorf("prometheus query failed: %w", err)
	}

	for _, w := range warnings {
		klog.V(2).InfoS("Prometheus query warning", "warning", w)
	}

	vector, ok := result.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("expected vector result from Prometheus, got %T", result)
	}

	matrix := make(LatencyMatrix)
	skipped := 0
	for _, sample := range vector {
		src := string(sample.Metric[model.LabelName(sourceLabel)])
		tgt := string(sample.Metric[model.LabelName(targetLabel)])
		if src == "" || tgt == "" {
			skipped++
			continue
		}
		if src == tgt {
			// skip self-loops (same node pinging itself)
			continue
		}
		if _, ok := matrix[src]; !ok {
			matrix[src] = make(map[string]float64)
		}
		matrix[src][tgt] = float64(sample.Value)
	}

	klog.V(2).InfoS("Built latency matrix from Prometheus",
		"totalSamples", len(vector),
		"sourceNodes", len(matrix),
		"skippedSamples", skipped,
	)
	return matrix, nil
}

// BuildCostProvider constructs a CostProvider based on the given configuration.
//
// Behavior:
//   - If latencyConfig is nil or has no Prometheus config → returns TopologyCostProvider (hardcoded defaults)
//   - If latencyConfig is provided but prometheusClient is nil → returns error (fail-fast)
//   - If latencyConfig is provided and prometheusClient is available → queries Prometheus, returns LatencyCostProvider
func BuildCostProvider(
	ctx context.Context,
	latencyConfig *LatencyMetricsConfig,
	prometheusClient promapi.Client,
) (CostProvider, error) {
	if latencyConfig == nil || latencyConfig.Prometheus == nil {
		klog.V(2).InfoS("No latency metrics configured, using topology-based cost provider")
		return &TopologyCostProvider{}, nil
	}

	if prometheusClient == nil {
		return nil, fmt.Errorf("latencyMetrics configured but Prometheus client not available; " +
			"ensure metricsProviders with source=Prometheus is configured at policy level")
	}

	klog.V(2).InfoS("Querying Prometheus for latency matrix",
		"query", latencyConfig.Prometheus.Query,
		"sourceLabel", latencyConfig.Prometheus.SourceNodeLabel,
		"targetLabel", latencyConfig.Prometheus.TargetNodeLabel,
	)

	matrix, err := QueryLatencyMatrix(
		ctx,
		prometheusClient,
		latencyConfig.Prometheus.Query,
		latencyConfig.Prometheus.SourceNodeLabel,
		latencyConfig.Prometheus.TargetNodeLabel,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query latency matrix: %w", err)
	}

	return &LatencyCostProvider{
		Matrix:   matrix,
		Fallback: &TopologyCostProvider{},
	}, nil
}
