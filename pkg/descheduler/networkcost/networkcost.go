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

// Package networkcost provides topology-distance-based cost computation
// for network-aware pod eviction decisions. It uses standard Kubernetes
// topology labels (topology.kubernetes.io/zone and topology.kubernetes.io/region)
// to estimate communication cost between pods on different nodes.
package networkcost

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
)

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
	SameZone int `json:"sameZone,omitempty"`

	// SameRegion is the cost between nodes in the same region but different zones.
	SameRegion int `json:"sameRegion,omitempty"`

	// CrossRegion is the cost between nodes in different regions.
	CrossRegion int `json:"crossRegion,omitempty"`
}

// DefaultTopologyCostConfig returns the default cost configuration.
func DefaultTopologyCostConfig() TopologyCostConfig {
	return TopologyCostConfig{
		SameZone:    1,
		SameRegion:  5,
		CrossRegion: 10,
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
func TopologyCost(nodeA, nodeB *v1.Node, config TopologyCostConfig) int {
	if nodeA.Name == nodeB.Name {
		return 0
	}

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

// ComputePlacementCost computes the total communication cost if a pod were
// placed on candidateNode, considering all its dependency pods. The cost is
// the sum of TopologyCost between candidateNode and each dependency pod's
// current node.
func ComputePlacementCost(
	candidateNode *v1.Node,
	depPods []*v1.Pod,
	nodesMap map[string]*v1.Node,
	config TopologyCostConfig,
) int {
	totalCost := 0
	for _, depPod := range depPods {
		depNode, ok := nodesMap[depPod.Spec.NodeName]
		if !ok {
			// dependency pod's node not found in our map, assume worst cost
			totalCost += config.CrossRegion
			continue
		}
		totalCost += TopologyCost(candidateNode, depNode, config)
	}
	return totalCost
}

// FindDependencyPods finds all pods that share the same network-group label
// value as the given pod. It searches across all provided nodes. The source
// pod itself is excluded from the result.
func FindDependencyPods(
	pod *v1.Pod,
	labelKey string,
	getPodsAssignedToNode podutil.GetPodsAssignedToNodeFunc,
	nodes []*v1.Node,
) []*v1.Pod {
	groupValue, exists := pod.Labels[labelKey]
	if !exists || groupValue == "" {
		return nil
	}

	var depPods []*v1.Pod
	for _, node := range nodes {
		podsOnNode, err := podutil.ListPodsOnANode(node.Name, getPodsAssignedToNode, nil)
		if err != nil {
			klog.V(4).InfoS("Error listing pods on node", "node", klog.KObj(node), "err", err)
			continue
		}
		klog.V(4).InfoS("Scanning node for dependency pods", "node", node.Name, "totalPods", len(podsOnNode), "group", groupValue)
		for _, p := range podsOnNode {
			// skip the source pod itself
			if p.UID == pod.UID {
				continue
			}
			if p.Labels[labelKey] == groupValue {
				depPods = append(depPods, p)
			}
		}
	}
	klog.V(2).InfoS("FindDependencyPods result", "pod", klog.KObj(pod), "group", groupValue, "depCount", len(depPods), "nodesScanned", len(nodes))
	return depPods
}

// ShouldAllowEviction determines whether a pod should be allowed to be
// evicted based on network cost. It returns true if at least one candidate
// node offers a lower communication cost than the pod's current placement.
//
// The function returns true (allow eviction) when:
//   - The pod has no network-group label (opt-in only)
//   - No dependency pods are found
//   - At least one candidate node has lower cost than current placement
//
// It returns false (block eviction) when:
//   - All candidate nodes would result in equal or higher network cost
func ShouldAllowEviction(
	pod *v1.Pod,
	labelKey string,
	candidateNodes []*v1.Node,
	getPodsAssignedToNode podutil.GetPodsAssignedToNodeFunc,
	allNodes []*v1.Node,
	nodesMap map[string]*v1.Node,
	config TopologyCostConfig,
) bool {
	// pods without the label are always allowed (opt-in)
	groupValue, exists := pod.Labels[labelKey]
	if !exists || groupValue == "" {
		klog.V(2).InfoS("ShouldAllowEviction: pod has no network-group label, allowing", "pod", klog.KObj(pod))
		return true
	}

	klog.V(1).InfoS("ShouldAllowEviction: evaluating pod", "pod", klog.KObj(pod), "group", groupValue, "candidateNodes", len(candidateNodes))

	// find all dependency pods with same group label
	depPods := FindDependencyPods(pod, labelKey, getPodsAssignedToNode, allNodes)
	if len(depPods) == 0 {
		klog.V(1).InfoS("ShouldAllowEviction: no dependency pods found, allowing", "pod", klog.KObj(pod), "group", groupValue)
		return true
	}
	klog.V(2).InfoS("ShouldAllowEviction: found dependency pods", "pod", klog.KObj(pod), "depCount", len(depPods))

	// compute cost at current placement
	currentNode, ok := nodesMap[pod.Spec.NodeName]
	if !ok {
		// can't find current node, allow eviction
		klog.V(4).InfoS("Current node not found in nodes map, allowing eviction",
			"pod", klog.KObj(pod), "nodeName", pod.Spec.NodeName)
		return true
	}
	currentCost := ComputePlacementCost(currentNode, depPods, nodesMap, config)

	// check if any candidate node offers lower cost
	for _, candidate := range candidateNodes {
		// skip the pod's current node
		if candidate.Name == pod.Spec.NodeName {
			continue
		}
		candidateCost := ComputePlacementCost(candidate, depPods, nodesMap, config)
		if candidateCost <= currentCost {
			klog.V(4).InfoS("Found candidate with lower network cost",
				"pod", klog.KObj(pod),
				"currentNode", pod.Spec.NodeName,
				"currentCost", currentCost,
				"candidateNode", candidate.Name,
				"candidateCost", candidateCost,
			)
			return true
		}
	}

	klog.V(3).InfoS("No candidate node offers lower network cost, blocking eviction",
		"pod", klog.KObj(pod),
		"currentNode", pod.Spec.NodeName,
		"currentCost", currentCost,
		"candidateCount", len(candidateNodes),
	)
	return false
}
