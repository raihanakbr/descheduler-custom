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

// Package resourcedefragmentationc2 is a single-criterion sibling of the
// ResourceDefragmentation plugin. It keeps the same consolidation/defragmentation
// pipeline (build node state, pick under-utilized/imbalanced drain candidates,
// drain worst-first, partial drain, anti-churn bins) but makes two deliberate
// simplifications so it can be compared against the multi-criteria plugin:
//
//   - No TOPSIS. The pod to evict is chosen by the single C2 criterion: evict the
//     pod whose predicted landing node has the best cpu:mem bin score (its best
//     achievable balance), aligned with the stranding objective
//     S = Σ|cpuFrac − memFrac|. This is exactly the TOPSIS plugin's `just-c2`
//     selector, standalone.
//   - Real usage. Node utilisation is read from actual metrics-server usage by
//     default, and the landing node is predicted with the same lightweight
//     MostAllocated + BalancedAllocation bin score the TOPSIS plugin uses.
package resourcedefragmentationc2

import (
	"context"
	"fmt"
	"math"
	"sort"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"sigs.k8s.io/descheduler/pkg/descheduler/evictions"
	podutil "sigs.k8s.io/descheduler/pkg/descheduler/pod"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
)

const PluginName = "ResourceDefragmentationC2"

const (
	UsageModeRequests   = "requests"
	UsageModeActualRaw  = "actual-raw"
	UsageModeActualEWMA = "actual-ewma"

	defaultConsolidationThreshold = 0.40
	defaultConsolidationTarget    = 0.90
)

type ResourceDefragmentationC2 struct {
	logger    klog.Logger
	handle    frameworktypes.Handle
	args      *ResourceDefragmentationC2Args
	podFilter podutil.FilterFunc
}

var _ frameworktypes.BalancePlugin = &ResourceDefragmentationC2{}

type nodeState struct {
	node           *v1.Node
	allocatableCPU int64
	allocatableMem int64
	requestedCPU   int64
	requestedMem   int64
	usedCPU        int64
	usedMem        int64
}

type drainCandidate struct {
	node     *v1.Node
	pods     []*v1.Pod
	priority float64
}

// New builds the plugin from its arguments while passing a handle.
func New(ctx context.Context, args runtime.Object, handle frameworktypes.Handle) (frameworktypes.Plugin, error) {
	a, ok := args.(*ResourceDefragmentationC2Args)
	if !ok {
		return nil, fmt.Errorf("want args to be of type ResourceDefragmentationC2Args, got %T", args)
	}
	logger := klog.FromContext(ctx).WithValues("plugin", PluginName)

	var included, excluded sets.Set[string]
	if a.Namespaces != nil {
		included = sets.New(a.Namespaces.Include...)
		excluded = sets.New(a.Namespaces.Exclude...)
	}
	podFilter, err := podutil.NewOptions().
		WithFilter(podutil.WrapFilterFuncs(handle.Evictor().Filter, handle.Evictor().PreEvictionFilter)).
		WithNamespaces(included).
		WithoutNamespaces(excluded).
		BuildFilterFunc()
	if err != nil {
		return nil, fmt.Errorf("error initializing pod filter function: %v", err)
	}
	return &ResourceDefragmentationC2{logger: logger, handle: handle, args: a, podFilter: podFilter}, nil
}

func (r *ResourceDefragmentationC2) Name() string { return PluginName }

func (r *ResourceDefragmentationC2) consolidationThreshold() float64 {
	if r.args.ConsolidationThreshold > 0 {
		return r.args.ConsolidationThreshold
	}
	return defaultConsolidationThreshold
}

func (r *ResourceDefragmentationC2) consolidationTarget() float64 {
	if r.args.ConsolidationTarget > 0 {
		return r.args.ConsolidationTarget
	}
	return defaultConsolidationTarget
}

// Balance extension point implementation.
func (r *ResourceDefragmentationC2) Balance(ctx context.Context, nodes []*v1.Node) *frameworktypes.Status {
	logger := klog.FromContext(klog.NewContext(ctx, r.logger)).WithValues("ExtensionPoint", frameworktypes.BalanceExtensionPoint)
	logger.V(1).Info("Starting ResourceDefragmentationC2 balance pass", "nodeCount", len(nodes))

	// Step 1: cluster resource state (real usage).
	states := make(map[string]*nodeState)
	for _, node := range nodes {
		if isControlPlaneNode(node) {
			continue
		}
		pods, err := podutil.ListPodsOnANode(node.Name, r.handle.GetPodsAssignedToNodeFunc(), nil)
		if err != nil {
			logger.Error(err, "Error listing pods on node", "node", node.Name)
			continue
		}
		var reqCpu, reqMem int64
		for _, p := range pods {
			c, m := getPodRequests(p)
			reqCpu += c
			reqMem += m
		}
		allocCpu := node.Status.Allocatable.Cpu().MilliValue()
		allocMem := node.Status.Allocatable.Memory().Value()
		if allocCpu <= 0 || allocMem <= 0 {
			continue
		}
		usedCpu, usedMem := r.nodeUsage(ctx, node, reqCpu, reqMem)
		states[node.Name] = &nodeState{
			node: node, allocatableCPU: allocCpu, allocatableMem: allocMem,
			requestedCPU: reqCpu, requestedMem: reqMem, usedCPU: usedCpu, usedMem: usedMem,
		}
	}

	// Step 2: drain candidates = under-utilized OR imbalanced (bad-bin) worker
	// nodes with evictable pods. Partial drains allowed.
	var candidates []drainCandidate
	for _, node := range nodes {
		s, ok := states[node.Name]
		if !ok {
			continue
		}
		threshold := r.consolidationThreshold()
		if avgUtilization(s) >= threshold && binScore(s.usedCPU, s.usedMem, s.allocatableCPU, s.allocatableMem) >= threshold {
			continue
		}
		pods, err := podutil.ListPodsOnANode(node.Name, r.handle.GetPodsAssignedToNodeFunc(), r.podFilter)
		if err != nil {
			logger.Error(err, "Error listing evictable pods", "node", node.Name)
			continue
		}
		if len(pods) == 0 {
			continue
		}
		imbalance := resourceImbalance(s.allocatableCPU, s.allocatableMem, s.usedCPU, s.usedMem)
		fsi := freeSpaceIndex(s.allocatableCPU, s.allocatableMem, s.usedCPU, s.usedMem)
		candidates = append(candidates, drainCandidate{node: node, pods: pods, priority: priorityIndex(imbalance, fsi)})
	}

	// Step 3: drain the highest-priority node first.
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].priority > candidates[j].priority })

	bins := sets.New[string]()
	iteration := 0
	for i := range candidates {
		if iteration >= r.args.MaxEvictions {
			break
		}
		dc := candidates[i]
		if bins.Has(dc.node.Name) {
			continue
		}
		remaining := append([]*v1.Pod(nil), dc.pods...)
		for iteration < r.args.MaxEvictions && len(remaining) > 0 {
			// C2-only selection: evict the pod whose predicted landing node scores best.
			pod, target := r.selectBestPod(remaining, dc.node.Name, states)
			if pod == nil {
				break
			}
			logger.V(1).Info("Eviction decision", "pod", klog.KObj(pod), "originNode", dc.node.Name, "predictedTarget", target)
			err := r.handle.Evictor().Evict(ctx, pod, evictions.EvictOptions{StrategyName: PluginName})
			iteration++
			if err != nil {
				switch err.(type) {
				case *evictions.EvictionTotalLimitError:
					return nil
				case *evictions.EvictionNodeLimitError:
					remaining = nil
					continue
				default:
					logger.Error(err, "Eviction failed", "pod", klog.KObj(pod))
					remaining = removePodFromList(remaining, pod)
					continue
				}
			}
			evCpu, evMem := getPodRequests(pod)
			evUCpu, evUMem := r.podUsage(ctx, pod)
			if s, ok := states[dc.node.Name]; ok {
				s.requestedCPU -= evCpu
				s.requestedMem -= evMem
				s.usedCPU -= evUCpu
				s.usedMem -= evUMem
			}
			if t, ok := states[target]; ok {
				t.requestedCPU += evCpu
				t.requestedMem += evMem
				t.usedCPU += evUCpu
				t.usedMem += evUMem
			}
			bins.Insert(target)
			remaining = removePodFromList(remaining, pod)
		}
	}
	return nil
}

// selectBestPod is the C2 selector (the TOPSIS plugin's C2 criterion, standalone):
// evict the pod whose predicted landing node has the highest cpu:mem bin score —
// i.e. the pod with the most balance to gain. Because predictSchedulerTarget
// returns the binScore-argmax node, this is the pod's best achievable balance,
// aligned with the stranding objective S = Σ|cpuFrac − memFrac|. nil if no pod has
// a feasible target.
func (r *ResourceDefragmentationC2) selectBestPod(pods []*v1.Pod, sourceName string, states map[string]*nodeState) (*v1.Pod, string) {
	var bestPod *v1.Pod
	bestTarget := ""
	bestScore := -math.MaxFloat64
	for _, pod := range pods {
		target, ts := r.predictSchedulerTarget(pod, sourceName, states)
		if target == "" {
			continue
		}
		podCpu, podMem := getPodRequests(pod)
		score := binScore(ts.requestedCPU+podCpu, ts.requestedMem+podMem, ts.allocatableCPU, ts.allocatableMem)
		if score > bestScore {
			bestScore = score
			bestPod = pod
			bestTarget = target
		}
	}
	return bestPod, bestTarget
}

// predictSchedulerTarget picks the node the pod would land on, scored by the same
// lightweight MostAllocated + BalancedAllocation bin score the TOPSIS plugin's
// C2 uses: schedulable, fits by requests, within the ceiling, packed upward, and
// among those the highest binScore. Because the target is the binScore-argmax,
// binScore(target) is the pod's best achievable balance — the selection signal.
func (r *ResourceDefragmentationC2) predictSchedulerTarget(pod *v1.Pod, sourceName string, states map[string]*nodeState) (string, *nodeState) {
	source, ok := states[sourceName]
	if !ok {
		return "", nil
	}
	podCpu, podMem := getPodRequests(pod)
	sourceUtil := minUtilization(source)
	ceiling := r.consolidationTarget()

	bestName := ""
	bestScore := -math.MaxFloat64
	var bestState *nodeState
	for name, s := range states {
		if name == sourceName || !isPodSchedulableOnNode(pod, s.node) {
			continue
		}
		if s.allocatableCPU-s.requestedCPU < podCpu || s.allocatableMem-s.requestedMem < podMem {
			continue
		}
		projCpu := float64(s.requestedCPU+podCpu) / float64(s.allocatableCPU)
		projMem := float64(s.requestedMem+podMem) / float64(s.allocatableMem)
		if math.Max(projCpu, projMem) > ceiling {
			continue
		}
		if minUtilization(s) < sourceUtil {
			continue
		}
		score := binScore(s.requestedCPU+podCpu, s.requestedMem+podMem, s.allocatableCPU, s.allocatableMem)
		if score > bestScore {
			bestScore = score
			bestName = name
			bestState = s
		}
	}
	return bestName, bestState
}

// ──────────────────────────────────────────────────────────────────────────────
// node metrics / helpers
// ──────────────────────────────────────────────────────────────────────────────

// binScore is the lightweight [0,1] node-quality score used only for candidacy
// (is this a bad bin?): (density + balance)/2.
func binScore(reqCpu, reqMem, allocCpu, allocMem int64) float64 {
	cpuFrac := float64(reqCpu) / float64(allocCpu)
	memFrac := float64(reqMem) / float64(allocMem)
	return ((cpuFrac+memFrac)/2.0 + (1.0 - math.Abs(cpuFrac-memFrac))) / 2.0
}

func avgUtilization(s *nodeState) float64 {
	return (float64(s.usedCPU)/float64(s.allocatableCPU) + float64(s.usedMem)/float64(s.allocatableMem)) / 2.0
}

func minUtilization(s *nodeState) float64 {
	return math.Min(float64(s.usedCPU)/float64(s.allocatableCPU), float64(s.usedMem)/float64(s.allocatableMem))
}

func resourceImbalance(allocCpu, allocMem, reqCpu, reqMem int64) float64 {
	return float64(reqCpu)/float64(allocCpu) - float64(reqMem)/float64(allocMem)
}

func freeSpaceIndex(allocCpu, allocMem, reqCpu, reqMem int64) float64 {
	c := float64(allocCpu-reqCpu) / float64(allocCpu)
	m := float64(allocMem-reqMem) / float64(allocMem)
	return c * m
}

func priorityIndex(imbalance, fsi float64) float64 {
	return 0.5*math.Abs(imbalance) + 0.5*(1.0/(fsi+1e-10))
}

func getPodRequests(pod *v1.Pod) (cpu, mem int64) {
	for _, c := range pod.Spec.Containers {
		cpu += c.Resources.Requests.Cpu().MilliValue()
		mem += c.Resources.Requests.Memory().Value()
	}
	return cpu, mem
}

func removePodFromList(pods []*v1.Pod, target *v1.Pod) []*v1.Pod {
	out := pods[:0]
	for _, p := range pods {
		if p.Namespace != target.Namespace || p.Name != target.Name {
			out = append(out, p)
		}
	}
	return out
}

func (r *ResourceDefragmentationC2) usageMode() string {
	switch r.args.UsageMode {
	case UsageModeRequests, UsageModeActualRaw, UsageModeActualEWMA:
		return r.args.UsageMode
	default:
		if r.handle.MetricsCollector() != nil {
			return UsageModeActualEWMA // "real usage" by default
		}
		return UsageModeRequests
	}
}

func (r *ResourceDefragmentationC2) nodeUsage(ctx context.Context, node *v1.Node, reqCpu, reqMem int64) (int64, int64) {
	switch r.usageMode() {
	case UsageModeActualRaw:
		if r.handle.MetricsCollector() == nil {
			return reqCpu, reqMem
		}
		nm, err := r.handle.MetricsCollector().MetricsClient().MetricsV1beta1().NodeMetricses().Get(ctx, node.Name, metav1.GetOptions{})
		if err != nil {
			r.logger.Error(err, "raw node metrics unavailable; using requests", "node", node.Name)
			return reqCpu, reqMem
		}
		return nm.Usage.Cpu().MilliValue(), nm.Usage.Memory().Value()
	case UsageModeActualEWMA:
		if r.handle.MetricsCollector() == nil {
			return reqCpu, reqMem
		}
		if !r.handle.MetricsCollector().HasSynced() {
			if err := r.handle.MetricsCollector().Collect(ctx); err != nil {
				return reqCpu, reqMem
			}
		}
		if u, err := r.handle.MetricsCollector().NodeUsage(node); err == nil {
			return u[v1.ResourceCPU].MilliValue(), u[v1.ResourceMemory].Value()
		}
		return reqCpu, reqMem
	default:
		return reqCpu, reqMem
	}
}

func (r *ResourceDefragmentationC2) podUsage(ctx context.Context, pod *v1.Pod) (int64, int64) {
	if r.usageMode() == UsageModeRequests || r.handle.MetricsCollector() == nil {
		return getPodRequests(pod)
	}
	pm, err := r.handle.MetricsCollector().MetricsClient().MetricsV1beta1().PodMetricses(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return getPodRequests(pod)
	}
	var cpu, mem int64
	for _, c := range pm.Containers {
		cpu += c.Usage.Cpu().MilliValue()
		mem += c.Usage.Memory().Value()
	}
	return cpu, mem
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
	for _, t := range node.Spec.Taints {
		if t.Key == "node-role.kubernetes.io/control-plane" || t.Key == "node-role.kubernetes.io/master" {
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
	for _, tol := range pod.Spec.Tolerations {
		if tol.Effect != "" && tol.Effect != taint.Effect {
			continue
		}
		if tol.Key != taint.Key {
			continue
		}
		switch tol.Operator {
		case v1.TolerationOpExists:
			return true
		case v1.TolerationOpEqual, "":
			if tol.Value == taint.Value {
				return true
			}
		}
	}
	return false
}
