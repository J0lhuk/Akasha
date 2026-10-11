// Package resources sizes bounded workloads from the resources available to
// this process. Heap limits are soft Go limits; native inference is separate.
package resources

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
)

type Profile struct {
	LogicalCPUs      int    `json:"logical_cpus"`
	PhysicalCores    int    `json:"physical_cores"`
	CPUPackages      int    `json:"cpu_packages"`
	AvailableThreads int    `json:"available_threads"`
	TotalMemory      uint64 `json:"total_memory_bytes"`
	AvailableMemory  uint64 `json:"available_memory_bytes"`
	GoMemoryLimit    int64  `json:"go_soft_memory_limit_bytes"`
	NativeReserve    int64  `json:"native_inference_reserve_bytes"`
	Workers          int    `json:"workers"`
	InferenceThreads int    `json:"inference_threads"`
	ImportWindow     int    `json:"import_window_files"`
	VectorCacheBytes int64  `json:"vector_cache_budget_bytes"`
}

var once sync.Once
var selected Profile

func Default() Profile { once.Do(func() { selected = Detect() }); return selected }
func Detect() Profile {
	p := Profile{LogicalCPUs: runtime.NumCPU(), AvailableThreads: runtime.GOMAXPROCS(0)}
	detectOS(&p)
	available := p.AvailableMemory
	if available == 0 {
		available = 2 << 30
	}
	p.GoMemoryLimit = int64(min(uint64(2<<30), max(uint64(128<<20), available/4)))
	if n, e := strconv.ParseInt(os.Getenv("AKASHA_MEMORY_MB"), 10, 64); e == nil && n >= 128 {
		p.GoMemoryLimit = min(n<<20, int64(available/2))
	}
	p.Workers = max(1, min(4, p.AvailableThreads/2, int(p.GoMemoryLimit/(64<<20))))
	if n, e := strconv.Atoi(os.Getenv("AKASHA_WORKERS")); e == nil && n > 0 {
		p.Workers = min(n, max(1, p.AvailableThreads), max(1, int(p.GoMemoryLimit/(64<<20))))
	}
	p.InferenceThreads = max(1, min(4, p.AvailableThreads/2))
	p.ImportWindow = p.Workers * 2
	p.VectorCacheBytes = min(128<<20, p.GoMemoryLimit/8)
	p.NativeReserve = 512 << 20
	return p
}
func Configure() Profile {
	p := Default()
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(p.GoMemoryLimit)
	}
	return p
}

// Parallel starts at most workers goroutines and has no unbounded queue. The
// caller owns disjoint output slots. Cancellation stops scheduling new jobs.
func Parallel(ctx context.Context, n, workers int, fn func(int)) {
	workers = max(1, min(n, workers))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range jobs {
				fn(i)
			}
		})
	}
loop:
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
}
