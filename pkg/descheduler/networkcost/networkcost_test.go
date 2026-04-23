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
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
)

func makeNode(name, region, zone string) *v1.Node {
	labels := map[string]string{}
	if region != "" {
		labels[v1.LabelTopologyRegion] = region
	}
	if zone != "" {
		labels[v1.LabelTopologyZone] = zone
	}
	return &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}

func makePod(name, namespace, nodeName, networkGroup string) *v1.Pod {
	labels := map[string]string{}
	if networkGroup != "" {
		labels[DefaultNetworkGroupLabelKey] = networkGroup
	}
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID(name + "-uid"),
			Labels:    labels,
		},
		Spec: v1.PodSpec{
			NodeName: nodeName,
		},
	}
}

func TestTopologyCostProvider(t *testing.T) {
	provider := &TopologyCostProvider{}
	config := DefaultTopologyCostConfig()

	tests := []struct {
		name     string
		nodeA    *v1.Node
		nodeB    *v1.Node
		expected float64
	}{
		{
			name:     "same node",
			nodeA:    makeNode("node-a", "us-east-1", "us-east-1a"),
			nodeB:    makeNode("node-a", "us-east-1", "us-east-1a"),
			expected: 0,
		},
		{
			name:     "same zone different node",
			nodeA:    makeNode("node-a", "us-east-1", "us-east-1a"),
			nodeB:    makeNode("node-b", "us-east-1", "us-east-1a"),
			expected: config.SameZone,
		},
		{
			name:     "same region different zone",
			nodeA:    makeNode("node-a", "us-east-1", "us-east-1a"),
			nodeB:    makeNode("node-b", "us-east-1", "us-east-1b"),
			expected: config.SameRegion,
		},
		{
			name:     "different region",
			nodeA:    makeNode("node-a", "us-east-1", "us-east-1a"),
			nodeB:    makeNode("node-b", "eu-west-1", "eu-west-1a"),
			expected: config.CrossRegion,
		},
		{
			name:     "no labels set",
			nodeA:    makeNode("node-a", "", ""),
			nodeB:    makeNode("node-b", "", ""),
			expected: config.CrossRegion,
		},
		{
			name:     "zone set but no region",
			nodeA:    makeNode("node-a", "", "zone-a"),
			nodeB:    makeNode("node-b", "", "zone-a"),
			expected: config.SameZone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := provider.Cost(tt.nodeA, tt.nodeB)
			if cost != tt.expected {
				t.Errorf("TopologyCostProvider.Cost() = %v, want %v", cost, tt.expected)
			}
		})
	}
}

func TestLatencyCostProvider(t *testing.T) {
	matrix := LatencyMatrix{
		"node-a": {"node-b": 0.002, "node-c": 0.008},
		"node-b": {"node-a": 0.002, "node-c": 0.010},
	}
	provider := &LatencyCostProvider{
		Matrix:   matrix,
		Fallback: &TopologyCostProvider{},
	}

	tests := []struct {
		name     string
		nodeA    *v1.Node
		nodeB    *v1.Node
		expected float64
	}{
		{
			name:     "same node",
			nodeA:    makeNode("node-a", "us-east-1", "us-east-1a"),
			nodeB:    makeNode("node-a", "us-east-1", "us-east-1a"),
			expected: 0,
		},
		{
			name:     "pair in matrix",
			nodeA:    makeNode("node-a", "us-east-1", "us-east-1a"),
			nodeB:    makeNode("node-b", "us-east-1", "us-east-1b"),
			expected: 0.002,
		},
		{
			name:     "pair not in matrix, fallback to topology",
			nodeA:    makeNode("node-c", "eu-west-1", "eu-west-1a"),
			nodeB:    makeNode("node-d", "us-east-1", "us-east-1a"),
			expected: DefaultTopologyCostConfig().CrossRegion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := provider.Cost(tt.nodeA, tt.nodeB)
			if cost != tt.expected {
				t.Errorf("LatencyCostProvider.Cost() = %v, want %v", cost, tt.expected)
			}
		})
	}
}

func TestFindDependencyPods(t *testing.T) {
	nodeA := makeNode("node-a", "us-east-1", "us-east-1a")
	nodeB := makeNode("node-b", "us-east-1", "us-east-1b")

	pod1 := makePod("pod-1", "default", "node-a", "group-1")
	pod2 := makePod("pod-2", "default", "node-a", "group-1")
	pod3 := makePod("pod-3", "default", "node-b", "group-1")
	pod4 := makePod("pod-4", "default", "node-b", "group-2")
	pod5 := makePod("pod-5", "default", "node-b", "")

	getPodsAssigned := podutil.GetPodsAssignedToNodeFunc(func(nodeName string, filterFunc podutil.FilterFunc) ([]*v1.Pod, error) {
		podsByNode := map[string][]*v1.Pod{
			"node-a": {pod1, pod2},
			"node-b": {pod3, pod4, pod5},
		}
		pods := podsByNode[nodeName]
		if filterFunc != nil {
			var filtered []*v1.Pod
			for _, p := range pods {
				if filterFunc(p) {
					filtered = append(filtered, p)
				}
			}
			return filtered, nil
		}
		return pods, nil
	})

	nodes := []*v1.Node{nodeA, nodeB}

	tests := []struct {
		name          string
		pod           *v1.Pod
		expectedCount int
	}{
		{
			name:          "pod with group-1 finds 2 deps (excludes self)",
			pod:           pod1,
			expectedCount: 2,
		},
		{
			name:          "pod with group-2 finds 0 deps (only one in group)",
			pod:           pod4,
			expectedCount: 0,
		},
		{
			name:          "pod without label finds 0 deps",
			pod:           pod5,
			expectedCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := FindDependencyPods(tt.pod, DefaultNetworkGroupLabelKey, getPodsAssigned, nodes)
			if len(deps) != tt.expectedCount {
				t.Errorf("FindDependencyPods() returned %d pods, want %d", len(deps), tt.expectedCount)
			}
		})
	}
}

func TestShouldAllowEviction(t *testing.T) {
	provider := &TopologyCostProvider{}

	nodeA := makeNode("node-a", "us-east-1", "us-east-1a")
	nodeB := makeNode("node-b", "us-east-1", "us-east-1b")
	nodeC := makeNode("node-c", "eu-west-1", "eu-west-1a")

	nodesMap := map[string]*v1.Node{
		"node-a": nodeA, "node-b": nodeB, "node-c": nodeC,
	}
	allNodes := []*v1.Node{nodeA, nodeB, nodeC}

	pod1 := makePod("pod-1", "default", "node-c", "group-1")
	dep1 := makePod("dep-1", "default", "node-a", "group-1")
	dep2 := makePod("dep-2", "default", "node-b", "group-1")
	podAlone := makePod("pod-alone", "default", "node-a", "group-alone")
	podNoLabel := makePod("pod-no-label", "default", "node-a", "")
	podOptimal := makePod("pod-optimal", "default", "node-a", "group-opt")
	depOpt1 := makePod("dep-opt-1", "default", "node-a", "group-opt")

	getPodsAssigned := podutil.GetPodsAssignedToNodeFunc(func(nodeName string, filterFunc podutil.FilterFunc) ([]*v1.Pod, error) {
		podsByNode := map[string][]*v1.Pod{
			"node-a": {pod1, dep1, podAlone, podNoLabel, podOptimal, depOpt1},
			"node-b": {dep2},
			"node-c": {pod1},
		}
		pods := podsByNode[nodeName]
		if filterFunc != nil {
			var filtered []*v1.Pod
			for _, p := range pods {
				if filterFunc(p) {
					filtered = append(filtered, p)
				}
			}
			return filtered, nil
		}
		return pods, nil
	})

	tests := []struct {
		name             string
		pod              *v1.Pod
		candidateNodes   []*v1.Node
		minBetterPercent int
		expected         bool
	}{
		{
			name:             "pod without network-group label → allow",
			pod:              podNoLabel,
			candidateNodes:   []*v1.Node{nodeB, nodeC},
			minBetterPercent: DefaultMinBetterCandidatesPercent,
			expected:         true,
		},
		{
			name:             "pod with no dependency pods → allow",
			pod:              podAlone,
			candidateNodes:   []*v1.Node{nodeB, nodeC},
			minBetterPercent: DefaultMinBetterCandidatesPercent,
			expected:         true,
		},
		{
			name: "pod in eu-west, deps in us-east → both candidates better → allow",
			pod:  pod1,
			// current cost: 20 (cross-region to both deps)
			// node-a: 0+5=5 < 20 ✓, node-b: 5+0=5 < 20 ✓
			// 2/2 better (100%) >= 50% → allow
			candidateNodes:   []*v1.Node{nodeA, nodeB},
			minBetterPercent: DefaultMinBetterCandidatesPercent,
			expected:         true,
		},
		{
			name: "pod already optimal, only worse candidates → block",
			pod:  podOptimal,
			// current cost: 0 (same node as dep)
			// node-c: 10, NOT < 0 → 0/1 better → block
			candidateNodes:   []*v1.Node{nodeC},
			minBetterPercent: DefaultMinBetterCandidatesPercent,
			expected:         false,
		},
		{
			name:             "no candidate nodes → block",
			pod:              pod1,
			candidateNodes:   []*v1.Node{},
			minBetterPercent: DefaultMinBetterCandidatesPercent,
			expected:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShouldAllowEviction(
				tt.pod, DefaultNetworkGroupLabelKey, tt.candidateNodes,
				getPodsAssigned, allNodes, nodesMap, provider,
				tt.minBetterPercent,
			)
			if result != tt.expected {
				t.Errorf("ShouldAllowEviction() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMinBetterCandidatesPercent(t *testing.T) {
	provider := &TopologyCostProvider{}

	nodeA := makeNode("node-a", "us-east-1", "us-east-1a")
	nodeB := makeNode("node-b", "us-east-1", "us-east-1b")
	nodeC := makeNode("node-c", "eu-west-1", "eu-west-1a")
	nodeD := makeNode("node-d", "eu-west-1", "eu-west-1b")
	nodeE := makeNode("node-e", "ap-south-1", "ap-south-1a")

	pod := makePod("pod-1", "default", "node-c", "group-1")
	dep1 := makePod("dep-1", "default", "node-a", "group-1")

	nodesMap := map[string]*v1.Node{
		"node-a": nodeA, "node-b": nodeB, "node-c": nodeC,
		"node-d": nodeD, "node-e": nodeE,
	}
	allNodes := []*v1.Node{nodeA, nodeB, nodeC, nodeD, nodeE}

	getPodsAssigned := podutil.GetPodsAssignedToNodeFunc(func(nodeName string, filterFunc podutil.FilterFunc) ([]*v1.Pod, error) {
		podsByNode := map[string][]*v1.Pod{
			"node-a": {dep1},
			"node-b": {},
			"node-c": {pod},
			"node-d": {},
			"node-e": {},
		}
		return podsByNode[nodeName], nil
	})

	// Current cost: pod on node-c, dep1 on node-a → CrossRegion = 10
	// node-a: dep1 same node = 0  → BETTER (0 <= 10)
	// node-b: dep1 same region = 5 → BETTER (5 <= 10)
	// node-d: dep1 cross region = 10 → EQUAL (10 <= 10, counts with <=)
	// node-e: dep1 cross region = 10 → EQUAL (10 <= 10, counts with <=)
	// 4 out of 4 candidates satisfy <= (100%)

	tests := []struct {
		name             string
		candidates       []*v1.Node
		minBetterPercent int
		expected         bool
	}{
		{
			name:             "50% threshold, 4/4 satisfy <= → allow",
			candidates:       []*v1.Node{nodeA, nodeB, nodeD, nodeE},
			minBetterPercent: 50,
			expected:         true,
		},
		{
			name:             "75% threshold, 4/4 satisfy <= → allow",
			candidates:       []*v1.Node{nodeA, nodeB, nodeD, nodeE},
			minBetterPercent: 75,
			expected:         true,
		},
		{
			name:             "25% threshold, 4/4 satisfy <= → allow",
			candidates:       []*v1.Node{nodeA, nodeB, nodeD, nodeE},
			minBetterPercent: 25,
			expected:         true,
		},
		{
			name:             "small cluster: 1 candidate better, 50% → allow (floor of 1)",
			candidates:       []*v1.Node{nodeA},
			minBetterPercent: 50,
			expected:         true,
		},
		{
			name:             "equal cost counts with <=, 2/2 satisfy → allow",
			candidates:       []*v1.Node{nodeD, nodeE},
			minBetterPercent: 50,
			expected:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShouldAllowEviction(
				pod, DefaultNetworkGroupLabelKey, tt.candidates,
				getPodsAssigned, allNodes, nodesMap, provider,
				tt.minBetterPercent,
			)
			if result != tt.expected {
				t.Errorf("ShouldAllowEviction() = %v, want %v", result, tt.expected)
			}
		})
	}
}
