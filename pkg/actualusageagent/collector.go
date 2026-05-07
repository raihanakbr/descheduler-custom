package actualusageagent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"
)

type Collector struct {
	kube    kubernetes.Interface
	metrics metricsclientset.Interface
	config  Config
}

func NewCollector(kube kubernetes.Interface, metrics metricsclientset.Interface, config Config) *Collector {
	return &Collector{kube: kube, metrics: metrics, config: config}
}

func (c *Collector) Collect(ctx context.Context) (Snapshot, error) {
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Snapshot{}, err
	}
	nodeMetrics, err := c.metrics.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("list node metrics from metrics-server: %w", err)
	}
	podMetrics, err := c.metrics.MetricsV1beta1().PodMetricses(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("list pod metrics from metrics-server: %w", err)
	}
	pods, err := c.kube.CoreV1().Pods(c.config.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Snapshot{}, fmt.Errorf("list pods for node mapping: %w", err)
	}
	podNodeByKey := map[string]string{}
	for _, pod := range pods.Items {
		podNodeByKey[pod.Namespace+"/"+pod.Name] = pod.Spec.NodeName
	}

	return buildSnapshot(nodes.Items, nodeMetrics.Items, podMetrics.Items, podNodeByKey, c.config), nil
}

func buildSnapshot(nodes []v1.Node, nodeMetrics []metricsv1beta1.NodeMetrics, podMetrics []metricsv1beta1.PodMetrics, podNodeByKey map[string]string, config Config) Snapshot {
	nodeMetricsByName := map[string]v1.ResourceList{}
	for _, m := range nodeMetrics {
		nodeMetricsByName[m.Name] = m.Usage
	}

	snapshot := Snapshot{
		Timestamp:       time.Now().UTC(),
		MetricsSource:   MetricsSourceKubernetesMetrics,
		Threshold:       config.NodeFragmentationThresh,
		RemediationMode: config.RemediationMode,
	}

	for _, node := range nodes {
		if !config.IncludeControlPlane && isControlPlaneNode(node) {
			continue
		}
		usage, ok := nodeMetricsByName[node.Name]
		if !ok {
			continue
		}
		cpuCapacity := node.Status.Allocatable.Cpu().MilliValue()
		memoryCapacity := node.Status.Allocatable.Memory().Value()
		cpuUsed := usage.Cpu().MilliValue()
		memoryUsed := usage.Memory().Value()
		rii := ComputeRII(cpuUsed, memoryUsed, cpuCapacity, memoryCapacity)
		fragmented := IsFragmented(rii, config.NodeFragmentationThresh)
		if fragmented {
			snapshot.FragmentedNodeCount++
		}
		snapshot.Nodes = append(snapshot.Nodes, NodeSnapshot{
			Name:          node.Name,
			CPUUsedMilli:  cpuUsed,
			MemoryUsedB:   memoryUsed,
			CPUCapacityM:  cpuCapacity,
			MemoryCapB:    memoryCapacity,
			CPUUsageRatio: ratio(cpuUsed, cpuCapacity),
			MemUsageRatio: ratio(memoryUsed, memoryCapacity),
			RII:           rii,
			Fragmented:    fragmented,
		})
	}

	for _, pm := range podMetrics {
		var cpuUsed, memoryUsed int64
		for _, container := range pm.Containers {
			cpuUsed += container.Usage.Cpu().MilliValue()
			memoryUsed += container.Usage.Memory().Value()
		}
		snapshot.Pods = append(snapshot.Pods, PodSnapshot{
			Namespace:    pm.Namespace,
			Name:         pm.Name,
			NodeName:     podNodeByKey[pm.Namespace+"/"+pm.Name],
			CPUUsedMilli: cpuUsed,
			MemoryUsedB:  memoryUsed,
		})
	}

	sort.Slice(snapshot.Nodes, func(i, j int) bool { return snapshot.Nodes[i].Name < snapshot.Nodes[j].Name })
	sort.Slice(snapshot.Pods, func(i, j int) bool {
		if snapshot.Pods[i].Namespace == snapshot.Pods[j].Namespace {
			return snapshot.Pods[i].Name < snapshot.Pods[j].Name
		}
		return snapshot.Pods[i].Namespace < snapshot.Pods[j].Namespace
	})
	snapshot.NodeCount = len(snapshot.Nodes)
	snapshot.PodCount = len(snapshot.Pods)
	if snapshot.FragmentedNodeCount > 0 {
		snapshot.RemediationRecommended = true
		snapshot.Recommendation = fmt.Sprintf("%d node(s) above |RII| threshold %.3f; run descheduler ResourceDefragmentation policy if remediation is desired", snapshot.FragmentedNodeCount, snapshot.Threshold)
	}
	return snapshot
}

func isControlPlaneNode(node v1.Node) bool {
	for key := range node.Labels {
		if strings.EqualFold(key, "node-role.kubernetes.io/control-plane") || strings.EqualFold(key, "node-role.kubernetes.io/master") {
			return true
		}
	}
	return false
}

func ratio(used, capacity int64) float64 {
	if capacity <= 0 {
		return 0
	}
	return float64(used) / float64(capacity)
}
