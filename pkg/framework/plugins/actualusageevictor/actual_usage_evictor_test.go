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

package actualusageevictor

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	fakemetricsclient "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"sigs.k8s.io/descheduler/pkg/api"
	"sigs.k8s.io/descheduler/pkg/descheduler/metricscollector"
	frameworkfake "sigs.k8s.io/descheduler/pkg/framework/fake"
	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
)

func TestDefaultsAndValidation(t *testing.T) {
	args := &ActualUsageEvictorArgs{}
	SetDefaults_ActualUsageEvictorArgs(args)
	if args.CPUUsageThreshold != 0.80 {
		t.Errorf("CPU threshold = %v, want 0.80", args.CPUUsageThreshold)
	}
	if args.MemoryUsageThreshold != 0.80 {
		t.Errorf("memory threshold = %v, want 0.80", args.MemoryUsageThreshold)
	}
	if args.MissingRequestPolicy != AllowMissingRequest {
		t.Errorf("missing request policy = %q, want %q", args.MissingRequestPolicy, AllowMissingRequest)
	}
	if err := ValidateActualUsageEvictorArgs(args); err != nil {
		t.Fatalf("default args should be valid: %v", err)
	}

	invalid := []struct {
		name string
		args *ActualUsageEvictorArgs
	}{
		{
			name: "zero CPU threshold",
			args: &ActualUsageEvictorArgs{CPUUsageThreshold: 0, MemoryUsageThreshold: 0.9},
		},
		{
			name: "negative memory threshold",
			args: &ActualUsageEvictorArgs{CPUUsageThreshold: 0.8, MemoryUsageThreshold: -1},
		},
		{
			name: "namespace include and exclude",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.9,
				Namespaces: &api.Namespaces{Include: []string{"a"}, Exclude: []string{"b"}},
			},
		},
		{
			name: "invalid label selector",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.9,
				LabelSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: "app", Operator: metav1.LabelSelectorOperator("invalid"),
				}}},
			},
		},
		{
			name: "invalid missing request policy",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.9,
				MissingRequestPolicy: MissingRequestPolicy("Invalid"),
			},
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateActualUsageEvictorArgs(tc.args); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	aboveOne := &ActualUsageEvictorArgs{
		CPUUsageThreshold: 1.2, MemoryUsageThreshold: 1.5,
		MissingRequestPolicy: BlockMissingRequest,
	}
	if err := ValidateActualUsageEvictorArgs(aboveOne); err != nil {
		t.Fatalf("thresholds above 1 must be valid: %v", err)
	}
}

func TestPreEvictionFilter(t *testing.T) {
	tests := []struct {
		name        string
		args        *ActualUsageEvictorArgs
		pod         *v1.Pod
		metrics     *metricsv1beta1.PodMetrics
		noCollector bool
		want        bool
	}{
		{
			name:    "allows usage below both thresholds",
			args:    testArgs(),
			pod:     testPod("default", map[string]string{}, "1000m", "1Gi"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app", cpuMilli: 799, memoryBytes: 700 * 1024 * 1024}}),
			want:    true,
		},
		{
			name:    "blocks CPU at threshold",
			args:    testArgs(),
			pod:     testPod("default", nil, "1000m", "1Gi"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app", cpuMilli: 800, memoryBytes: 100 * 1024 * 1024}}),
			want:    false,
		},
		{
			name:    "blocks memory above threshold",
			args:    testArgs(),
			pod:     testPod("default", nil, "1000m", "1Gi"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app", cpuMilli: 100, memoryBytes: 950 * 1024 * 1024}}),
			want:    false,
		},
		{
			name:    "missing CPU request allows that dimension by default",
			args:    testArgs(),
			pod:     testPod("default", nil, "0", "1Gi"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app", cpuMilli: 1, memoryBytes: 0}}),
			want:    true,
		},
		{
			name: "missing CPU request blocks with block policy",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.8,
				MissingRequestPolicy: BlockMissingRequest,
			},
			pod:     testPod("default", nil, "0", "1Gi"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app", cpuMilli: 1, memoryBytes: 0}}),
			want:    false,
		},
		{
			name:    "both missing requests allow with allow policy",
			args:    testArgs(),
			pod:     testPod("default", nil, "0", "0"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app"}}),
			want:    true,
		},
		{
			name: "both missing requests block with block policy",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.8,
				MissingRequestPolicy: BlockMissingRequest,
			},
			pod:     testPod("default", nil, "0", "0"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app"}}),
			want:    false,
		},
		{
			name:    "missing CPU request does not suppress busy memory",
			args:    testArgs(),
			pod:     testPod("default", nil, "0", "1Gi"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app", cpuMilli: 1, memoryBytes: 900 * 1024 * 1024}}),
			want:    false,
		},
		{
			name:    "missing memory request does not suppress busy CPU",
			args:    testArgs(),
			pod:     testPod("default", nil, "1000m", "0"),
			metrics: testPodMetrics("default", []containerUsage{{name: "app", cpuMilli: 900, memoryBytes: 1}}),
			want:    false,
		},
		{
			name: "namespace outside include scope allows without metrics",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.9,
				Namespaces: &api.Namespaces{Include: []string{"protected"}},
			},
			pod:         testPod("other", nil, "1000m", "1Gi"),
			noCollector: true,
			want:        true,
		},
		{
			name: "label outside scope allows without metrics",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.9,
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"protect": "true"}},
			},
			pod:         testPod("default", map[string]string{"protect": "false"}, "1000m", "1Gi"),
			noCollector: true,
			want:        true,
		},
		{
			name: "namespace and label scope use AND",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.9,
				Namespaces:    &api.Namespaces{Include: []string{"protected"}},
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"protect": "true"}},
			},
			pod:         testPod("protected", map[string]string{"protect": "false"}, "1000m", "1Gi"),
			noCollector: true,
			want:        true,
		},
		{
			name: "matching namespace and label are evaluated",
			args: &ActualUsageEvictorArgs{
				CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.9,
				Namespaces:    &api.Namespaces{Include: []string{"protected"}},
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"protect": "true"}},
			},
			pod:     testPod("protected", map[string]string{"protect": "true"}, "1000m", "1Gi"),
			metrics: testPodMetrics("protected", []containerUsage{{name: "app", cpuMilli: 900, memoryBytes: 100}}),
			want:    false,
		},
		{
			name:        "missing collector blocks",
			args:        testArgs(),
			pod:         testPod("default", nil, "1000m", "1Gi"),
			noCollector: true,
			want:        false,
		},
		{
			name: "missing pod metrics blocks",
			args: testArgs(),
			pod:  testPod("default", nil, "1000m", "1Gi"),
			want: false,
		},
		{
			name:    "missing container metrics blocks",
			args:    testArgs(),
			pod:     testPod("default", nil, "1000m", "1Gi"),
			metrics: testPodMetrics("default", []containerUsage{{name: "other", cpuMilli: 100, memoryBytes: 100}}),
			want:    false,
		},
		{
			name: "aggregates multiple containers",
			args: testArgs(),
			pod: testPodWithContainers("default", nil, []containerRequest{
				{name: "app", cpu: "500m", memory: "512Mi"},
				{name: "sidecar", cpu: "500m", memory: "512Mi"},
			}),
			metrics: testPodMetrics("default", []containerUsage{
				{name: "app", cpuMilli: 400, memoryBytes: 100 * 1024 * 1024},
				{name: "sidecar", cpuMilli: 400, memoryBytes: 100 * 1024 * 1024},
			}),
			want: false,
		},
		{
			name: "one missing container request skips the whole resource dimension",
			args: testArgs(),
			pod: testPodWithContainers("default", nil, []containerRequest{
				{name: "app", cpu: "1000m", memory: "512Mi"},
				{name: "sidecar", cpu: "0", memory: "512Mi"},
			}),
			metrics: testPodMetrics("default", []containerUsage{
				{name: "app", cpuMilli: 900, memoryBytes: 100 * 1024 * 1024},
				{name: "sidecar", cpuMilli: 1, memoryBytes: 100 * 1024 * 1024},
			}),
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handle := &frameworkfake.HandleImpl{}
			if !tc.noCollector {
				metricsClient := fakemetricsclient.NewSimpleClientset()
				if tc.metrics != nil {
					podMetricsGVR := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
					if err := metricsClient.Tracker().Create(podMetricsGVR, tc.metrics, tc.metrics.Namespace); err != nil {
						t.Fatalf("create metrics: %v", err)
					}
				}
				handle.MetricsCollectorImpl = metricscollector.NewMetricsCollector(nil, metricsClient, labels.Everything())
			}

			plugin, err := New(context.Background(), tc.args, handle)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := plugin.(frameworktypes.EvictorPlugin).PreEvictionFilter(tc.pod); got != tc.want {
				t.Errorf("PreEvictionFilter() = %v, want %v", got, tc.want)
			}
			if !plugin.(frameworktypes.EvictorPlugin).Filter(tc.pod) {
				t.Error("Filter() must be a no-op")
			}
		})
	}
}

func TestAggregatePodResourcesRejectsMissingResourceMetric(t *testing.T) {
	pod := testPod("default", nil, "1000m", "1Gi")
	metrics := []metricsv1beta1.ContainerMetrics{{
		Name: "app",
		Usage: v1.ResourceList{
			v1.ResourceMemory: resource.MustParse("100Mi"),
		},
	}}

	if _, err := aggregatePodResources(pod, metrics); err == nil {
		t.Fatal("expected missing CPU metric to be rejected")
	}
}

type containerRequest struct {
	name   string
	cpu    string
	memory string
}

type containerUsage struct {
	name        string
	cpuMilli    int64
	memoryBytes int64
}

func testArgs() *ActualUsageEvictorArgs {
	return &ActualUsageEvictorArgs{
		CPUUsageThreshold: 0.8, MemoryUsageThreshold: 0.8,
		MissingRequestPolicy: AllowMissingRequest,
	}
}

func testPod(namespace string, podLabels map[string]string, cpu, memory string) *v1.Pod {
	return testPodWithContainers(namespace, podLabels, []containerRequest{{name: "app", cpu: cpu, memory: memory}})
}

func testPodWithContainers(namespace string, podLabels map[string]string, containers []containerRequest) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: namespace, Labels: podLabels},
	}
	for _, container := range containers {
		pod.Spec.Containers = append(pod.Spec.Containers, v1.Container{
			Name: container.name,
			Resources: v1.ResourceRequirements{Requests: v1.ResourceList{
				v1.ResourceCPU:    resource.MustParse(container.cpu),
				v1.ResourceMemory: resource.MustParse(container.memory),
			}},
		})
	}
	return pod
}

func testPodMetrics(namespace string, usages []containerUsage) *metricsv1beta1.PodMetrics {
	metrics := &metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: namespace},
	}
	for _, usage := range usages {
		metrics.Containers = append(metrics.Containers, metricsv1beta1.ContainerMetrics{
			Name: usage.name,
			Usage: v1.ResourceList{
				v1.ResourceCPU:    *resource.NewMilliQuantity(usage.cpuMilli, resource.DecimalSI),
				v1.ResourceMemory: *resource.NewQuantity(usage.memoryBytes, resource.BinarySI),
			},
		})
	}
	return metrics
}
