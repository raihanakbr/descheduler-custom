package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	defaultMaxCPUUnits      = 5000
	defaultIterationsPerCPU = 200000
	defaultMaxMemMB         = 160
	defaultMaxHoldMs        = 10000
	defaultMaxInflight      = 128
	defaultMaxTotalAlloc    = 640
)

var (
	maxCPUUnits      = envInt("MAX_CPU_UNITS", defaultMaxCPUUnits)
	iterationsPerCPU = envInt("ITERATIONS_PER_CPU_UNIT", defaultIterationsPerCPU)
	maxMemMB         = envInt("MAX_MEM_MB", defaultMaxMemMB)
	maxHoldMs        = envInt("MAX_HOLD_MS", defaultMaxHoldMs)
	maxInflight      = envInt("MAX_INFLIGHT", defaultMaxInflight)
	maxTotalAlloc    = envInt("MAX_TOTAL_ALLOC_MB", defaultMaxTotalAlloc)
	drainDelay       = time.Duration(envInt("DRAIN_DELAY_SECONDS", 5)) * time.Second
	shutdownTimeout  = time.Duration(envInt("SHUTDOWN_TIMEOUT_SECONDS", 25)) * time.Second
	podName          = envString("POD_NAME", "unknown")
	ready            atomic.Bool
	inflight         atomic.Int64
	totalAllocMB     atomic.Int64
	sem              chan struct{}
)

type workResponse struct {
	OK       bool   `json:"ok"`
	Pod      string `json:"pod"`
	CPUUnits int    `json:"cpu_units"`
	MemMB    int    `json:"mem_mb"`
	HoldMs   int    `json:"hold_ms"`
	Inflight int64  `json:"inflight"`
	Duration string `json:"duration"`
}

func main() {
	sem = make(chan struct{}, maxInflight)
	ready.Store(true)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthHandler)
	mux.HandleFunc("/readyz", readyHandler)
	mux.HandleFunc("/work", workHandler)

	server := &http.Server{
		Addr:              ":" + envString("PORT", "8080"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-stopCtx.Done()
		ready.Store(false)
		log.Printf("pod=%s entering drain delay=%s inflight=%d", podName, drainDelay, inflight.Load())
		time.Sleep(drainDelay)

		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		}
	}()

	log.Printf("pod=%s listening=%s max_inflight=%d drain_delay=%s shutdown_timeout=%s",
		podName, server.Addr, maxInflight, drainDelay, shutdownTimeout)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Printf("pod=%s stopped inflight=%d", podName, inflight.Load())
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func readyHandler(w http.ResponseWriter, _ *http.Request) {
	if !ready.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

func workHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/work" {
		http.NotFound(w, r)
		return
	}
	if !ready.Load() {
		http.Error(w, "draining", http.StatusServiceUnavailable)
		return
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	default:
		http.Error(w, "too many in-flight requests", http.StatusTooManyRequests)
		return
	}

	start := time.Now()
	currentInflight := inflight.Add(1)
	defer inflight.Add(-1)

	cpuUnits := boundedQueryInt(r, "cpu_units", 0, maxCPUUnits)
	memMB := boundedQueryInt(r, "mem_mb", 0, maxMemMB)
	holdMs := boundedQueryInt(r, "hold_ms", 0, maxHoldMs)

	if memMB > 0 {
		current := totalAllocMB.Load()
		if current+int64(memMB) > int64(maxTotalAlloc) {
			http.Error(w, "total memory budget exceeded", http.StatusTooManyRequests)
			return
		}
		totalAllocMB.Add(int64(memMB))
		defer totalAllocMB.Add(-int64(memMB))
	}

	buf := allocateCommitted(memMB)
	burnFixedCPU(uint64(cpuUnits) * uint64(iterationsPerCPU))
	if holdMs > 0 {
		time.Sleep(time.Duration(holdMs) * time.Millisecond)
	}
	runtime.KeepAlive(buf)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Workload-Pod", podName)
	_ = json.NewEncoder(w).Encode(workResponse{
		OK:       true,
		Pod:      podName,
		CPUUnits: cpuUnits,
		MemMB:    memMB,
		HoldMs:   holdMs,
		Inflight: currentInflight,
		Duration: time.Since(start).String(),
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
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
