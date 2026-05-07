package actualusageagent

import "time"

const (
	MetricsSourceKubernetesMetrics = "metrics-server"
	RemediationModeReport          = "report"
)

type Config struct {
	Kubeconfig              string
	MetricsSource           string
	Interval                time.Duration
	RunOnce                 bool
	OutputDir               string
	NodeFragmentationThresh float64
	Namespace               string
	IncludeControlPlane     bool
	RemediationMode         string
}

type Snapshot struct {
	Timestamp              time.Time      `json:"timestamp"`
	MetricsSource          string         `json:"metricsSource"`
	Threshold              float64        `json:"fragmentationThreshold"`
	NodeCount              int            `json:"nodeCount"`
	PodCount               int            `json:"podCount"`
	FragmentedNodeCount    int            `json:"fragmentedNodeCount"`
	Nodes                  []NodeSnapshot `json:"nodes"`
	Pods                   []PodSnapshot  `json:"pods"`
	RemediationMode        string         `json:"remediationMode"`
	RemediationRecommended bool           `json:"remediationRecommended"`
	Recommendation         string         `json:"recommendation,omitempty"`
}

type NodeSnapshot struct {
	Name          string  `json:"name"`
	CPUUsedMilli  int64   `json:"cpuUsedMilli"`
	MemoryUsedB   int64   `json:"memoryUsedBytes"`
	CPUCapacityM  int64   `json:"cpuCapacityMilli"`
	MemoryCapB    int64   `json:"memoryCapacityBytes"`
	CPUUsageRatio float64 `json:"cpuUsageRatio"`
	MemUsageRatio float64 `json:"memoryUsageRatio"`
	RII           float64 `json:"rii"`
	Fragmented    bool    `json:"fragmented"`
}

type PodSnapshot struct {
	Namespace    string `json:"namespace"`
	Name         string `json:"name"`
	NodeName     string `json:"nodeName"`
	CPUUsedMilli int64  `json:"cpuUsedMilli"`
	MemoryUsedB  int64  `json:"memoryUsedBytes"`
}
