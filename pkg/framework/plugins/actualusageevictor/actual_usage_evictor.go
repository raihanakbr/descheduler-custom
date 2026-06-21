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

	resources, err := aggregatePodResources(pod, metrics.Containers)
	if err != nil {
		a.logger.Error(err, "Incomplete pod metrics, blocking eviction", "pod", klog.KObj(pod))
		return false
	}

	cpuRatio, cpuBusy := a.evaluateResource(
		pod,
		v1.ResourceCPU,
		resources.requestCPU,
		resources.usageCPU,
		resources.cpuRequestMissing,
		a.args.CPUUsageThreshold,
	)
	memoryRatio, memoryBusy := a.evaluateResource(
		pod,
		v1.ResourceMemory,
		resources.requestMemory,
		resources.usageMemory,
		resources.memoryRequestMissing,
		a.args.MemoryUsageThreshold,
	)
	if cpuBusy || memoryBusy {
		a.logger.V(1).Info("Pod usage is above eviction threshold, blocking eviction",
			"pod", klog.KObj(pod),
			"cpuUsageMilli", resources.usageCPU,
			"cpuRequestMilli", resources.requestCPU,
			"cpuRequestMissing", resources.cpuRequestMissing,
			"cpuRatio", cpuRatio,
			"cpuThreshold", a.args.CPUUsageThreshold,
			"memoryUsageBytes", resources.usageMemory,
			"memoryRequestBytes", resources.requestMemory,
			"memoryRequestMissing", resources.memoryRequestMissing,
			"memoryRatio", memoryRatio,
			"memoryThreshold", a.args.MemoryUsageThreshold,
			"missingRequestPolicy", a.args.MissingRequestPolicy,
		)
		return false
	}

	a.logger.V(2).Info("Pod usage is below eviction thresholds, allowing eviction",
		"pod", klog.KObj(pod),
		"cpuRatio", cpuRatio,
		"cpuThreshold", a.args.CPUUsageThreshold,
		"memoryRatio", memoryRatio,
		"memoryThreshold", a.args.MemoryUsageThreshold,
		"missingRequestPolicy", a.args.MissingRequestPolicy,
	)
	return true
}

func (a *ActualUsageEvictor) evaluateResource(pod *v1.Pod, resourceName v1.ResourceName, request, usage int64, requestMissing bool, threshold float64) (float64, bool) {
	if requestMissing {
		block := a.args.MissingRequestPolicy == BlockMissingRequest
		a.logger.V(1).Info("Pod resource request is missing, applying configured policy",
			"pod", klog.KObj(pod),
			"resource", resourceName,
			"missingRequestPolicy", a.args.MissingRequestPolicy,
			"blocking", block,
		)
		return 0, block
	}
	return usageRatio(request, usage, threshold)
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

type podResources struct {
	requestCPU           int64
	requestMemory        int64
	usageCPU             int64
	usageMemory          int64
	cpuRequestMissing    bool
	memoryRequestMissing bool
}

func aggregatePodResources(pod *v1.Pod, containerMetrics []metricsv1beta1.ContainerMetrics) (podResources, error) {
	metricsByContainer := make(map[string]v1.ResourceList, len(containerMetrics))
	for _, metrics := range containerMetrics {
		metricsByContainer[metrics.Name] = metrics.Usage
	}

	var resources podResources
	for _, container := range pod.Spec.Containers {
		cpuRequest := container.Resources.Requests.Cpu()
		memoryRequest := container.Resources.Requests.Memory()
		if cpuRequest.IsZero() {
			resources.cpuRequestMissing = true
		} else {
			resources.requestCPU += cpuRequest.MilliValue()
		}
		if memoryRequest.IsZero() {
			resources.memoryRequestMissing = true
		} else {
			resources.requestMemory += memoryRequest.Value()
		}

		usage, ok := metricsByContainer[container.Name]
		if !ok {
			return podResources{}, fmt.Errorf("container %q has no metrics", container.Name)
		}
		cpu, ok := usage[v1.ResourceCPU]
		if !ok {
			return podResources{}, fmt.Errorf("container %q metrics have no CPU usage", container.Name)
		}
		memory, ok := usage[v1.ResourceMemory]
		if !ok {
			return podResources{}, fmt.Errorf("container %q metrics have no memory usage", container.Name)
		}
		resources.usageCPU += cpu.MilliValue()
		resources.usageMemory += memory.Value()
	}

	return resources, nil
}

func usageRatio(request, usage int64, threshold float64) (float64, bool) {
	ratio := float64(usage) / float64(request)
	return ratio, ratio >= threshold
}
