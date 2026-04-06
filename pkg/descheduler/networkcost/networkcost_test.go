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

func TestTopologyCost(t *testing.T) {
	config := DefaultTopologyCostConfig()

	tests := []struct {
		name     string
		nodeA    *v1.Node
		nodeB    *v1.Node
		expected int
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
			cost := TopologyCost(tt.nodeA, tt.nodeB, config)
			if cost != tt.expected {
				t.Errorf("TopologyCost() = %d, want %d", cost, tt.expected)
			}
		})
	}
}

func TestComputePlacementCost(t *testing.T) {
	config := DefaultTopologyCostConfig()

	nodeA := makeNode("node-a", "us-east-1", "us-east-1a")
	nodeB := makeNode("node-b", "us-east-1", "us-east-1b")
	nodeC := makeNode("node-c", "eu-west-1", "eu-west-1a")

	nodesMap := map[string]*v1.Node{
		"node-a": nodeA,
		"node-b": nodeB,
		"node-c": nodeC,
	}

	depPods := []*v1.Pod{
		makePod("dep-1", "default", "node-a", "group-1"),
		makePod("dep-2", "default", "node-b", "group-1"),
	}

	tests := []struct {
		name          string
		candidateNode *v1.Node
		expected      int
	}{
		{
			name:          "candidate in same zone as dep-1, same region as dep-2",
			candidateNode: nodeA,
			// cost to dep-1 on node-a: 0 (same node), cost to dep-2 on node-b: 5 (same region)
			expected: 0 + config.SameRegion,
		},
		{
			name:          "candidate in different region from both",
			candidateNode: nodeC,
			// cost to dep-1 on node-a: 10, cost to dep-2 on node-b: 10
			expected: config.CrossRegion + config.CrossRegion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := ComputePlacementCost(tt.candidateNode, depPods, nodesMap, config)
			if cost != tt.expected {
				t.Errorf("ComputePlacementCost() = %d, want %d", cost, tt.expected)
			}
		})
	}
}

func TestComputePlacementCostMissingNode(t *testing.T) {
	config := DefaultTopologyCostConfig()
	nodeA := makeNode("node-a", "us-east-1", "us-east-1a")
	nodesMap := map[string]*v1.Node{
		"node-a": nodeA,
	}

	// dep pod on a node that is NOT in our map
	depPods := []*v1.Pod{
		makePod("dep-1", "default", "node-unknown", "group-1"),
	}

	cost := ComputePlacementCost(nodeA, depPods, nodesMap, config)
	if cost != config.CrossRegion {
		t.Errorf("Expected CrossRegion cost for unknown node, got %d", cost)
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

	// mock GetPodsAssignedToNodeFunc
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
			expectedCount: 2, // pod2 and pod3 (not pod1 itself)
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
	config := DefaultTopologyCostConfig()

	// Nodes:
	// node-a: us-east-1 / us-east-1a
	// node-b: us-east-1 / us-east-1b
	// node-c: eu-west-1 / eu-west-1a
	nodeA := makeNode("node-a", "us-east-1", "us-east-1a")
	nodeB := makeNode("node-b", "us-east-1", "us-east-1b")
	nodeC := makeNode("node-c", "eu-west-1", "eu-west-1a")

	nodesMap := map[string]*v1.Node{
		"node-a": nodeA,
		"node-b": nodeB,
		"node-c": nodeC,
	}

	allNodes := []*v1.Node{nodeA, nodeB, nodeC}

	// Pods:
	// pod-1 (group-1) on node-c (eu-west-1)
	// dep-1 (group-1) on node-a (us-east-1a)
	// dep-2 (group-1) on node-b (us-east-1b)
	pod1 := makePod("pod-1", "default", "node-c", "group-1")
	dep1 := makePod("dep-1", "default", "node-a", "group-1")
	dep2 := makePod("dep-2", "default", "node-b", "group-1")

	// pod-alone (group-alone) on node-a, no other pods in same group
	podAlone := makePod("pod-alone", "default", "node-a", "group-alone")

	// pod-no-label: no network-group label
	podNoLabel := makePod("pod-no-label", "default", "node-a", "")

	// pod-optimal (group-opt) on node-a, deps all in same zone
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
		name           string
		pod            *v1.Pod
		candidateNodes []*v1.Node
		expected       bool
	}{
		{
			name:           "pod without network-group label → allow",
			pod:            podNoLabel,
			candidateNodes: []*v1.Node{nodeB, nodeC},
			expected:       true,
		},
		{
			name:           "pod with no dependency pods → allow",
			pod:            podAlone,
			candidateNodes: []*v1.Node{nodeB, nodeC},
			expected:       true,
		},
		{
			name: "pod in eu-west, deps in us-east → candidate in us-east has lower cost → allow",
			pod:  pod1,
			// current cost: pod1 on node-c, dep1 on node-a = 10, dep2 on node-b = 10 → total 20
			// candidate node-a: dep1 on node-a = 0, dep2 on node-b = 5 → total 5 < 20
			candidateNodes: []*v1.Node{nodeA, nodeB},
			expected:       true,
		},
		{
			name: "pod already optimal, only worse candidates → block",
			pod:  podOptimal,
			// current cost: podOptimal on node-a, depOpt1 on node-a = 0 → total 0
			// candidate node-c: depOpt1 on node-a = 10 → total 10 > 0
			candidateNodes: []*v1.Node{nodeC},
			expected:       false,
		},
		{
			name:           "no candidate nodes → block",
			pod:            pod1,
			candidateNodes: []*v1.Node{},
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ShouldAllowEviction(
				tt.pod, DefaultNetworkGroupLabelKey, tt.candidateNodes,
				getPodsAssigned, allNodes, nodesMap, config,
			)
			if result != tt.expected {
				t.Errorf("ShouldAllowEviction() = %v, want %v", result, tt.expected)
			}
		})
	}
}
