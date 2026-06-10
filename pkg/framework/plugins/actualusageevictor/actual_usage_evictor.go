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
	"fmt"
	"math"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	frameworktypes "sigs.k8s.io/descheduler/pkg/framework/types"
)

const PluginName = "ActualUsageEvictor"

// ActualUsageEvictor prevents currently busy pods from becoming eviction
// candidates.
type ActualUsageEvictor struct {
	logger             klog.Logger
	handle             frameworktypes.Handle
	args               *ActualUsageEvictorArgs
	includedNamespaces sets.Set[string]
	excludedNamespaces sets.Set[string]
	labelSelector      labels.Selector
}

var _ frameworktypes.EvictorPlugin = &ActualUsageEvictor{}

// New builds an ActualUsageEvictor.
func New(ctx context.Context, args runtime.Object, handle frameworktypes.Handle) (frameworktypes.Plugin, error) {
	a, ok := args.(*ActualUsageEvictorArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type ActualUsageEvictorArgs, got %T", args)
	}

	var selector labels.Selector
	if a.LabelSelector != nil {
		var err error
		selector, err = metav1.LabelSelectorAsSelector(a.LabelSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid labelSelector: %w", err)
		}
	}

	var included, excluded sets.Set[string]
	if a.Namespaces != nil {
		included = sets.New(a.Namespaces.Include...)
		excluded = sets.New(a.Namespaces.Exclude...)
	}

	return &ActualUsageEvictor{
		logger:             klog.FromContext(ctx).WithValues("plugin", PluginName),
		handle:             handle,
		args:               a,
		includedNamespaces: included,
		excludedNamespaces: excluded,
		labelSelector:      selector,
	}, nil
}

func (a *ActualUsageEvictor) Name() string {
	return PluginName
}

// Filter is a no-op. Runtime usage is evaluated at PreEvictionFilter.
func (a *ActualUsageEvictor) Filter(_ *v1.Pod) bool {
	return true
}

// PreEvictionFilter allows eviction only when both CPU and memory usage ratios
// are below their configured thresholds.
func (a *ActualUsageEvictor) PreEvictionFilter(pod *v1.Pod) bool {
	if !a.inScope(pod) {
		return true
	}

	collector := a.handle.MetricsCollector()
	if collector == nil || collector.MetricsClient() == nil {
		a.logger.Error(fmt.Errorf("metrics collector or client is nil"), "Metrics client unavailable, blocking eviction", "pod", klog.KObj(pod))
		return false
	}

	metrics, err := collector.MetricsClient().MetricsV1beta1().PodMetricses(pod.Namespace).Get(context.TODO(), pod.Name, metav1.GetOptions{})
	if err != nil {
		a.logger.Error(err, "Unable to read pod metrics, blocking eviction", "pod", klog.KObj(pod))
		return false
	}

	requestCPU, requestMemory, usageCPU, usageMemory, err := aggregatePodResources(pod, metrics.Containers)
	if err != nil {
		a.logger.Error(err, "Incomplete pod metrics, blocking eviction", "pod", klog.KObj(pod))
		return false
	}

	cpuRatio, cpuBusy := usageRatio(requestCPU, usageCPU, a.args.CPUUsageThreshold)
	memoryRatio, memoryBusy := usageRatio(requestMemory, usageMemory, a.args.MemoryUsageThreshold)
	if cpuBusy || memoryBusy {
		a.logger.V(1).Info("Pod usage is above eviction threshold, blocking eviction",
			"pod", klog.KObj(pod),
			"cpuUsageMilli", usageCPU,
			"cpuRequestMilli", requestCPU,
			"cpuRatio", cpuRatio,
			"cpuThreshold", a.args.CPUUsageThreshold,
			"memoryUsageBytes", usageMemory,
			"memoryRequestBytes", requestMemory,
			"memoryRatio", memoryRatio,
			"memoryThreshold", a.args.MemoryUsageThreshold,
		)
		return false
	}

	a.logger.V(2).Info("Pod usage is below eviction thresholds, allowing eviction",
		"pod", klog.KObj(pod),
		"cpuRatio", cpuRatio,
		"cpuThreshold", a.args.CPUUsageThreshold,
		"memoryRatio", memoryRatio,
		"memoryThreshold", a.args.MemoryUsageThreshold,
	)
	return true
}

func (a *ActualUsageEvictor) inScope(pod *v1.Pod) bool {
	if len(a.includedNamespaces) > 0 && !a.includedNamespaces.Has(pod.Namespace) {
		return false
	}
	if len(a.excludedNamespaces) > 0 && a.excludedNamespaces.Has(pod.Namespace) {
		return false
	}
	if a.labelSelector != nil && !a.labelSelector.Matches(labels.Set(pod.Labels)) {
		return false
	}
	return true
}

func aggregatePodResources(pod *v1.Pod, containerMetrics []metricsv1beta1.ContainerMetrics) (requestCPU, requestMemory, usageCPU, usageMemory int64, err error) {
	metricsByContainer := make(map[string]v1.ResourceList, len(containerMetrics))
	for _, metrics := range containerMetrics {
		metricsByContainer[metrics.Name] = metrics.Usage
	}

	for _, container := range pod.Spec.Containers {
		requestCPU += container.Resources.Requests.Cpu().MilliValue()
		requestMemory += container.Resources.Requests.Memory().Value()

		usage, ok := metricsByContainer[container.Name]
		if !ok {
			return 0, 0, 0, 0, fmt.Errorf("container %q has no metrics", container.Name)
		}
		cpu, ok := usage[v1.ResourceCPU]
		if !ok {
			return 0, 0, 0, 0, fmt.Errorf("container %q metrics have no CPU usage", container.Name)
		}
		memory, ok := usage[v1.ResourceMemory]
		if !ok {
			return 0, 0, 0, 0, fmt.Errorf("container %q metrics have no memory usage", container.Name)
		}
		usageCPU += cpu.MilliValue()
		usageMemory += memory.Value()
	}

	return requestCPU, requestMemory, usageCPU, usageMemory, nil
}

func usageRatio(request, usage int64, threshold float64) (float64, bool) {
	if request == 0 {
		if usage > 0 {
			return math.Inf(1), true
		}
		return 0, false
	}
	ratio := float64(usage) / float64(request)
	return ratio, ratio >= threshold
}
