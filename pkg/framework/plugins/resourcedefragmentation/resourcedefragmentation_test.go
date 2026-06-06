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
	"testing"

	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	fakemetricsclient "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"sigs.k8s.io/descheduler/pkg/descheduler/metricscollector"

	"sigs.k8s.io/descheduler/pkg/framework/plugins/defaultevictor"
	frameworktesting "sigs.k8s.io/descheduler/pkg/framework/testing"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
	"sigs.k8s.io/descheduler/test"
)

var (
	testNodesGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	testPodsGVR  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
)

// withNodeCapacity overrides only the CPU and Memory allocatable on a node,
// preserving other resources (e.g. Pods capacity) set by buildTestNode.
func withNodeCapacity(cpu, memory string) func(*v1.Node) {
	return func(n *v1.Node) {
		n.Status.Allocatable[v1.ResourceCPU] = resource.MustParse(cpu)
		n.Status.Allocatable[v1.ResourceMemory] = resource.MustParse(memory)
	}
}

// withPodRequests sets the resource requests on the first (and only) container of a pod.
func withPodRequests(cpu, memory string) func(*v1.Pod) {
	return func(p *v1.Pod) {
		p.Spec.Containers = []v1.Container{
			{
				Resources: v1.ResourceRequirements{
					Requests: v1.ResourceList{
						v1.ResourceCPU:    resource.MustParse(cpu),
						v1.ResourceMemory: resource.MustParse(memory),
					},
				},
			},
		}
	}
}

func withNodeTaint(key string, effect v1.TaintEffect) func(*v1.Node) {
	return func(n *v1.Node) {
		n.Spec.Taints = append(n.Spec.Taints, v1.Taint{Key: key, Effect: effect})
	}
}

func withNodeLabel(key, value string) func(*v1.Node) {
	return func(n *v1.Node) {
		if n.Labels == nil {
			n.Labels = map[string]string{}
		}
		n.Labels[key] = value
	}
}

func buildTestNode(nodeName string, apply ...func(*v1.Node)) *v1.Node {
	node := test.BuildTestNode(nodeName, 2000, 4294967296, 10, nil)
	for _, fn := range apply {
		fn(node)
	}
	return node
}

func buildTestPodForNode(name, nodeName string, apply ...func(*v1.Pod)) *v1.Pod {
	// SetRSOwnerRef is required so the DefaultEvictor does not reject the pod as a naked pod.
	pod := test.BuildTestPod(name, 100, 0, nodeName, test.SetRSOwnerRef)
	for _, fn := range apply {
		fn(pod)
	}
	return pod
}

func TestResourceDefragmentation(t *testing.T) {
	testCases := []struct {
		description             string
		args                    *ResourceDefragmentationArgs
		pods                    []*v1.Pod
		nodes                   []*v1.Node
		expectedEvictedPodCount uint
		expectedEvictedPodName  string // non-empty: assert the exact pod evicted by TOPSIS
	}{
		{
			// No node is under the consolidation threshold → nothing to empty.
			description: "well-utilized cluster: no under-utilized node, no eviction",
			args:        &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-a", withNodeCapacity("2000m", "4Gi")),
			},
			pods: []*v1.Pod{
				// util = max(1000/2000, 2Gi/4Gi) = 0.50 ≥ 0.40 → not a drain candidate.
				buildTestPodForNode("pod-balanced", "node-a", withPodRequests("1000m", "2Gi")),
			},
			expectedEvictedPodCount: 0,
		},
		{
			// An under-utilized node is emptied; its pod is relocated to the denser,
			// balanced bin (which is itself above the threshold and pack-upward valid).
			description: "under-utilized node drains its pod onto a denser bin",
			args:        &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-light", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-bin", withNodeCapacity("2000m", "4Gi")),
			},
			pods: []*v1.Pod{
				// node-light: util = max(0.20, 0.195) = 0.20 < 0.40 → candidate.
				buildTestPodForNode("pod-light", "node-light", withPodRequests("400m", "800Mi")),
				// node-bin: util = 0.50 ≥ 0.40 → not a candidate; it is the denser target.
				buildTestPodForNode("pod-binload", "node-bin", withPodRequests("1000m", "2Gi")),
			},
			expectedEvictedPodCount: 1,
			expectedEvictedPodName:  "pod-light",
		},
		{
			// The only feasible target would exceed the 0.90 ceiling → keep the node.
			description: "consolidation ceiling prevents overpacking",
			args:        &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-light", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-almost-full", withNodeCapacity("2000m", "4Gi")),
			},
			pods: []*v1.Pod{
				// node-light: util = max(0.125, 0.049) = 0.125 < 0.40 → candidate.
				buildTestPodForNode("pod-drain", "node-light", withPodRequests("250m", "200Mi")),
				// node-almost-full: 1600/2000 = 0.80; adding pod-drain → 1850/2000 = 0.925 > 0.90.
				buildTestPodForNode("pod-heavy", "node-almost-full", withPodRequests("1600m", "3Gi")),
			},
			expectedEvictedPodCount: 0,
		},
		{
			// Two under-utilized candidates with near-equal FSI, so the priority
			// pr = 0.5·|RII| + 0.5·(1/FSI) is decided by imbalance: the lopsided node
			// is drained first. With MaxEvictions=1, only its pod is evicted.
			//   node-lopsided: |RII|≈0.34, FSI≈0.644 → pr ≈ 0.95
			//   node-balanced: |RII|≈0.00, FSI≈0.644 → pr ≈ 0.78
			description: "priority order: lopsided under-utilized node is drained before the balanced one",
			args:        &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 1},
			nodes: []*v1.Node{
				buildTestNode("node-lopsided", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-balanced", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-bin", withNodeCapacity("2000m", "4Gi")),
			},
			pods: []*v1.Pod{
				// node-lopsided: util 0.35, cpu-skewed.
				buildTestPodForNode("pod-cpu", "node-lopsided", withPodRequests("700m", "40Mi")),
				// node-balanced: util 0.20, even cpu:mem; similar FSI to node-lopsided.
				buildTestPodForNode("pod-bal", "node-balanced", withPodRequests("400m", "800Mi")),
				// node-bin: util 0.50 ≥ 0.40 → the denser pack-upward target for both.
				buildTestPodForNode("pod-binload", "node-bin", withPodRequests("1000m", "2Gi")),
			},
			expectedEvictedPodCount: 1,
			expectedEvictedPodName:  "pod-cpu",
		},
		{
			// Control-plane node and its pods are excluded from both source and target
			// consideration; the lone worker has no valid (non-control-plane) target.
			description: "control-plane node and pods are fully excluded",
			args:        &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode(
					"node-control-plane",
					withNodeCapacity("2000m", "4Gi"),
					withNodeLabel("node-role.kubernetes.io/control-plane", ""),
					withNodeTaint("node-role.kubernetes.io/control-plane", v1.TaintEffectNoSchedule),
				),
				buildTestNode("node-worker", withNodeCapacity("4000m", "8Gi")),
			},
			pods: []*v1.Pod{
				buildTestPodForNode("cp-pod", "node-control-plane", withPodRequests("1800m", "200Mi")),
				// Under-utilized worker, but the only other node is the control-plane → no target.
				buildTestPodForNode("worker-idle", "node-worker", withPodRequests("100m", "100Mi")),
			},
			expectedEvictedPodCount: 0,
		},
		{
			// Balance gate ON (λ=0.70): a cpu-skewed pod's only feasible target is a
			// clean balanced node; placing it there drops that node's balance by ~0.34
			// > (1−λ)=0.30, so the gate rejects it and the pod is left in place. This is
			// the S3 fix — it refuses to relocate stranding onto a clean node.
			description: "balance gate blocks pouring a skewed pod onto a clean balanced node",
			args:        &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, BalancePenaltyWeight: 0.70, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-cpuskew", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-bin", withNodeCapacity("2000m", "4Gi")),
			},
			pods: []*v1.Pod{
				// node-cpuskew: util max-dim 0.35, avg 0.18 < 0.40 → candidate.
				buildTestPodForNode("pod-cpu", "node-cpuskew", withPodRequests("700m", "40Mi")),
				// node-bin: balanced & well-utilized (0.5/0.5) → a bin, not a candidate;
				// adding the cpu pod would skew it to 0.85/0.51 (balance 1.0 → 0.66).
				buildTestPodForNode("pod-binload", "node-bin", withPodRequests("1000m", "2Gi")),
			},
			expectedEvictedPodCount: 0,
		},
		{
			// Same layout with λ=0 → gate disabled → legacy behavior: the move proceeds.
			description: "balance gate disabled (lambda=0) reproduces the legacy skewing move",
			args:        &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, BalancePenaltyWeight: 0, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-cpuskew", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-bin", withNodeCapacity("2000m", "4Gi")),
			},
			pods: []*v1.Pod{
				buildTestPodForNode("pod-cpu", "node-cpuskew", withPodRequests("700m", "40Mi")),
				buildTestPodForNode("pod-binload", "node-bin", withPodRequests("1000m", "2Gi")),
			},
			expectedEvictedPodCount: 1,
			expectedEvictedPodName:  "pod-cpu",
		},
		{
			// A NoSchedule taint the pod does not tolerate makes the only other node an
			// infeasible target → the under-utilized node cannot be drained.
			description: "tainted target node is not a feasible relocation target",
			args:        &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-light", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-tainted", withNodeCapacity("4000m", "8Gi"), withNodeTaint("dedicated", v1.TaintEffectNoSchedule)),
			},
			pods: []*v1.Pod{
				buildTestPodForNode("pod-only", "node-light", withPodRequests("400m", "200Mi")),
			},
			expectedEvictedPodCount: 0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var objs []runtime.Object
			for _, node := range tc.nodes {
				objs = append(objs, node)
			}
			for _, pod := range tc.pods {
				objs = append(objs, pod)
			}
			fakeClient := fake.NewSimpleClientset(objs...)

			handle, podEvictor, err := frameworktesting.InitFrameworkHandle(ctx, fakeClient, nil, defaultevictor.DefaultEvictorArgs{NodeFit: true}, nil)
			if err != nil {
				t.Fatalf("Unable to initialize a framework handle: %v", err)
			}

			plugin, err := New(ctx, tc.args, handle)
			if err != nil {
				t.Fatalf("Unable to initialize the plugin: %v", err)
			}

			status := plugin.(frameworktypes.BalancePlugin).Balance(ctx, tc.nodes)
			if status != nil && status.Err != nil {
				t.Fatalf("Balance failed: %v", status.Err)
			}

			actualEvicted := podEvictor.TotalEvicted()
			if actualEvicted != tc.expectedEvictedPodCount {
				t.Errorf("expected %d evictions, got %d", tc.expectedEvictedPodCount, actualEvicted)
			}

			// When a specific pod is expected to be evicted, verify the Eviction API was
			// called with that pod name (not just that the count matched).
			if tc.expectedEvictedPodName != "" {
				found := false
				for _, action := range fakeClient.Actions() {
					if action.GetVerb() != "create" ||
						action.GetResource().Resource != "pods" ||
						action.GetSubresource() != "eviction" {
						continue
					}
					eviction := action.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction)
					if eviction.Name == tc.expectedEvictedPodName {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected pod %q to be evicted by TOPSIS, but it was not found in eviction API calls", tc.expectedEvictedPodName)
				}
			}
		})
	}
}

// TestResourceDefragmentationUsesMetricsServerUsage verifies that "under-utilized"
// is judged from actual usage, not requests: node-a is heavily over-provisioned
// (requests 1500m/3Gi) but barely used (200m/200Mi), so only with metrics-server
// usage is it seen as a drain candidate and consolidated onto node-b.
func TestResourceDefragmentationUsesMetricsServerUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeA := buildTestNode("node-a", withNodeCapacity("2000m", "4Gi"))
	nodeB := buildTestNode("node-b", withNodeCapacity("4000m", "8Gi"))
	// node-a request util = 0.75 (looks loaded), but actual util = 0.10 (idle).
	pod := buildTestPodForNode("pod-overprovisioned", "node-a", withPodRequests("1500m", "3Gi"))
	// node-b is the denser bin and is more utilized than node-a, so it is a valid
	// pack-upward target.
	podB := buildTestPodForNode("pod-binload", "node-b", withPodRequests("1000m", "2Gi"))

	fakeClient := fake.NewSimpleClientset(nodeA, nodeB, pod, podB)
	metricsClient := fakemetricsclient.NewSimpleClientset()
	if err := metricsClient.Tracker().Create(testNodesGVR, test.BuildNodeMetrics("node-a", 200, 200*1024*1024), ""); err != nil {
		t.Fatalf("failed creating node-a metrics: %v", err)
	}
	if err := metricsClient.Tracker().Create(testNodesGVR, test.BuildNodeMetrics("node-b", 1000, 2*1024*1024*1024), ""); err != nil {
		t.Fatalf("failed creating node-b metrics: %v", err)
	}
	if err := metricsClient.Tracker().Create(testPodsGVR, test.BuildPodMetrics("pod-overprovisioned", 200, 200*1024*1024), "default"); err != nil {
		t.Fatalf("failed creating pod metrics: %v", err)
	}
	if err := metricsClient.Tracker().Create(testPodsGVR, test.BuildPodMetrics("pod-binload", 1000, 2*1024*1024*1024), "default"); err != nil {
		t.Fatalf("failed creating pod-b metrics: %v", err)
	}

	sharedInformerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	// Create the node informer before Start so the factory actually starts and syncs it.
	sharedInformerFactory.Core().V1().Nodes().Informer()
	sharedInformerFactory.Start(ctx.Done())
	sharedInformerFactory.WaitForCacheSync(ctx.Done())
	collector := metricscollector.NewMetricsCollector(sharedInformerFactory.Core().V1().Nodes().Lister(), metricsClient, labels.Everything())
	if err := collector.Collect(ctx); err != nil {
		t.Fatalf("failed collecting metrics: %v", err)
	}

	handle, podEvictor, err := frameworktesting.InitFrameworkHandle(ctx, fakeClient, nil, defaultevictor.DefaultEvictorArgs{NodeFit: true}, nil)
	if err != nil {
		t.Fatalf("Unable to initialize a framework handle: %v", err)
	}
	handle.MetricsCollectorImpl = collector

	plugin, err := New(ctx, &ResourceDefragmentationArgs{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 1}, handle)
	if err != nil {
		t.Fatalf("Unable to initialize the plugin: %v", err)
	}

	status := plugin.(frameworktypes.BalancePlugin).Balance(ctx, []*v1.Node{nodeA, nodeB})
	if status != nil && status.Err != nil {
		t.Fatalf("Balance failed: %v", status.Err)
	}

	if podEvictor.TotalEvicted() != 1 {
		t.Fatalf("expected actual-usage metrics to drive 1 consolidation eviction, got %d", podEvictor.TotalEvicted())
	}
}
