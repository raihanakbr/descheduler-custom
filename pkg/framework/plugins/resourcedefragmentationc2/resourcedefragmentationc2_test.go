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

package resourcedefragmentationc2

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	fakemetricsclient "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"sigs.k8s.io/descheduler/pkg/descheduler/metricscollector"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/actualusageevictor"
	"sigs.k8s.io/descheduler/pkg/framework/plugins/defaultevictor"
	frameworktesting "sigs.k8s.io/descheduler/pkg/framework/testing"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
	"sigs.k8s.io/descheduler/test"
)

func node(name, cpu, mem string) *v1.Node {
	n := test.BuildTestNode(name, 2000, 4294967296, 10, nil)
	n.Status.Allocatable[v1.ResourceCPU] = resource.MustParse(cpu)
	n.Status.Allocatable[v1.ResourceMemory] = resource.MustParse(mem)
	return n
}

func pod(name, nodeName, cpu, mem string) *v1.Pod {
	p := test.BuildTestPod(name, 100, 0, nodeName, test.SetRSOwnerRef)
	p.Spec.Containers = []v1.Container{{
		Name: "container-1",
		Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
			v1.ResourceCPU:    resource.MustParse(cpu),
			v1.ResourceMemory: resource.MustParse(mem),
		}},
	}}
	return p
}

func TestBinScorePrefersBalanced(t *testing.T) {
	alloc := int64(2000)
	balanced := binScore(1200, 1200, alloc, alloc) // 0.60/0.60
	skewed := binScore(1960, 400, alloc, alloc)    // 0.98/0.20 — denser cpu but skewed
	if !(balanced > skewed) {
		t.Errorf("bin score should prefer the balanced node: balanced=%.4f skewed=%.4f", balanced, skewed)
	}
}

type combinedEvictorPlugin struct {
	plugins []frameworktypes.EvictorPlugin
}

func (c *combinedEvictorPlugin) Name() string {
	return "CombinedEvictor"
}

func (c *combinedEvictorPlugin) Filter(pod *v1.Pod) bool {
	for _, plugin := range c.plugins {
		if !plugin.Filter(pod) {
			return false
		}
	}
	return true
}

func (c *combinedEvictorPlugin) PreEvictionFilter(pod *v1.Pod) bool {
	for _, plugin := range c.plugins {
		if !plugin.PreEvictionFilter(pod) {
			return false
		}
	}
	return true
}

func TestBalanceFiltersHighActualUsagePod(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	nodeLight := node("node-light", "2000m", "4Gi")
	nodeBin := node("node-bin", "2000m", "4Gi")
	highUsagePod := pod("pod-high", "node-light", "200m", "400Mi")
	lowUsagePod := pod("pod-low", "node-light", "200m", "400Mi")
	binPod := pod("pod-binload", "node-bin", "1000m", "2Gi")

	client := fake.NewSimpleClientset(nodeLight, nodeBin, highUsagePod, lowUsagePod, binPod)
	handle, podEvictor, err := frameworktesting.InitFrameworkHandle(
		ctx,
		client,
		nil,
		defaultevictor.DefaultEvictorArgs{NodeFit: true},
		nil,
	)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	metricsClient := fakemetricsclient.NewSimpleClientset()
	podMetricsGVR := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	for _, metrics := range []runtime.Object{
		test.BuildPodMetrics("pod-high", 180, 360*1024*1024),
		test.BuildPodMetrics("pod-low", 20, 40*1024*1024),
	} {
		if err := metricsClient.Tracker().Create(podMetricsGVR, metrics, "default"); err != nil {
			t.Fatalf("create pod metrics: %v", err)
		}
	}
	handle.MetricsCollectorImpl = metricscollector.NewMetricsCollector(
		handle.SharedInformerFactoryImpl.Core().V1().Nodes().Lister(),
		metricsClient,
		labels.Everything(),
	)

	usagePlugin, err := actualusageevictor.New(ctx, &actualusageevictor.ActualUsageEvictorArgs{
		CPUUsageThreshold:    0.8,
		MemoryUsageThreshold: 0.9,
	}, handle)
	if err != nil {
		t.Fatalf("ActualUsageEvictor New: %v", err)
	}
	handle.EvictorFilterImpl = &combinedEvictorPlugin{plugins: []frameworktypes.EvictorPlugin{
		handle.EvictorFilterImpl,
		usagePlugin.(frameworktypes.EvictorPlugin),
	}}

	plugin, err := New(ctx, &ResourceDefragmentationC2Args{
		ConsolidationThreshold: 0.40,
		ConsolidationTarget:    0.90,
		MaxEvictions:           1,
	}, handle)
	if err != nil {
		t.Fatalf("ResourceDefragmentationC2 New: %v", err)
	}

	if status := plugin.(frameworktypes.BalancePlugin).Balance(ctx, []*v1.Node{nodeLight, nodeBin}); status != nil && status.Err != nil {
		t.Fatalf("Balance: %v", status.Err)
	}
	if got := podEvictor.TotalEvicted(); got != 1 {
		t.Fatalf("evictions = %d, want 1", got)
	}

	var evictedPod string
	for _, action := range client.Actions() {
		if action.GetVerb() == "create" && action.GetSubresource() == "eviction" {
			evictedPod = action.(k8stesting.CreateAction).GetObject().(*policyv1.Eviction).Name
		}
	}
	if evictedPod != "pod-low" {
		t.Errorf("evicted pod = %q, want pod-low", evictedPod)
	}
}

func TestBalance(t *testing.T) {
	cases := []struct {
		name      string
		args      *ResourceDefragmentationC2Args
		nodes     []*v1.Node
		pods      []*v1.Pod
		wantEvict uint
		wantName  string
	}{
		{
			name:  "under-utilized node drains its pod onto the denser bin",
			args:  &ResourceDefragmentationC2Args{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 5},
			nodes: []*v1.Node{node("node-light", "2000m", "4Gi"), node("node-bin", "2000m", "4Gi")},
			pods: []*v1.Pod{
				pod("pod-light", "node-light", "400m", "800Mi"), // util 0.20 → candidate
				pod("pod-binload", "node-bin", "1000m", "2Gi"),  // util 0.50 → bin
			},
			wantEvict: 1, wantName: "pod-light",
		},
		{
			name:      "well-utilized cluster: no eviction",
			args:      &ResourceDefragmentationC2Args{ConsolidationThreshold: 0.40, ConsolidationTarget: 0.90, MaxEvictions: 5},
			nodes:     []*v1.Node{node("node-a", "2000m", "4Gi")},
			pods:      []*v1.Pod{pod("pod-a", "node-a", "1000m", "2Gi")}, // util 0.50 ≥ 0.40
			wantEvict: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var objs []runtime.Object
			for _, n := range tc.nodes {
				objs = append(objs, n)
			}
			for _, p := range tc.pods {
				objs = append(objs, p)
			}
			client := fake.NewSimpleClientset(objs...)
			handle, evictor, err := frameworktesting.InitFrameworkHandle(ctx, client, nil, defaultevictor.DefaultEvictorArgs{NodeFit: true}, nil)
			if err != nil {
				t.Fatalf("handle: %v", err)
			}
			plugin, err := New(ctx, tc.args, handle)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if s := plugin.(frameworktypes.BalancePlugin).Balance(ctx, tc.nodes); s != nil && s.Err != nil {
				t.Fatalf("Balance: %v", s.Err)
			}
			if got := evictor.TotalEvicted(); got != tc.wantEvict {
				t.Errorf("evictions = %d, want %d", got, tc.wantEvict)
			}
		})
	}
}
