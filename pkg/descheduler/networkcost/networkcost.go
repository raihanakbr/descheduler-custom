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


// computePlacementCost computes the total communication cost if a pod were
// placed on candidateNode, considering all its dependency pods.
func computePlacementCost(
	candidateNode *v1.Node,
	depPods []*v1.Pod,
	nodesMap map[string]*v1.Node,
	provider CostProvider,
) float64 {
	totalCost := 0.0
	for _, depPod := range depPods {
		depNode, ok := nodesMap[depPod.Spec.NodeName]
		if !ok {
			// dependency pod's node not found in our map, assume worst cost
			totalCost += 1.0
			continue
		}
		totalCost += provider.Cost(candidateNode, depNode)
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
// evicted based on network cost. It requires that at least minBetterPercent%
// of candidate nodes have strictly lower cost than the current node.
//
// This increases the probability that the scheduler (which we don't control)
// will place the evicted pod on a node with better network locality.
//
// The function returns true (allow eviction) when:
//   - The pod has no network-group label (opt-in only)
//   - No dependency pods are found
//   - At least minBetterPercent% of candidates have strictly lower cost
//
// It returns false (block eviction) when:
//   - Fewer than minBetterPercent% of candidates are strictly better
func ShouldAllowEviction(
	pod *v1.Pod,
	labelKey string,
	candidateNodes []*v1.Node,
	getPodsAssignedToNode podutil.GetPodsAssignedToNodeFunc,
	allNodes []*v1.Node,
	nodesMap map[string]*v1.Node,
	provider CostProvider,
	minBetterPercent int,
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
	currentCost := computePlacementCost(currentNode, depPods, nodesMap, provider)

	// count candidates with strictly lower cost
	betterCount := 0
	totalCandidates := 0
	for _, candidate := range candidateNodes {
		if candidate.Name == pod.Spec.NodeName {
			continue
		}
		totalCandidates++
		candidateCost := computePlacementCost(candidate, depPods, nodesMap, provider)
		if candidateCost <= currentCost {
			betterCount++
		}
	}

	if totalCandidates == 0 {
		klog.V(2).InfoS("No candidates available, blocking eviction",
			"pod", klog.KObj(pod))
		return false
	}

	// minimum floor of 1 to avoid requiring 0 nodes in small clusters
	minRequired := max(1, (totalCandidates*minBetterPercent)/100)

	if betterCount >= minRequired {
		klog.V(2).InfoS("Enough better candidates, allowing eviction",
			"pod", klog.KObj(pod),
			"betterCount", betterCount,
			"minRequired", minRequired,
			"totalCandidates", totalCandidates,
			"currentCost", currentCost,
		)
		return true
	}

	klog.V(2).InfoS("Not enough better candidates, blocking eviction",
		"pod", klog.KObj(pod),
		"betterCount", betterCount,
		"minRequired", minRequired,
		"totalCandidates", totalCandidates,
		"currentCost", currentCost,
	)
	return false
}
