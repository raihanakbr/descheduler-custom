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
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"sigs.k8s.io/descheduler/pkg/descheduler/evictions"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
)

const PluginName = "ResourceDefragmentation"

const (
	UsageModeRequests       = "requests"
	UsageModeActualRaw      = "actual-raw"
	UsageModeActualEWMA     = "actual-ewma"
	UsageModePublishedEWMA  = "published-ewma"
	publishedCPUAnnotation  = "descheduler.thesis/actual-cpu-milli"
	publishedMemAnnotation  = "descheduler.thesis/actual-memory-bytes"
	publishedTimeAnnotation = "descheduler.thesis/timestamp"
)

// ResourceDefragmentation evicts pods to defragment resource usage across nodes.
type ResourceDefragmentation struct {
	logger    klog.Logger
	handle    frameworktypes.Handle
	args      *ResourceDefragmentationArgs
	podFilter podutil.FilterFunc
}

var _ frameworktypes.BalancePlugin = &ResourceDefragmentation{}

type NodeResourceState struct {
	AllocatableCPU int64
	AllocatableMem int64
	RequestedCPU   int64
	RequestedMem   int64
	UsedCPU        int64
	UsedMem        int64
}

type fragmentedNode struct {
	node           *v1.Node
	pods           []*v1.Pod
	imbalanceIndex float64
	freeSpaceIndex float64
	priorityIndex  float64
}

// New builds the plugin from its arguments while passing a handle.
func New(ctx context.Context, args runtime.Object, handle frameworktypes.Handle) (frameworktypes.Plugin, error) {
	resourceDefragmentationArgs, ok := args.(*ResourceDefragmentationArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type ResourceDefragmentationArgs, got %T", args)
	}
	logger := klog.FromContext(ctx).WithValues("plugin", PluginName)

	var includedNamespaces, excludedNamespaces sets.Set[string]
	if resourceDefragmentationArgs.Namespaces != nil {
		includedNamespaces = sets.New(resourceDefragmentationArgs.Namespaces.Include...)
		excludedNamespaces = sets.New(resourceDefragmentationArgs.Namespaces.Exclude...)
	}

	podFilter, err := podutil.NewOptions().
		WithFilter(podutil.WrapFilterFuncs(handle.Evictor().Filter, handle.Evictor().PreEvictionFilter)).
		WithNamespaces(includedNamespaces).
		WithoutNamespaces(excludedNamespaces).
		BuildFilterFunc()
	if err != nil {
		return nil, fmt.Errorf("error initializing pod filter function: %v", err)
	}

	return &ResourceDefragmentation{
		logger:    logger,
		handle:    handle,
		args:      resourceDefragmentationArgs,
		podFilter: podFilter,
	}, nil
}

// Name retrieves the plugin name.
func (r *ResourceDefragmentation) Name() string {
	return PluginName
}

// Balance extension point implementation for the plugin.
func (r *ResourceDefragmentation) Balance(ctx context.Context, nodes []*v1.Node) *frameworktypes.Status {
	logger := klog.FromContext(klog.NewContext(ctx, r.logger)).WithValues("ExtensionPoint", frameworktypes.BalanceExtensionPoint)
	logger.V(1).Info("Starting resource defragmentation balance pass", "nodeCount", len(nodes))

	nodeStates := make(map[string]*NodeResourceState)
	var fragmentedNodes []fragmentedNode

	// Step 1: Build the cluster resource state cache once
	for _, node := range nodes {
		pods, err := podutil.ListPodsOnANode(node.Name, r.handle.GetPodsAssignedToNodeFunc(), r.podFilter)
		if err != nil {
			logger.Error(err, "Error listing pods on node", "node", node.Name)
			continue
		}

		var reqCpu, reqMem int64
		for _, pod := range pods {
			c, m := getPodRequests(pod)
			reqCpu += c
			reqMem += m
		}

		allocCpu := node.Status.Allocatable.Cpu().MilliValue()
		allocMem := node.Status.Allocatable.Memory().Value()

		// Protect against division by zero for misconfigured nodes
		if allocCpu <= 0 || allocMem <= 0 {
			logger.V(2).Info("Skipping node with zero/negative allocatable resources", "node", node.Name)
			continue
		}

		usedCpu, usedMem, usageSource := r.getNodeUsage(ctx, node, reqCpu, reqMem)

		nodeStates[node.Name] = &NodeResourceState{
			AllocatableCPU: allocCpu,
			AllocatableMem: allocMem,
			RequestedCPU:   reqCpu,
			RequestedMem:   reqMem,
			UsedCPU:        usedCpu,
			UsedMem:        usedMem,
		}

		imbalanceIndex := r.computeRII(allocCpu, allocMem, usedCpu, usedMem)
		logger.V(2).Info("Node imbalance index", "node", node.Name, "imbalanceIndex", imbalanceIndex, "usageSource", usageSource)

		if math.Abs(imbalanceIndex) > r.args.ImbalanceThreshold {
			logger.V(1).Info("Node is considered fragmented", "node", node.Name, "imbalanceIndex", imbalanceIndex, "usageSource", usageSource)

			fsi := r.computeFSI(allocCpu, allocMem, usedCpu, usedMem)
			fn := fragmentedNode{
				node:           node,
				pods:           pods,
				imbalanceIndex: imbalanceIndex,
				freeSpaceIndex: fsi,
			}
			fn.priorityIndex = r.computePriorityIndex(&fn)
			fragmentedNodes = append(fragmentedNodes, fn)
		}
	}

	// Process worst-fragmented nodes first.
	sort.Slice(fragmentedNodes, func(i, j int) bool {
		return fragmentedNodes[i].priorityIndex > fragmentedNodes[j].priorityIndex
	})

	iteration := 0
	idx := 0
	for idx < len(fragmentedNodes) && iteration < r.args.MaxEvictions {
		fn := fragmentedNodes[idx]
		logger.V(1).Info("Attempting eviction on fragmented node", "node", fn.node.Name, "imbalanceIndex", fn.imbalanceIndex, "iteration", iteration+1)

		pod := r.topsis(ctx, fn.node, fn.pods, nodeStates)
		if pod == nil {
			logger.V(1).Info("TOPSIS returned no candidate, skipping node", "node", fn.node.Name, "skippedReason", "no-feasible-target-or-no-candidate")
			idx++
			continue
		}

		decision := r.evaluateFeasibleTargets(ctx, pod, fn.node.Name, nodeStates)
		logger.V(1).Info("Resource defragmentation eviction decision", "pod", klog.KObj(pod), "originNode", fn.node.Name, "feasibleTargets", decision.targetNames, "bestProjectedScoreImprovement", decision.bestImprovement, "evict", decision.canEvict)
		if !decision.canEvict {
			logger.V(1).Info("Skipping candidate before eviction", "pod", klog.KObj(pod), "originNode", fn.node.Name, "skippedReason", decision.reason, "feasibleTargets", decision.targetNames)
			idx++
			continue
		}

		err := r.handle.Evictor().Evict(ctx, pod, evictions.EvictOptions{StrategyName: PluginName})
		iteration++

		if err != nil {
			switch err.(type) {
			case *evictions.EvictionTotalLimitError:
				logger.V(1).Info("Total eviction limit reached, stopping")
				return nil
			case *evictions.EvictionNodeLimitError:
				logger.V(1).Info("Node eviction limit reached, skipping node", "node", fn.node.Name)
				idx++
				continue
			default:
				logger.Error(err, "Eviction failed", "pod", klog.KObj(pod))
				idx++
				continue
			}
		}

		// Eviction succeeded — update our caches and re-evaluate this node.
		// TODO(thesis): add a post-reschedule observation hook for moved/returned/pending/latency.
		evictedCpu, evictedMem := getPodRequests(pod)
		evictedUsedCpu, evictedUsedMem, _, usageErr := r.getPodUsage(ctx, pod)
		if usageErr != nil {
			logger.Error(usageErr, "Unable to read pod usage after eviction; falling back to requests for cache update", "pod", klog.KObj(pod))
			evictedUsedCpu, evictedUsedMem = evictedCpu, evictedMem
		}

		// Update the global node state map dynamically
		if state, exists := nodeStates[fn.node.Name]; exists {
			state.RequestedCPU -= evictedCpu
			state.RequestedMem -= evictedMem
			state.UsedCPU -= evictedUsedCpu
			state.UsedMem -= evictedUsedMem
		}

		// Explicitly exclude the evicted pod from the current node's pod list
		filtered := fn.pods[:0]
		for _, p := range fn.pods {
			if p.Namespace != pod.Namespace || p.Name != pod.Name {
				filtered = append(filtered, p)
			}
		}
		updatedPods := filtered

		// Recalculate node imbalance based on updated cache
		currentState := nodeStates[fn.node.Name]
		newImbalance := r.computeRII(currentState.AllocatableCPU, currentState.AllocatableMem, currentState.UsedCPU, currentState.UsedMem)

		if math.Abs(newImbalance) <= r.args.ImbalanceThreshold {
			logger.V(1).Info("Node no longer fragmented, removing from list", "node", fn.node.Name)
			fragmentedNodes = append(fragmentedNodes[:idx], fragmentedNodes[idx+1:]...)
			// Do not increment idx — the next element has shifted into position idx.
		} else {
			fragmentedNodes[idx].pods = updatedPods
			fragmentedNodes[idx].imbalanceIndex = newImbalance
			idx++
			if idx >= len(fragmentedNodes) {
				idx = 0 // wrap around to revisit nodes still above threshold
			}
		}
	}

	return nil
}

func (r *ResourceDefragmentation) usageMode() string {
	switch r.args.UsageMode {
	case UsageModeRequests, UsageModeActualRaw, UsageModeActualEWMA, UsageModePublishedEWMA:
		return r.args.UsageMode
	default:
		if r.handle.MetricsCollector() != nil {
			return UsageModeActualEWMA
		}
		return UsageModeRequests
	}
}

func (r *ResourceDefragmentation) getNodeUsage(ctx context.Context, node *v1.Node, reqCpu, reqMem int64) (cpu int64, mem int64, source string) {
	switch r.usageMode() {
	case UsageModeRequests:
		return reqCpu, reqMem, UsageModeRequests
	case UsageModeActualRaw:
		if r.handle.MetricsCollector() == nil {
			r.logger.V(1).Info("Metrics collector is unavailable, falling back to requests", "node", node.Name, "usageMode", UsageModeActualRaw)
			return reqCpu, reqMem, UsageModeRequests
		}
		nodeMetrics, err := r.handle.MetricsCollector().MetricsClient().MetricsV1beta1().NodeMetricses().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			r.logger.Error(err, "Unable to read raw metrics-server node usage, falling back to requests", "node", node.Name)
			return reqCpu, reqMem, UsageModeRequests
		}
		return nodeMetrics.Usage.Cpu().MilliValue(), nodeMetrics.Usage.Memory().Value(), UsageModeActualRaw
	case UsageModePublishedEWMA:
		cpu, mem, err := r.publishedNodeUsage(node)
		if err != nil {
			r.logger.Error(err, "Unable to read published node usage, falling back to requests", "node", node.Name)
			return reqCpu, reqMem, UsageModeRequests
		}
		return cpu, mem, UsageModePublishedEWMA
	case UsageModeActualEWMA:
		if r.handle.MetricsCollector() == nil {
			r.logger.V(1).Info("Metrics collector is unavailable, falling back to requests", "node", node.Name, "usageMode", UsageModeActualEWMA)
			return reqCpu, reqMem, UsageModeRequests
		}
		if !r.handle.MetricsCollector().HasSynced() {
			if err := r.handle.MetricsCollector().Collect(ctx); err != nil {
				r.logger.Error(err, "Unable to collect metrics-server node usage, falling back to requests", "node", node.Name)
				return reqCpu, reqMem, UsageModeRequests
			}
		}
		if nodeUsage, err := r.handle.MetricsCollector().NodeUsage(node); err == nil {
			return nodeUsage[v1.ResourceCPU].MilliValue(), nodeUsage[v1.ResourceMemory].Value(), UsageModeActualEWMA
		} else {
			r.logger.Error(err, "Unable to read metrics-server node usage, falling back to requests", "node", node.Name)
		}
	}
	return reqCpu, reqMem, UsageModeRequests
}

func (r *ResourceDefragmentation) publishedNodeUsage(node *v1.Node) (cpu int64, mem int64, err error) {
	annotations := node.GetAnnotations()
	if annotations == nil {
		return 0, 0, fmt.Errorf("node has no published usage annotations")
	}
	if maxAge := r.args.PublishedUsageMaxAgeSeconds; maxAge > 0 {
		timestamp := annotations[publishedTimeAnnotation]
		if timestamp == "" {
			return 0, 0, fmt.Errorf("published usage timestamp annotation %q is missing", publishedTimeAnnotation)
		}
		publishedAt, parseErr := time.Parse(time.RFC3339, timestamp)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("parse published usage timestamp: %w", parseErr)
		}
		if time.Since(publishedAt) > time.Duration(maxAge)*time.Second {
			return 0, 0, fmt.Errorf("published usage is stale: age %s exceeds %ds", time.Since(publishedAt).Round(time.Second), maxAge)
		}
	}
	cpu, err = strconv.ParseInt(annotations[publishedCPUAnnotation], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse %s: %w", publishedCPUAnnotation, err)
	}
	mem, err = strconv.ParseInt(annotations[publishedMemAnnotation], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("parse %s: %w", publishedMemAnnotation, err)
	}
	return cpu, mem, nil
}

// getPodRequests is a helper to sum a pod's requested resources
func getPodRequests(pod *v1.Pod) (cpu int64, mem int64) {
	for _, c := range pod.Spec.Containers {
		cpu += c.Resources.Requests.Cpu().MilliValue()
		mem += c.Resources.Requests.Memory().Value()
	}
	return cpu, mem
}

func (r *ResourceDefragmentation) getPodUsage(ctx context.Context, pod *v1.Pod) (cpu int64, mem int64, source string, err error) {
	if r.usageMode() == UsageModeRequests || r.handle.MetricsCollector() == nil {
		cpu, mem = getPodRequests(pod)
		return cpu, mem, UsageModeRequests, nil
	}

	podMetrics, err := r.handle.MetricsCollector().MetricsClient().MetricsV1beta1().PodMetricses(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return 0, 0, "metrics-server", err
	}

	for _, container := range podMetrics.Containers {
		cpu += container.Usage.Cpu().MilliValue()
		mem += container.Usage.Memory().Value()
	}
	return cpu, mem, r.usageMode(), nil
}

// computeRII calculates the Resource Imbalance Index for a node purely mathematically
func (r *ResourceDefragmentation) computeRII(allocCpu, allocMem, reqCpu, reqMem int64) float64 {
	rCPU := float64(reqCpu) / float64(allocCpu)
	rMem := float64(reqMem) / float64(allocMem)
	return rCPU - rMem
}

// computeFSI calculates the Free Space Index purely mathematically
func (r *ResourceDefragmentation) computeFSI(allocCpu, allocMem, reqCpu, reqMem int64) float64 {
	c := float64(allocCpu-reqCpu) / float64(allocCpu)
	m := float64(allocMem-reqMem) / float64(allocMem)
	return c * m
}

func (r *ResourceDefragmentation) computePriorityIndex(node *fragmentedNode) float64 {
	wp := 0.5
	return wp*math.Abs(node.imbalanceIndex) + (1-wp)*(1/(node.freeSpaceIndex+1e-10))
}

func (r *ResourceDefragmentation) computeC1(ctx context.Context, nodeRII float64, candidatePod *v1.Pod, allocCpu, allocMem int64) float64 {
	podCpu, podMem, _, err := r.getPodUsage(ctx, candidatePod)
	if err != nil {
		r.logger.Error(err, "Unable to read pod usage for C1; falling back to requests", "pod", klog.KObj(candidatePod))
		podCpu, podMem = getPodRequests(candidatePod)
	}

	podCpuRatio := float64(podCpu) / float64(allocCpu)
	podMemRatio := float64(podMem) / float64(allocMem)

	podRII := podCpuRatio - podMemRatio
	maxResourceShare := math.Max(podCpuRatio, podMemRatio)

	return nodeRII * podRII * maxResourceShare
}

type targetFeasibilityDecision struct {
	canEvict        bool
	reason          string
	targetNames     []string
	bestImprovement float64
}

// evaluateFeasibleTargets is the pre-eviction safety guard for the default scheduler:
// a target must be non-origin, fit by Kubernetes resource requests, and improve the
// real-usage-aware projected imbalance score. We do not mutate affinity/nodeName;
// this only prevents evictions that would likely become Pending under request-based scheduling.
func (r *ResourceDefragmentation) evaluateFeasibleTargets(ctx context.Context, candidatePod *v1.Pod, currentNodeName string, nodeStates map[string]*NodeResourceState) targetFeasibilityDecision {
	decision := targetFeasibilityDecision{reason: "no-non-origin-request-feasible-target", bestImprovement: -1.0}
	podCpu, podMem := getPodRequests(candidatePod)
	podUsedCpu, podUsedMem, usageSource, err := r.getPodUsage(ctx, candidatePod)
	if err != nil {
		r.logger.Error(err, "Unable to read pod usage for feasibility guard; falling back to requests", "pod", klog.KObj(candidatePod))
		podUsedCpu, podUsedMem = podCpu, podMem
		usageSource = "requests-fallback"
	}

	originState, ok := nodeStates[currentNodeName]
	if !ok {
		decision.reason = "missing-origin-state"
		return decision
	}
	originBefore := math.Abs(r.computeRII(originState.AllocatableCPU, originState.AllocatableMem, originState.UsedCPU, originState.UsedMem))
	originAfter := math.Abs(r.computeRII(originState.AllocatableCPU, originState.AllocatableMem, originState.UsedCPU-podUsedCpu, originState.UsedMem-podUsedMem))

	for nodeName, state := range nodeStates {
		if nodeName == currentNodeName {
			continue
		}

		freeCpu := state.AllocatableCPU - state.RequestedCPU
		freeMem := state.AllocatableMem - state.RequestedMem

		if freeCpu < podCpu || freeMem < podMem {
			continue
		}

		decision.targetNames = append(decision.targetNames, nodeName)
		targetBefore := math.Abs(r.computeRII(state.AllocatableCPU, state.AllocatableMem, state.UsedCPU, state.UsedMem))
		targetAfter := math.Abs(r.computeRII(state.AllocatableCPU, state.AllocatableMem, state.UsedCPU+podUsedCpu, state.UsedMem+podUsedMem))
		improvement := (originBefore + targetBefore) - (originAfter + targetAfter)
		if improvement > decision.bestImprovement {
			decision.bestImprovement = improvement
		}
	}

	if len(decision.targetNames) == 0 {
		return decision
	}
	decision.reason = "no-positive-projected-score-improvement"
	if decision.bestImprovement > 0 {
		decision.canEvict = true
		decision.reason = "request-feasible-and-score-improves"
	}
	r.logger.V(2).Info("Evaluated feasible targets", "pod", klog.KObj(candidatePod), "originNode", currentNodeName, "usageSource", usageSource, "feasibleTargets", decision.targetNames, "bestProjectedScoreImprovement", decision.bestImprovement, "decision", decision.reason)
	return decision
}

// computeC2 scores the best feasible migration target.
func (r *ResourceDefragmentation) computeC2(ctx context.Context, candidatePod *v1.Pod, currentNodeName string, nodeStates map[string]*NodeResourceState) float64 {
	decision := r.evaluateFeasibleTargets(ctx, candidatePod, currentNodeName, nodeStates)
	if !decision.canEvict {
		return -999.9
	}
	return decision.bestImprovement
}

func (r *ResourceDefragmentation) computeC3(ctx context.Context, candidatePod *v1.Pod, currentState *NodeResourceState) float64 {
	allocCpu := float64(currentState.AllocatableCPU)
	allocMem := float64(currentState.AllocatableMem)

	c := float64(currentState.AllocatableCPU-currentState.UsedCPU) / allocCpu
	m := float64(currentState.AllocatableMem-currentState.UsedMem) / allocMem

	podCpu, podMem, _, err := r.getPodUsage(ctx, candidatePod)
	if err != nil {
		r.logger.Error(err, "Unable to read pod usage for C3; falling back to requests", "pod", klog.KObj(candidatePod))
		podCpu, podMem = getPodRequests(candidatePod)
	}
	p_c := float64(podCpu) / allocCpu
	p_m := float64(podMem) / allocMem

	return (c * p_m) + (m * p_c) + (p_c * p_m)
}

func (r *ResourceDefragmentation) computeC4(candidatePod *v1.Pod) float64 {
	if candidatePod.Spec.Priority != nil {
		return float64(*candidatePod.Spec.Priority)
	}
	return 0
}

func (r *ResourceDefragmentation) topsis(ctx context.Context, node *v1.Node, pods []*v1.Pod, nodeStates map[string]*NodeResourceState) *v1.Pod {
	if len(pods) == 0 {
		return nil
	}

	weights := []float64{0.30, 0.30, 0.25, 0.15}
	isBenefit := []bool{true, true, true, false}

	nPods := len(pods)
	nCriteria := len(weights)

	currentState := nodeStates[node.Name]
	currentNodeRII := r.computeRII(currentState.AllocatableCPU, currentState.AllocatableMem, currentState.UsedCPU, currentState.UsedMem)

	matrix := make([][]float64, nPods)
	for i, pod := range pods {
		matrix[i] = []float64{
			r.computeC1(ctx, currentNodeRII, pod, currentState.AllocatableCPU, currentState.AllocatableMem),
			r.computeC2(ctx, pod, node.Name, nodeStates),
			r.computeC3(ctx, pod, currentState),
			r.computeC4(pod),
		}
		r.logger.V(2).Info("Resource defragmentation candidate score inputs", "pod", klog.KObj(pod), "originNode", node.Name, "c1", matrix[i][0], "c2", matrix[i][1], "c3", matrix[i][2], "c4", matrix[i][3])
	}

	// Step 1: Normalize the decision matrix (vector normalization)
	normalizedMatrix := make([][]float64, nPods)
	for i := 0; i < nPods; i++ {
		normalizedMatrix[i] = make([]float64, nCriteria)
	}
	for j := 0; j < nCriteria; j++ {
		var sumSquares float64
		for i := 0; i < nPods; i++ {
			sumSquares += matrix[i][j] * matrix[i][j]
		}
		normFactor := math.Sqrt(sumSquares)
		for i := 0; i < nPods; i++ {
			if normFactor == 0 {
				normalizedMatrix[i][j] = 0
			} else {
				normalizedMatrix[i][j] = matrix[i][j] / normFactor
			}
		}
	}

	// Step 2: Apply weights to the normalized matrix
	weightedMatrix := make([][]float64, nPods)
	for i := 0; i < nPods; i++ {
		weightedMatrix[i] = make([]float64, nCriteria)
		for j := 0; j < nCriteria; j++ {
			weightedMatrix[i][j] = normalizedMatrix[i][j] * weights[j]
		}
	}

	// Step 3: Determine ideal best and ideal worst solutions
	idealBest := make([]float64, nCriteria)
	idealWorst := make([]float64, nCriteria)
	for j := 0; j < nCriteria; j++ {
		idealBest[j] = weightedMatrix[0][j]
		idealWorst[j] = weightedMatrix[0][j]
		for i := 1; i < nPods; i++ {
			if isBenefit[j] {
				if weightedMatrix[i][j] > idealBest[j] {
					idealBest[j] = weightedMatrix[i][j]
				}
				if weightedMatrix[i][j] < idealWorst[j] {
					idealWorst[j] = weightedMatrix[i][j]
				}
			} else {
				if weightedMatrix[i][j] < idealBest[j] {
					idealBest[j] = weightedMatrix[i][j]
				}
				if weightedMatrix[i][j] > idealWorst[j] {
					idealWorst[j] = weightedMatrix[i][j]
				}
			}
		}
	}

	// Step 4: Calculate separation measures
	dPlus := make([]float64, nPods)
	dMinus := make([]float64, nPods)
	for i := 0; i < nPods; i++ {
		for j := 0; j < nCriteria; j++ {
			dPlus[i] += math.Pow(weightedMatrix[i][j]-idealBest[j], 2)
			dMinus[i] += math.Pow(weightedMatrix[i][j]-idealWorst[j], 2)
		}
		dPlus[i] = math.Sqrt(dPlus[i])
		dMinus[i] = math.Sqrt(dMinus[i])
	}

	// Step 5: Calculate relative closeness and select the best eviction candidate.
	bestCC := -1.0
	bestIdx := -1
	for i := 0; i < nPods; i++ {
		denom := dPlus[i] + dMinus[i]
		var cc float64
		if denom == 0 {
			cc = 0.5 // equidistant — treat as a valid candidate
		} else {
			cc = dMinus[i] / denom
		}
		if cc > bestCC {
			bestCC = cc
			bestIdx = i
		}
	}

	if bestIdx == -1 {
		return nil
	}
	return pods[bestIdx]
}
