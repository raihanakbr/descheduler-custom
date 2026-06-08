package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultMaxCPUUnits      = 5000
	defaultIterationsPerCPU = 200000
	defaultMaxMemMB         = 128
	defaultMaxHoldMs        = 5000
	defaultMaxInflight      = 64
	defaultMaxTotalAlloc    = 0 // 0 = disabled
)

var (
	maxCPUUnits      = envInt("MAX_CPU_UNITS", envInt("MAX_CPU_MS", defaultMaxCPUUnits))
	iterationsPerCPU = envInt("ITERATIONS_PER_CPU_UNIT", defaultIterationsPerCPU)
	maxMemMB         = envInt("MAX_MEM_MB", defaultMaxMemMB)
	maxHoldMs        = envInt("MAX_HOLD_MS", defaultMaxHoldMs)
	maxInflight      = envInt("MAX_INFLIGHT", defaultMaxInflight)
	maxTotalAlloc    = envInt("MAX_TOTAL_ALLOC_MB", defaultMaxTotalAlloc)
	inflight         int64
	totalAllocMB     int64
	sem              chan struct{}
)

type workResponse struct {
	OK                   bool   `json:"ok"`
	Path                 string `json:"path"`
	CPUUnits             int    `json:"cpu_units"`
	IterationsPerCPUUnit int    `json:"iterations_per_cpu_unit"`
	Iterations           uint64 `json:"iterations"`
	MemMB                int    `json:"mem_mb"`
	HoldMs               int    `json:"hold_ms"`
	Sink                 uint64 `json:"sink"`
	Inflight             int64  `json:"inflight"`
	Duration             string `json:"duration"`
	GoMaxProc            int    `json:"gomaxprocs"`
}

func main() {
	sem = make(chan struct{}, maxInflight)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", workHandler)

	addr := ":" + envString("PORT", "8080")
	log.Printf("starting workload-http-fixed on %s max_cpu_units=%d iterations_per_cpu_unit=%d max_mem_mb=%d max_hold_ms=%d max_inflight=%d max_total_alloc_mb=%d",
		addr, maxCPUUnits, iterationsPerCPU, maxMemMB, maxHoldMs, maxInflight, maxTotalAlloc)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func workHandler(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/work") {
		http.NotFound(w, r)
		return
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		http.Error(w, "too many in-flight work requests", http.StatusTooManyRequests)
		return
	}

	start := time.Now()
	currentInflight := atomic.AddInt64(&inflight, 1)
	defer atomic.AddInt64(&inflight, -1)

	cpuUnits := boundedQueryInt(r, "cpu_units", -1, maxCPUUnits)
	if cpuUnits < 0 {
		// Backward-compatible with existing k6 scripts. In this workload,
		// cpu_ms means fixed CPU work units, not wall-clock milliseconds.
		cpuUnits = boundedQueryInt(r, "cpu_ms", 0, maxCPUUnits)
	}
	memMB := boundedQueryInt(r, "mem_mb", 0, maxMemMB)
	holdMs := boundedQueryInt(r, "hold_ms", 0, maxHoldMs)

	if maxTotalAlloc > 0 && memMB > 0 {
		current := atomic.LoadInt64(&totalAllocMB)
		if current+int64(memMB) > int64(maxTotalAlloc) {
			http.Error(w, "total memory budget exceeded", http.StatusTooManyRequests)
			return
		}
		atomic.AddInt64(&totalAllocMB, int64(memMB))
		defer atomic.AddInt64(&totalAllocMB, -int64(memMB))
	}

	buf := allocateCommitted(memMB)
	iterations := uint64(cpuUnits) * uint64(iterationsPerCPU)
	sink := burnFixedCPU(iterations)
	if holdMs > 0 {
		time.Sleep(time.Duration(holdMs) * time.Millisecond)
	}
	runtime.KeepAlive(buf)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(workResponse{
		OK:                   true,
		Path:                 r.URL.Path,
		CPUUnits:             cpuUnits,
		IterationsPerCPUUnit: iterationsPerCPU,
		Iterations:           iterations,
		MemMB:                memMB,
		HoldMs:               holdMs,
		Sink:                 sink,
		Inflight:             currentInflight,
		Duration:             time.Since(start).String(),
		GoMaxProc:            runtime.GOMAXPROCS(0),
	})
}

func allocateCommitted(memMB int) []byte {
	if memMB <= 0 {
		return nil
	}
	buf := make([]byte, memMB*1024*1024)
	for i := 0; i < len(buf); i += 4096 {
		buf[i] = byte(i)
	}
	return buf
}

func burnFixedCPU(iterations uint64) uint64 {
	x := uint64(1469598103934665603)
	for i := uint64(0); i < iterations; i++ {
		x ^= i + 0x9e3779b97f4a7c15
		x *= 1099511628211
		x ^= x >> 33
		x *= 0xff51afd7ed558ccd
		x ^= x >> 33
	}
	return x
}

func boundedQueryInt(r *http.Request, key string, fallback, max int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
