package Util

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/dimagog/bufa/internal/contract"
)

// Records the highest number of concurrent enter/leave pairs; the sleep widens the overlap window.
type peakTracker struct{ inflight, peak atomic.Int32 }

func (pt *peakTracker) enter() {
	cur := pt.inflight.Add(1)
	for {
		old := pt.peak.Load()
		if cur <= old || pt.peak.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(time.Millisecond)
}

func (pt *peakTracker) leave() { pt.inflight.Add(-1) }

func TestRoutinePool_RunsAllTasksWithinLimit(t *testing.T) {
	const P = 3
	const N = 100
	p := NewRoutinePoolWithParallelism(P)
	var done atomic.Int32
	var pt peakTracker
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		p.Go(func() {
			defer wg.Done()
			pt.enter()
			defer pt.leave()
			done.Add(1)
		})
	}
	wg.Wait()
	if done.Load() != N {
		t.Errorf("done = %d, want %d", done.Load(), N)
	}
	if pt.peak.Load() > P {
		t.Errorf("peak inflight = %d, want <= %d", pt.peak.Load(), P)
	}
}

func TestRoutinePool_GoBlocksWhenSlotsBusy(t *testing.T) {
	const P = 2
	p := NewRoutinePoolWithParallelism(P)
	release := make(chan Nothing)
	var running sync.WaitGroup
	running.Add(P)
	for range P {
		p.Go(func() {
			running.Done()
			<-release
		})
	}
	running.Wait()
	started := make(chan Nothing)
	go p.Go(func() { close(started) })
	select {
	case <-started:
		t.Fatal("task P+1 started while all slots were busy")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("task P+1 never started after slots were freed")
	}
}

func TestRoutinePool_Parallelism(t *testing.T) {
	if p := NewRoutinePool(); p.Parallelism() != DefaultParallelism || cap(p.slots) != DefaultParallelism {
		t.Errorf("default: Parallelism = %d, cap = %d, want %d", p.Parallelism(), cap(p.slots), DefaultParallelism)
	}
	if p := NewRoutinePoolWithParallelism(1); p.Parallelism() != 1 || cap(p.slots) != 1 {
		t.Errorf("serial: Parallelism = %d, cap = %d, want 1", p.Parallelism(), cap(p.slots))
	}
	err := c.Rescue(func() { NewRoutinePoolWithParallelism(0) })
	if err == nil || !strings.Contains(err.Error(), "parallelism") {
		t.Errorf("parallelism 0: err = %v, want a precondition failure", err)
	}
}
