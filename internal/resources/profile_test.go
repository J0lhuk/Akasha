package resources

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestBoundedParallel(t *testing.T) {
	var active, peak, completed atomic.Int32
	Parallel(context.Background(), 200, 3, func(int) {
		n := active.Add(1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		completed.Add(1)
		active.Add(-1)
	})
	if peak.Load() > 3 || completed.Load() != 200 {
		t.Fatal(peak.Load(), completed.Load())
	}
	p := Default()
	if p.Workers < 1 || p.Workers > p.AvailableThreads || p.VectorCacheBytes > p.GoMemoryLimit {
		t.Fatal(p)
	}
}
