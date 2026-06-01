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
	UsageModeRequests            = "requests"
	UsageModeActualRaw           = "actual-raw"
	UsageModeActualEWMA          = "actual-ewma"
	UsageModeActualEWMAPersisted = "actual-ewma-persisted"
	UsageModePublishedEWMA       = "published-ewma"
	publishedCPUAnnotation       = "descheduler.thesis/actual-cpu-milli"
	publishedMemAnnotation       = "descheduler.thesis/actual-memory-bytes"
	publishedTimeAnnotation      = "descheduler.thesis/timestamp"

	defaultConsolidationThreshold = 0.40
	defaultConsolidationTarget    = 0.90
)

// ResourceDefragmentation evicts pods so the kube-scheduler re-packs them onto a
// minimal set of balanced nodes.
//
// It assumes the cluster scheduler scores with MostAllocated + BalancedAllocation:
// an evicted pod is re-placed on the node that is both densest and most cpu:mem
// balanced after placement. Under that assumption a single objective — pack onto
// the fewest, most-balanced nodes — captures both consolidation (fewer active
// nodes, the MostAllocated half) and defragmentation (each kept node is balanced,
// low stranding, the BalancedAllocation half). The plugin therefore needs no
// separate "fragmentation" trigger: it empties under-utilized nodes and lets the
// combined bin score decide ordering and targets.
type ResourceDefragmentation struct {
	logger    klog.Logger
	handle    frameworktypes.Handle
	args      *ResourceDefragmentationArgs
	podFilter podutil.FilterFunc
}

var _ frameworktypes.BalancePlugin = &ResourceDefragmentation{}

type NodeResourceState struct {
	Node           *v1.Node
	AllocatableCPU int64
	AllocatableMem int64
	RequestedCPU   int64
	RequestedMem   int64
	UsedCPU        int64
	UsedMem        int64
}

// drainCandidate is an under-utilized node selected to be emptied, tagged with a
// priority so the worst nodes are drained first.
type drainCandidate struct {
	node *v1.Node
	pods []*v1.Pod
	// priority orders draining: pr = 0.5·|RII| + 0.5·(1/FSI). Higher is processed
	// first (most imbalanced and/or least free).
	priority float64
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

func (r *ResourceDefragmentation) consolidationThreshold() float64 {
	if r.args.ConsolidationThreshold > 0 {
		return r.args.ConsolidationThreshold
	}
	return defaultConsolidationThreshold
}

func (r *ResourceDefragmentation) consolidationTarget() float64 {
	if r.args.ConsolidationTarget > 0 {
		return r.args.ConsolidationTarget
	}
	return defaultConsolidationTarget
}

// Balance extension point implementation for the plugin.
func (r *ResourceDefragmentation) Balance(ctx context.Context, nodes []*v1.Node) *frameworktypes.Status {
	logger := klog.FromContext(klog.NewContext(ctx, r.logger)).WithValues("ExtensionPoint", frameworktypes.BalanceExtensionPoint)
	logger.V(1).Info("Starting resource defragmentation balance pass", "nodeCount", len(nodes))

	// Step 1: build the cluster resource state cache once (true cluster state,
	// unfiltered, so utilisation accounting is accurate).
	nodeStates := make(map[string]*NodeResourceState)
	for _, node := range nodes {
		if isControlPlaneNode(node) {
			logger.V(2).Info("Skipping control-plane node", "node", node.Name)
			continue
		}

		pods, err := podutil.ListPodsOnANode(node.Name, r.handle.GetPodsAssignedToNodeFunc(), nil)
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
		if allocCpu <= 0 || allocMem <= 0 {
			logger.V(2).Info("Skipping node with zero/negative allocatable resources", "node", node.Name)
			continue
		}

		usedCpu, usedMem, usageSource := r.getNodeUsage(ctx, node, reqCpu, reqMem)
		nodeStates[node.Name] = &NodeResourceState{
			Node:           node,
			AllocatableCPU: allocCpu,
			AllocatableMem: allocMem,
			RequestedCPU:   reqCpu,
			RequestedMem:   reqMem,
			UsedCPU:        usedCpu,
			UsedMem:        usedMem,
		}
		logger.V(2).Info("Node state", "node", node.Name, "avgUtilization", nodeAvgUtilization(nodeStates[node.Name]), "minUtilization", nodeMinUtilization(nodeStates[node.Name]), "binScore", nodeBinScore(usedCpu, usedMem, allocCpu, allocMem), "usageSource", usageSource)
	}

	// Step 2: build drain candidates. A worker node with evictable pods is a
	// candidate if EITHER trigger fires against consolidationThreshold:
	//   consolidation: average utilization is low (little total load), or
	//   defragmentation: bin score is low (a bad bin — skewed and/or sparse), the
	//                    MostAllocated+BalancedAllocation score on actual usage.
	// A node that is both well-utilized on average AND a good bin is kept.
	// Partial drains are allowed: a node whose big pod has no feasible target still
	// gets its smaller, relocatable pods moved toward denser, balanced bins.
	var candidates []drainCandidate
	for _, node := range nodes {
		state, ok := nodeStates[node.Name]
		if !ok {
			continue
		}
		threshold := r.consolidationThreshold()
		avgUtil := nodeAvgUtilization(state)
		binScore := nodeBinScore(state.UsedCPU, state.UsedMem, state.AllocatableCPU, state.AllocatableMem)
		if avgUtil >= threshold && binScore >= threshold {
			continue // well-utilized and a good bin → keep
		}
		pods, err := podutil.ListPodsOnANode(node.Name, r.handle.GetPodsAssignedToNodeFunc(), r.podFilter)
		if err != nil {
			logger.Error(err, "Error listing evictable pods on candidate", "node", node.Name)
			continue
		}
		if len(pods) == 0 {
			continue
		}
		imbalance := r.computeRII(state.AllocatableCPU, state.AllocatableMem, state.UsedCPU, state.UsedMem)
		fsi := r.computeFSI(state.AllocatableCPU, state.AllocatableMem, state.UsedCPU, state.UsedMem)
		candidates = append(candidates, drainCandidate{
			node:     node,
			pods:     pods,
			priority: r.computePriorityIndex(imbalance, fsi),
		})
		logger.V(1).Info("Node is a drain candidate", "node", node.Name, "avgUtilization", avgUtil, "binScore", binScore, "imbalanceIndex", imbalance, "freeSpaceIndex", fsi, "priority", candidates[len(candidates)-1].priority)
	}

	// Step 3: process the highest-priority node first. pr = 0.5·|RII| + 0.5·(1/FSI)
	// favours the most imbalanced and/or least-free under-utilized node.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].priority > candidates[j].priority
	})

	// Nodes that receive a relocated pod become bins and are never drained, which
	// prevents A->B then B->A ping-pong.
	bins := sets.New[string]()

	iteration := 0
	for i := range candidates {
		if iteration >= r.args.MaxEvictions {
			break
		}
		dc := candidates[i]
		if bins.Has(dc.node.Name) {
			logger.V(1).Info("Skipping node now serving as a bin", "node", dc.node.Name)
			continue
		}
		logger.V(1).Info("Draining under-utilized node", "node", dc.node.Name, "priority", dc.priority)

		remaining := append([]*v1.Pod(nil), dc.pods...)
		for iteration < r.args.MaxEvictions && len(remaining) > 0 {
			pod := r.topsis(ctx, dc.node, remaining, nodeStates)
			if pod == nil {
				logger.V(1).Info("TOPSIS returned no candidate, stopping node", "node", dc.node.Name)
				break
			}

			targetName, _ := r.predictSchedulerTarget(pod, dc.node.Name, nodeStates)
			if targetName == "" {
				// No feasible within-ceiling target for this pod: leave it in place
				// (partial drain) and try the next pod on this node.
				logger.V(1).Info("No feasible scheduler target within ceiling; leaving pod in place", "pod", klog.KObj(pod), "originNode", dc.node.Name)
				remaining = removePodFromList(remaining, pod)
				continue
			}
			logger.V(1).Info("Eviction decision", "pod", klog.KObj(pod), "originNode", dc.node.Name, "predictedTarget", targetName)

			err := r.handle.Evictor().Evict(ctx, pod, evictions.EvictOptions{StrategyName: PluginName})
			iteration++
			if err != nil {
				switch err.(type) {
				case *evictions.EvictionTotalLimitError:
					logger.V(1).Info("Total eviction limit reached, stopping")
					return nil
				case *evictions.EvictionNodeLimitError:
					logger.V(1).Info("Node eviction limit reached, moving to next node", "node", dc.node.Name)
					remaining = nil
					continue
				default:
					logger.Error(err, "Eviction failed", "pod", klog.KObj(pod))
					remaining = removePodFromList(remaining, pod)
					continue
				}
			}

			// Eviction succeeded — move the pod's footprint from source to the
			// predicted target so later feasibility checks see accurate capacity.
			evictedCpu, evictedMem := getPodRequests(pod)
			evictedUsedCpu, evictedUsedMem, _, usageErr := r.getPodUsage(ctx, pod)
			if usageErr != nil {
				logger.Error(usageErr, "Unable to read pod usage after eviction; falling back to requests", "pod", klog.KObj(pod))
				evictedUsedCpu, evictedUsedMem = evictedCpu, evictedMem
			}
			if s, ok := nodeStates[dc.node.Name]; ok {
				s.RequestedCPU -= evictedCpu
				s.RequestedMem -= evictedMem
				s.UsedCPU -= evictedUsedCpu
				s.UsedMem -= evictedUsedMem
			}
			if t, ok := nodeStates[targetName]; ok {
				t.RequestedCPU += evictedCpu
				t.RequestedMem += evictedMem
				t.UsedCPU += evictedUsedCpu
				t.UsedMem += evictedUsedMem
			}
			bins.Insert(targetName)
			remaining = removePodFromList(remaining, pod)
		}
	}

	return nil
}

func (r *ResourceDefragmentation) usageMode() string {
	switch r.args.UsageMode {
	case UsageModeRequests, UsageModeActualRaw, UsageModeActualEWMA, UsageModeActualEWMAPersisted, UsageModePublishedEWMA:
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
	case UsageModeActualEWMAPersisted:
		cpu, mem, err := r.persistedEWMANodeUsage(ctx, node)
		if err != nil {
			r.logger.Error(err, "Unable to calculate persisted EWMA node usage, falling back to requests", "node", node.Name)
			return reqCpu, reqMem, UsageModeRequests
		}
		return cpu, mem, UsageModeActualEWMAPersisted
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

func (r *ResourceDefragmentation) persistedEWMANodeUsage(ctx context.Context, node *v1.Node) (cpu int64, mem int64, err error) {
	if r.handle.MetricsCollector() == nil {
		return 0, 0, fmt.Errorf("metrics collector is unavailable")
	}
	nodeMetrics, err := r.handle.MetricsCollector().MetricsClient().MetricsV1beta1().NodeMetricses().Get(ctx, node.Name, metav1.GetOptions{})
	if err != nil {
		return 0, 0, err
	}
	rawCPU := nodeMetrics.Usage.Cpu().MilliValue()
	rawMem := nodeMetrics.Usage.Memory().Value()
	beta := r.args.EWMABeta
	if beta <= 0 || beta >= 1 {
		beta = 0.9
	}
	annotations := node.GetAnnotations()
	prevCPU, cpuErr := strconv.ParseInt(annotations[publishedCPUAnnotation], 10, 64)
	prevMem, memErr := strconv.ParseInt(annotations[publishedMemAnnotation], 10, 64)
	if cpuErr == nil && memErr == nil {
		cpu = int64(math.Round(beta*float64(prevCPU) + (1-beta)*float64(rawCPU)))
		mem = int64(math.Round(beta*float64(prevMem) + (1-beta)*float64(rawMem)))
	} else {
		cpu, mem = rawCPU, rawMem
	}
	copy := node.DeepCopy()
	if copy.Annotations == nil {
		copy.Annotations = map[string]string{}
	}
	copy.Annotations[publishedCPUAnnotation] = strconv.FormatInt(cpu, 10)
	copy.Annotations[publishedMemAnnotation] = strconv.FormatInt(mem, 10)
	copy.Annotations[publishedTimeAnnotation] = time.Now().UTC().Format(time.RFC3339)
	if _, err := r.handle.ClientSet().CoreV1().Nodes().Update(ctx, copy, metav1.UpdateOptions{}); err != nil {
		return 0, 0, err
	}
	return cpu, mem, nil
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

// nodeBinScore is the single "strategy" score modelling a MostAllocated +
// BalancedAllocation scheduler, computed from request fractions (the scheduler
// scores requests). Higher is a better bin.
//
//	density = (cpuFrac + memFrac) / 2     // MostAllocated: prefer fuller nodes
//	balance = 1 - |cpuFrac - memFrac|     // BalancedAllocation: prefer even cpu:mem
//	score   = (density + balance) / 2
func nodeBinScore(reqCpu, reqMem, allocCpu, allocMem int64) float64 {
	cpuFrac := float64(reqCpu) / float64(allocCpu)
	memFrac := float64(reqMem) / float64(allocMem)
	density := (cpuFrac + memFrac) / 2.0
	balance := 1.0 - math.Abs(cpuFrac-memFrac)
	return (density + balance) / 2.0
}

// nodeAvgUtilization returns the mean of the node's cpu and mem utilization from
// actual usage. It is the consolidation trigger: a low average means little total
// load, so the node is worth emptying.
func nodeAvgUtilization(state *NodeResourceState) float64 {
	cpu := float64(state.UsedCPU) / float64(state.AllocatableCPU)
	mem := float64(state.UsedMem) / float64(state.AllocatableMem)
	return (cpu + mem) / 2.0
}

// nodeMinUtilization returns the utilization of the node's least-used resource,
// min(cpuUtil, memUtil). It is used only for "pack upward": a pod is relocated
// onto a node whose least-used dimension is at least as loaded as the source's,
// so pods move toward fuller bins, never onto an emptier node. Using min (not max)
// admits the defragmenting direction — a pod on a lopsided node (one dimension
// ~full, min low) may move onto a more-balanced node (min higher).
func nodeMinUtilization(state *NodeResourceState) float64 {
	return math.Min(
		float64(state.UsedCPU)/float64(state.AllocatableCPU),
		float64(state.UsedMem)/float64(state.AllocatableMem),
	)
}

// removePodFromList returns pods with target removed (matched by namespace/name).
// It reuses the backing array, so callers must own the passed slice.
func removePodFromList(pods []*v1.Pod, target *v1.Pod) []*v1.Pod {
	out := pods[:0]
	for _, p := range pods {
		if p.Namespace != target.Namespace || p.Name != target.Name {
			out = append(out, p)
		}
	}
	return out
}

// predictSchedulerTarget simulates the MostAllocated + BalancedAllocation
// scheduler to predict which feasible node the pod would land on after eviction.
// A target must be schedulable, fit by requests, stay within the consolidation
// ceiling, and be at least as utilized as the source (pack upward, never relocate
// onto an emptier node). Among those, the node with the highest post-placement
// bin score wins. Returns "" if no feasible target exists.
func (r *ResourceDefragmentation) predictSchedulerTarget(pod *v1.Pod, sourceName string, nodeStates map[string]*NodeResourceState) (string, *NodeResourceState) {
	source, ok := nodeStates[sourceName]
	if !ok {
		return "", nil
	}
	podCpu, podMem := getPodRequests(pod)
	sourceUtil := nodeMinUtilization(source)
	ceiling := r.consolidationTarget()

	bestName := ""
	bestScore := -math.MaxFloat64
	var bestState *NodeResourceState
	for name, s := range nodeStates {
		if name == sourceName {
			continue
		}
		if !isPodSchedulableOnNode(pod, s.Node) {
			continue
		}
		if s.AllocatableCPU-s.RequestedCPU < podCpu || s.AllocatableMem-s.RequestedMem < podMem {
			continue
		}
		projCpu := float64(s.RequestedCPU+podCpu) / float64(s.AllocatableCPU)
		projMem := float64(s.RequestedMem+podMem) / float64(s.AllocatableMem)
		if math.Max(projCpu, projMem) > ceiling {
			continue
		}
		if nodeMinUtilization(s) < sourceUtil {
			continue
		}
		score := nodeBinScore(s.RequestedCPU+podCpu, s.RequestedMem+podMem, s.AllocatableCPU, s.AllocatableMem)
		if score > bestScore {
			bestScore = score
			bestName = name
			bestState = s
		}
	}
	return bestName, bestState
}

func isControlPlaneNode(node *v1.Node) bool {
	if node == nil {
		return false
	}
	if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
		return true
	}
	if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
		return true
	}
	for _, taint := range node.Spec.Taints {
		switch taint.Key {
		case "node-role.kubernetes.io/control-plane", "node-role.kubernetes.io/master":
			return true
		}
	}
	return false
}

func isPodSchedulableOnNode(pod *v1.Pod, node *v1.Node) bool {
	if node == nil || node.Spec.Unschedulable || isControlPlaneNode(node) {
		return false
	}

	for _, taint := range node.Spec.Taints {
		if taint.Effect != v1.TaintEffectNoSchedule && taint.Effect != v1.TaintEffectNoExecute {
			continue
		}
		if !podToleratesTaint(pod, taint) {
			return false
		}
	}

	return true
}

func podToleratesTaint(pod *v1.Pod, taint v1.Taint) bool {
	for _, toleration := range pod.Spec.Tolerations {
		if toleration.Effect != "" && toleration.Effect != taint.Effect {
			continue
		}
		if toleration.Key != taint.Key {
			continue
		}
		switch toleration.Operator {
		case v1.TolerationOpExists:
			return true
		case v1.TolerationOpEqual, "":
			if toleration.Value == taint.Value {
				return true
			}
		}
	}
	return false
}

// computeRII calculates the Resource Imbalance Index for a node: cpuFrac - memFrac.
func (r *ResourceDefragmentation) computeRII(allocCpu, allocMem, reqCpu, reqMem int64) float64 {
	rCPU := float64(reqCpu) / float64(allocCpu)
	rMem := float64(reqMem) / float64(allocMem)
	return rCPU - rMem
}

// computeFSI calculates the Free Space Index for a node: the product of its free
// cpu and free mem fractions. Low FSI means little usable free space left.
func (r *ResourceDefragmentation) computeFSI(allocCpu, allocMem, reqCpu, reqMem int64) float64 {
	c := float64(allocCpu-reqCpu) / float64(allocCpu)
	m := float64(allocMem-reqMem) / float64(allocMem)
	return c * m
}

// computePriorityIndex orders drain candidates: pr = wp·|RII| + (1-wp)·(1/FSI).
// A high value flags a node that is strongly imbalanced and/or nearly out of free
// space, so it is drained first.
func (r *ResourceDefragmentation) computePriorityIndex(imbalanceIndex, freeSpaceIndex float64) float64 {
	wp := 0.5
	return wp*math.Abs(imbalanceIndex) + (1-wp)*(1.0/(freeSpaceIndex+1e-10))
}

// computeC1 scores how much the pod contributes to the node's imbalance:
// nodeRII · podRII · maxResourceShare. After TOPSIS normalization the node-level
// magnitude cancels, so this effectively favours evicting the pod whose own skew
// matches the node's skew (the pod causing fragmentation), weighted by size.
// On a balanced node it is ~0 and stays out of the decision. This is the
// balanced-allocation (defrag) signal in the pod choice; C2/C3 carry the
// bin-packing signal.
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

// computeC2 scores the quality of the pod's predicted scheduler destination using
// the combined MostAllocated + BalancedAllocation bin score. A pod the scheduler
// would land on a dense, balanced node is a good pod to evict; if no feasible
// target exists it is penalised heavily so TOPSIS avoids it.
func (r *ResourceDefragmentation) computeC2(pod *v1.Pod, sourceName string, nodeStates map[string]*NodeResourceState) float64 {
	name, state := r.predictSchedulerTarget(pod, sourceName, nodeStates)
	if name == "" {
		return -999.9
	}
	podCpu, podMem := getPodRequests(pod)
	return nodeBinScore(state.RequestedCPU+podCpu, state.RequestedMem+podMem, state.AllocatableCPU, state.AllocatableMem)
}

// computeC3 scores the marginal free-space gain from evicting the pod: the delta
// of the node's Free Space Index, ΔFSI = (c+p_c)(m+p_m) − c·m = c·p_m + m·p_c +
// p_c·p_m, where c,m are the node's free cpu/mem fractions and p_c,p_m the pod's.
// Besides favouring larger pods (consolidation), it rewards evicting the pod that
// relieves the scarcer resource (defragmentation): on a memory-bound node the
// c·p_m term dominates for a memory-heavy pod.
func (r *ResourceDefragmentation) computeC3(ctx context.Context, pod *v1.Pod, source *NodeResourceState) float64 {
	allocCpu := float64(source.AllocatableCPU)
	allocMem := float64(source.AllocatableMem)

	c := float64(source.AllocatableCPU-source.UsedCPU) / allocCpu
	m := float64(source.AllocatableMem-source.UsedMem) / allocMem

	podCpu, podMem, _, err := r.getPodUsage(ctx, pod)
	if err != nil {
		r.logger.Error(err, "Unable to read pod usage for C3; falling back to requests", "pod", klog.KObj(pod))
		podCpu, podMem = getPodRequests(pod)
	}
	p_c := float64(podCpu) / allocCpu
	p_m := float64(podMem) / allocMem

	return (c * p_m) + (m * p_c) + (p_c * p_m)
}

// computeC4 prefers evicting low-priority pods (cost criterion).
func (r *ResourceDefragmentation) computeC4(pod *v1.Pod) float64 {
	if pod.Spec.Priority != nil {
		return float64(*pod.Spec.Priority)
	}
	return 0
}

// topsis selects the best pod to evict from a drain node using four criteria:
// C1 pod size (benefit), C2 predicted-destination bin score (benefit),
// C3 residual emptiness (benefit), C4 priority (cost).
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
			r.computeC2(pod, node.Name, nodeStates),
			r.computeC3(ctx, pod, currentState),
			r.computeC4(pod),
		}
		r.logger.V(2).Info("Candidate score inputs", "pod", klog.KObj(pod), "originNode", node.Name, "c1", matrix[i][0], "c2", matrix[i][1], "c3", matrix[i][2], "c4", matrix[i][3])
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
