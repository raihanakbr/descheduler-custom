package actualusageagent

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func WriteSnapshot(outputDir string, snapshot Snapshot) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	if err := appendJSONL(filepath.Join(outputDir, "snapshots.jsonl"), snapshot); err != nil {
		return err
	}
	return appendNodeCSV(filepath.Join(outputDir, "node_rii_history.csv"), snapshot)
}

func appendJSONL(path string, snapshot Snapshot) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func appendNodeCSV(path string, snapshot Snapshot) error {
	newFile := false
	if _, err := os.Stat(path); os.IsNotExist(err) {
		newFile = true
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if newFile {
		if err := w.Write([]string{"timestamp", "node", "metrics_source", "smoothing_method", "ewma_beta", "raw_cpu_used_milli", "raw_memory_used_bytes", "cpu_used_milli", "memory_used_bytes", "cpu_capacity_milli", "memory_capacity_bytes", "cpu_usage_ratio", "memory_usage_ratio", "rii", "fragmented"}); err != nil {
			return err
		}
	}
	for _, node := range snapshot.Nodes {
		row := []string{
			snapshot.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			node.Name,
			snapshot.MetricsSource,
			snapshot.SmoothingMethod,
			fmt.Sprintf("%.3f", snapshot.EWMABeta),
			strconv.FormatInt(node.RawCPUUsedMilli, 10),
			strconv.FormatInt(node.RawMemoryUsedB, 10),
			strconv.FormatInt(node.CPUUsedMilli, 10),
			strconv.FormatInt(node.MemoryUsedB, 10),
			strconv.FormatInt(node.CPUCapacityM, 10),
			strconv.FormatInt(node.MemoryCapB, 10),
			fmt.Sprintf("%.6f", node.CPUUsageRatio),
			fmt.Sprintf("%.6f", node.MemUsageRatio),
			fmt.Sprintf("%.6f", node.RII),
			strconv.FormatBool(node.Fragmented),
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	return w.Error()
}
