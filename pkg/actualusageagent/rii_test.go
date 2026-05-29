package actualusageagent

import (
	"math"
	"testing"
)

func TestComputeRII(t *testing.T) {
	tests := []struct {
		name     string
		cpuUsed  int64
		memUsed  int64
		cpuCap   int64
		memCap   int64
		expected float64
	}{
		{name: "cpu heavy", cpuUsed: 1000, memUsed: 256, cpuCap: 2000, memCap: 1024, expected: 0.25},
		{name: "memory heavy", cpuUsed: 500, memUsed: 768, cpuCap: 2000, memCap: 1024, expected: -0.50},
		{name: "balanced", cpuUsed: 1000, memUsed: 512, cpuCap: 2000, memCap: 1024, expected: 0},
		{name: "invalid capacity", cpuUsed: 1000, memUsed: 512, cpuCap: 0, memCap: 1024, expected: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeRII(tt.cpuUsed, tt.memUsed, tt.cpuCap, tt.memCap)
			if math.Abs(got-tt.expected) > 0.000001 {
				t.Fatalf("ComputeRII() = %f, want %f", got, tt.expected)
			}
		})
	}
}

func TestIsFragmented(t *testing.T) {
	if !IsFragmented(-0.30, 0.20) {
		t.Fatalf("expected negative RII beyond threshold to be fragmented")
	}
	if IsFragmented(0.20, 0.20) {
		t.Fatalf("expected threshold boundary to not be fragmented")
	}
}
