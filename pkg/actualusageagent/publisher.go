package actualusageagent

import (
	"context"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	PublishTargetNone            = "none"
	PublishTargetNodeAnnotations = "node-annotations"

	AnnotationPrefix          = "descheduler.thesis/"
	AnnotationCPUUsedMilli    = AnnotationPrefix + "actual-cpu-milli"
	AnnotationMemoryUsedBytes = AnnotationPrefix + "actual-memory-bytes"
	AnnotationRawCPUUsedMilli = AnnotationPrefix + "raw-cpu-milli"
	AnnotationRawMemoryBytes  = AnnotationPrefix + "raw-memory-bytes"
	AnnotationRIIScore        = AnnotationPrefix + "rii"
	AnnotationTimestamp       = AnnotationPrefix + "timestamp"
	AnnotationSmoothingMethod = AnnotationPrefix + "smoothing-method"
	AnnotationEWMABeta        = AnnotationPrefix + "ewma-beta"
	AnnotationMetricsSource   = AnnotationPrefix + "metrics-source"
)

func PublishSnapshot(ctx context.Context, kube kubernetes.Interface, target string, snapshot Snapshot) error {
	switch target {
	case "", PublishTargetNone:
		return nil
	case PublishTargetNodeAnnotations:
		return publishNodeAnnotations(ctx, kube, snapshot)
	default:
		return nil
	}
}

func publishNodeAnnotations(ctx context.Context, kube kubernetes.Interface, snapshot Snapshot) error {
	for _, nodeSnapshot := range snapshot.Nodes {
		node, err := kube.CoreV1().Nodes().Get(ctx, nodeSnapshot.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		copy := node.DeepCopy()
		if copy.Annotations == nil {
			copy.Annotations = map[string]string{}
		}
		copy.Annotations[AnnotationCPUUsedMilli] = strconv.FormatInt(nodeSnapshot.CPUUsedMilli, 10)
		copy.Annotations[AnnotationMemoryUsedBytes] = strconv.FormatInt(nodeSnapshot.MemoryUsedB, 10)
		copy.Annotations[AnnotationRawCPUUsedMilli] = strconv.FormatInt(nodeSnapshot.RawCPUUsedMilli, 10)
		copy.Annotations[AnnotationRawMemoryBytes] = strconv.FormatInt(nodeSnapshot.RawMemoryUsedB, 10)
		copy.Annotations[AnnotationRIIScore] = strconv.FormatFloat(nodeSnapshot.RII, 'f', 6, 64)
		copy.Annotations[AnnotationTimestamp] = snapshot.Timestamp.Format(timeFormatRFC3339)
		copy.Annotations[AnnotationSmoothingMethod] = snapshot.SmoothingMethod
		copy.Annotations[AnnotationEWMABeta] = strconv.FormatFloat(snapshot.EWMABeta, 'f', 3, 64)
		copy.Annotations[AnnotationMetricsSource] = snapshot.MetricsSource
		if _, err := kube.CoreV1().Nodes().Update(ctx, copy, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
