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
			description: "healthy balanced node: imbalance below threshold, no eviction",
			args:        &ResourceDefragmentationArgs{ImbalanceThreshold: 0.3, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-a", withNodeCapacity("2000m", "4Gi")),
			},
			pods: []*v1.Pod{
				// rCPU = 1000/2000 = 0.50, rMem = 2Gi/4Gi = 0.50 → imbalance = 0.00 < 0.3
				buildTestPodForNode("pod-balanced", "node-a", withPodRequests("1000m", "2Gi")),
			},
			expectedEvictedPodCount: 0,
		},
		{
			// Exercises the Bug 2 fix: when nPods==1, dPlus[0]==dMinus[0]==0 (denom==0).
			// Without the fix, TOPSIS returns nil and nothing is evicted.
			// With the fix, cc=0.5 (equidistant) and the single pod is correctly selected.
			description: "single pod on fragmented node: TOPSIS must not return nil",
			args:        &ResourceDefragmentationArgs{ImbalanceThreshold: 0.3, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-fragmented", withNodeCapacity("2000m", "4Gi")),
				// node-target has ample room to absorb the pod.
				buildTestNode("node-target", withNodeCapacity("4000m", "8Gi")),
			},
			pods: []*v1.Pod{
				// rCPU = 1800/2000 = 0.90, rMem = 200Mi/4Gi ≈ 0.048 → imbalance ≈ 0.85 >> 0.3
				buildTestPodForNode("pod-only-one", "node-fragmented", withPodRequests("1800m", "200Mi")),
			},
			expectedEvictedPodCount: 1,
			expectedEvictedPodName:  "pod-only-one",
		},

		{
			description: "tainted target node is not treated as a feasible eviction target",
			args:        &ResourceDefragmentationArgs{ImbalanceThreshold: 0.3, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-fragmented", withNodeCapacity("2000m", "4Gi")),
				// This node has enough free resources, but a regular workload pod cannot land here.
				buildTestNode("node-control-plane", withNodeCapacity("4000m", "8Gi"), withNodeTaint("node-role.kubernetes.io/control-plane", v1.TaintEffectNoSchedule)),
			},
			pods: []*v1.Pod{
				buildTestPodForNode("pod-only-one", "node-fragmented", withPodRequests("1800m", "200Mi")),
			},
			expectedEvictedPodCount: 0,
		},
		{
			// TOPSIS must distinguish the CPU-heavy parasite from the innocent pod and
			// evict only the one causing fragmentation (higher C1 and C3 scores). The
			// non-origin target is memory-heavy, so simulating the CPU-heavy pod there
			// improves the projected real-usage/request score instead of just moving
			// fragmentation from one node to another.
			description: "fragmented node with CPU-heavy pod: TOPSIS selects the right pod to evict",
			args:        &ResourceDefragmentationArgs{ImbalanceThreshold: 0.5, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-sick", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-healthy", withNodeCapacity("4000m", "8Gi")), // has room to receive pods
			},
			pods: []*v1.Pod{
				// CPU-heavy pod: rCPU contribution is large, rMem is tiny → high C1
				buildTestPodForNode("pod-cpu-parasite", "node-sick", withPodRequests("1600m", "200Mi")),
				// Innocent pod: balanced usage → should NOT be evicted
				buildTestPodForNode("pod-innocent", "node-sick", withPodRequests("200m", "1Gi")),
				// Target-side complementary load lets the feasibility guard project an improvement.
				buildTestPodForNode("pod-target-mem-heavy", "node-healthy", withPodRequests("200m", "4Gi")),
			},
			expectedEvictedPodCount: 1,
			expectedEvictedPodName:  "pod-cpu-parasite",
		},
		{
			// The C2 criterion returns -999.9 when the candidate pod cannot fit on any other node.
			// This heavy penalty drives TOPSIS not to select any pod for eviction.
			description: "fragmented node but all target nodes full: C2 penalty blocks eviction",
			args:        &ResourceDefragmentationArgs{ImbalanceThreshold: 0.3, MaxEvictions: 5},
			nodes: []*v1.Node{
				buildTestNode("node-sick", withNodeCapacity("2000m", "4Gi")),
				buildTestNode("node-full", withNodeCapacity("2000m", "4Gi")),
			},
			pods: []*v1.Pod{
				buildTestPodForNode("pod-cpu-parasite", "node-sick", withPodRequests("1600m", "200Mi")),
				// pod-blocker saturates node-full on CPU, leaving no room for pod-cpu-parasite.
				buildTestPodForNode("pod-blocker", "node-full", withPodRequests("1900m", "3Gi")),
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

func TestResourceDefragmentationUsesMetricsServerUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeA := buildTestNode("node-a", withNodeCapacity("2000m", "4Gi"))
	nodeB := buildTestNode("node-b", withNodeCapacity("4000m", "8Gi"))
	pod := buildTestPodForNode("pod-real-cpu-heavy", "node-a", withPodRequests("1000m", "2Gi"))
	podB := buildTestPodForNode("pod-target-mem-heavy", "node-b", withPodRequests("500m", "2Gi"))

	fakeClient := fake.NewSimpleClientset(nodeA, nodeB, pod, podB)
	metricsClient := fakemetricsclient.NewSimpleClientset()
	if err := metricsClient.Tracker().Create(testNodesGVR, test.BuildNodeMetrics("node-a", 1800, 200*1024*1024), ""); err != nil {
		t.Fatalf("failed creating node-a metrics: %v", err)
	}
	if err := metricsClient.Tracker().Create(testNodesGVR, test.BuildNodeMetrics("node-b", 200, 6*1024*1024*1024), ""); err != nil {
		t.Fatalf("failed creating node-b metrics: %v", err)
	}
	if err := metricsClient.Tracker().Create(testPodsGVR, test.BuildPodMetrics("pod-real-cpu-heavy", 1800, 200*1024*1024), "default"); err != nil {
		t.Fatalf("failed creating pod metrics: %v", err)
	}
	if err := metricsClient.Tracker().Create(testPodsGVR, test.BuildPodMetrics("pod-target-mem-heavy", 200, 6*1024*1024*1024), "default"); err != nil {
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

	plugin, err := New(ctx, &ResourceDefragmentationArgs{ImbalanceThreshold: 0.3, MaxEvictions: 1}, handle)
	if err != nil {
		t.Fatalf("Unable to initialize the plugin: %v", err)
	}

	status := plugin.(frameworktypes.BalancePlugin).Balance(ctx, []*v1.Node{nodeA, nodeB})
	if status != nil && status.Err != nil {
		t.Fatalf("Balance failed: %v", status.Err)
	}

	if podEvictor.TotalEvicted() != 1 {
		t.Fatalf("expected real-usage metrics to drive 1 eviction, got %d", podEvictor.TotalEvicted())
	}
}
