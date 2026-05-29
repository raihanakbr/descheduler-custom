package actualusageagent

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func TestBuildSnapshotUsesActualMetricsAndRII(t *testing.T) {
	nodes := []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "worker-01"}, Status: v1.NodeStatus{Allocatable: allocatable("2000m", "1Gi")}},
		{ObjectMeta: metav1.ObjectMeta{Name: "controlplane", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}, Status: v1.NodeStatus{Allocatable: allocatable("2000m", "1Gi")}},
	}
	nodeMetrics := []metricsv1beta1.NodeMetrics{
		{ObjectMeta: metav1.ObjectMeta{Name: "worker-01"}, Usage: usage("1800m", "256Mi")},
		{ObjectMeta: metav1.ObjectMeta{Name: "controlplane"}, Usage: usage("100m", "256Mi")},
	}
	podMetrics := []metricsv1beta1.PodMetrics{
		{ObjectMeta: metav1.ObjectMeta{Name: "cpu-burner", Namespace: "rii-actual"}, Containers: []metricsv1beta1.ContainerMetrics{{Name: "main", Usage: usage("900m", "64Mi")}}},
	}
	podNodeByKey := map[string]string{"rii-actual/cpu-burner": "worker-01"}

	snapshot := buildSnapshot(nodes, nodeMetrics, podMetrics, podNodeByKey, nil, Config{NodeFragmentationThresh: 0.20, RemediationMode: RemediationModeReport, EWMABeta: DefaultEWMABeta})
	if snapshot.NodeCount != 1 {
		t.Fatalf("NodeCount = %d, want 1 worker node", snapshot.NodeCount)
	}
	if snapshot.PodCount != 1 {
		t.Fatalf("PodCount = %d, want 1", snapshot.PodCount)
	}
	if snapshot.Pods[0].NodeName != "worker-01" {
		t.Fatalf("pod NodeName = %q, want worker-01", snapshot.Pods[0].NodeName)
	}
	if !snapshot.Nodes[0].Fragmented || snapshot.FragmentedNodeCount != 1 || !snapshot.RemediationRecommended {
		t.Fatalf("expected worker to be fragmented and remediation to be recommended: %#v", snapshot)
	}
}

func allocatable(cpu, memory string) v1.ResourceList {
	return v1.ResourceList{
		v1.ResourceCPU:    resource.MustParse(cpu),
		v1.ResourceMemory: resource.MustParse(memory),
	}
}

func usage(cpu, memory string) v1.ResourceList {
	return v1.ResourceList{
		v1.ResourceCPU:    resource.MustParse(cpu),
		v1.ResourceMemory: resource.MustParse(memory),
	}
}

func TestBuildSnapshotAppliesEWMA(t *testing.T) {
	nodes := []v1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "worker-01"}, Status: v1.NodeStatus{Allocatable: allocatable("2000m", "1Gi")}},
	}
	podMetrics := []metricsv1beta1.PodMetrics{}
	podNodeByKey := map[string]string{}
	smoothed := map[string]smoothedNodeUsage{}

	firstMetrics := []metricsv1beta1.NodeMetrics{{ObjectMeta: metav1.ObjectMeta{Name: "worker-01"}, Usage: usage("1000m", "256Mi")}}
	first := buildSnapshot(nodes, firstMetrics, podMetrics, podNodeByKey, smoothed, Config{NodeFragmentationThresh: 0.20, RemediationMode: RemediationModeReport, EWMABeta: DefaultEWMABeta})
	if first.Nodes[0].CPUUsedMilli != 1000 || first.Nodes[0].RawCPUUsedMilli != 1000 {
		t.Fatalf("first snapshot should initialize EWMA from raw metrics: %#v", first.Nodes[0])
	}

	secondMetrics := []metricsv1beta1.NodeMetrics{{ObjectMeta: metav1.ObjectMeta{Name: "worker-01"}, Usage: usage("2000m", "512Mi")}}
	second := buildSnapshot(nodes, secondMetrics, podMetrics, podNodeByKey, smoothed, Config{NodeFragmentationThresh: 0.20, RemediationMode: RemediationModeReport, EWMABeta: DefaultEWMABeta})
	if second.Nodes[0].RawCPUUsedMilli != 2000 {
		t.Fatalf("raw CPU = %d, want 2000", second.Nodes[0].RawCPUUsedMilli)
	}
	if second.Nodes[0].CPUUsedMilli != 1100 {
		t.Fatalf("smoothed CPU = %d, want 1100", second.Nodes[0].CPUUsedMilli)
	}
}
