package actualusageagent

import "math"

func ComputeRII(cpuUsedMilli, memoryUsedBytes, cpuCapacityMilli, memoryCapacityBytes int64) float64 {
	if cpuCapacityMilli <= 0 || memoryCapacityBytes <= 0 {
		return 0
	}
	cpuRatio := float64(cpuUsedMilli) / float64(cpuCapacityMilli)
	memRatio := float64(memoryUsedBytes) / float64(memoryCapacityBytes)
	return cpuRatio - memRatio
}

func IsFragmented(rii, threshold float64) bool {
	return math.Abs(rii) > threshold
}
