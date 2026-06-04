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
	defaultMaxCPUMs       = 2000
	defaultMaxMemMB       = 128
	defaultMaxHoldMs      = 5000
	defaultMaxInflight    = 64
	defaultMaxTotalAlloc  = 0 // 0 = disabled
)

var (
	maxCPUMs       = envInt("MAX_CPU_MS", defaultMaxCPUMs)
	maxMemMB       = envInt("MAX_MEM_MB", defaultMaxMemMB)
	maxHoldMs      = envInt("MAX_HOLD_MS", defaultMaxHoldMs)
	maxInflight    = envInt("MAX_INFLIGHT", defaultMaxInflight)
	maxTotalAlloc  = envInt("MAX_TOTAL_ALLOC_MB", defaultMaxTotalAlloc)
	inflight       int64
	totalAllocMB   int64
	sem            chan struct{}
)

type workResponse struct {
	OK        bool   `json:"ok"`
	Path      string `json:"path"`
	CPUMs     int    `json:"cpu_ms"`
	MemMB     int    `json:"mem_mb"`
	HoldMs    int    `json:"hold_ms"`
	Sink      uint64 `json:"sink"`
	Inflight  int64  `json:"inflight"`
	Duration  string `json:"duration"`
	GoMaxProc int    `json:"gomaxprocs"`
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
	log.Printf("starting workload-http on %s max_cpu_ms=%d max_mem_mb=%d max_hold_ms=%d max_inflight=%d max_total_alloc_mb=%d",
		addr, maxCPUMs, maxMemMB, maxHoldMs, maxInflight, maxTotalAlloc)
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

	cpuMs := boundedQueryInt(r, "cpu_ms", 0, maxCPUMs)
	memMB := boundedQueryInt(r, "mem_mb", 0, maxMemMB)
	holdMs := boundedQueryInt(r, "hold_ms", 0, maxHoldMs)

	// Check total alloc budget before allocating
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
	sink := burnCPU(time.Duration(cpuMs) * time.Millisecond)
	if holdMs > 0 {
		time.Sleep(time.Duration(holdMs) * time.Millisecond)
	}
	runtime.KeepAlive(buf)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(workResponse{
		OK:        true,
		Path:      r.URL.Path,
		CPUMs:     cpuMs,
		MemMB:     memMB,
		HoldMs:    holdMs,
		Sink:      sink,
		Inflight:  currentInflight,
		Duration:  time.Since(start).String(),
		GoMaxProc: runtime.GOMAXPROCS(0),
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

func burnCPU(duration time.Duration) uint64 {
	if duration <= 0 {
		return 0
	}
	deadline := time.Now().Add(duration)
	x := uint64(1469598103934665603)
	for time.Now().Before(deadline) {
		x ^= uint64(time.Now().UnixNano())
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
