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
	v1 "k8s.io/api/core/v1"
)

const (
	// DefaultMinBetterCandidatesPercent is the default percentage of candidate
	// nodes that must offer strictly lower cost before eviction is allowed.
	// This increases the probability that the scheduler (which we don't control)
	// will place the evicted pod on a node with better network locality.
	DefaultMinBetterCandidatesPercent = 50
)

// CostProvider computes the communication cost between two nodes.
// Higher return values indicate a more expensive network path.
// Implementations must be safe for concurrent use.
type CostProvider interface {
	Cost(nodeA, nodeB *v1.Node) float64
}

// TopologyCostProvider uses hardcoded topology-based costs derived from
// standard Kubernetes labels (zone/region). This is the default fallback
// when Prometheus latency data is not configured.
type TopologyCostProvider struct{}

// DefaultTopologyCostConfig returns the default cost configuration.
func DefaultTopologyCostConfig() TopologyCostConfig {
	return TopologyCostConfig{
		SameZone:    1.0,
		SameRegion:  5.0,
		CrossRegion: 10.0,
	}
}

// TopologyCost computes the communication cost between two nodes based on
// their topology labels. It uses the standard Kubernetes well-known labels:
//   - topology.kubernetes.io/zone
//   - topology.kubernetes.io/region
//
// The cost model is:
//   - Same node:             0
//   - Same zone, diff node:  config.SameZone
//   - Same region, diff zone: config.SameRegion
//   - Different region:      config.CrossRegion
func (p *TopologyCostProvider) Cost(nodeA, nodeB *v1.Node) float64 {
	if nodeA.Name == nodeB.Name {
		return 0
	}

	config := DefaultTopologyCostConfig()

	zoneA := nodeA.Labels[v1.LabelTopologyZone]
	zoneB := nodeB.Labels[v1.LabelTopologyZone]
	regionA := nodeA.Labels[v1.LabelTopologyRegion]
	regionB := nodeB.Labels[v1.LabelTopologyRegion]

	// same zone implies same region
	if zoneA != "" && zoneA == zoneB {
		return config.SameZone
	}

	// same region but different zone
	if regionA != "" && regionA == regionB {
		return config.SameRegion
	}

	// different region (or labels not set)
	return config.CrossRegion
}

// LatencyCostProvider uses real latency measurements from a LatencyMatrix
// (typically populated from Prometheus/Goldpinger). If a specific node pair
// is not found in the matrix, it falls back to the Fallback provider.
type LatencyCostProvider struct {
	Matrix   LatencyMatrix
	Fallback CostProvider // used when a specific node pair is missing from the matrix
}

func (p *LatencyCostProvider) Cost(nodeA, nodeB *v1.Node) float64 {
	if nodeA.Name == nodeB.Name {
		return 0
	}
	if lat, ok := p.Matrix.GetLatency(nodeA.Name, nodeB.Name); ok {
		return lat
	}
	// node pair not found in matrix — use fallback
	if p.Fallback != nil {
		return p.Fallback.Cost(nodeA, nodeB)
	}
	return 1.0 // worst-case fallback (1 second)
}
