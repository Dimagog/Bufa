package Util

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/dimagog/bufa/internal/contract"
)

// The documented usage shape, driven under Rescue so Cleanup's re-panic becomes the returned error.
func runForkJoin[V any](fj *ForkJoin[V], body func()) (results []V, err error) {
	err = c.Rescue(func() {
		defer fj.Cleanup()
		body()
		results = fj.Results()
	})
	return
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Errorf("err = %v, want one containing %q", err, substr)
	}
}

// A reaped collector closes doneCh; a leaked one never does. Returns the cause recorded by then.
func wantReaped(t *testing.T, fj *ForkJoin[int]) error {
	t.Helper()
	select {
	case <-fj.doneCh:
		return fj.Err()
	case <-time.After(5 * time.Second):
		t.Fatal("collector was not reaped")
		return nil
	}
}

func TestForkJoin_CollectsWorkAndDirectResults(t *testing.T) {
	const N = 100
	fj := ForkJoinBuilder[int]().Parallelism(3).Build()
	got, err := runForkJoin(fj, func() {
		for i := range N {
			if i%10 == 0 {
				fj.AddResult(i)
			} else {
				fj.Go(func() int { return i })
			}
		}
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	slices.Sort(got)
	want := make([]int, N)
	for i := range want {
		want[i] = i
	}
	if !slices.Equal(got, want) {
		t.Errorf("results = %v, want 0..%d", got, N-1)
	}
}

func TestForkJoin_SerialWithCapacity(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(1).Capacity(8).Build()
	got, err := runForkJoin(fj, func() {
		for i := range 4 {
			fj.Go(func() int { return i })
		}
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{0, 1, 2, 3}) {
		t.Errorf("results = %v, want [0 1 2 3]", got)
	}
	if cap(got) < 8 {
		t.Errorf("cap = %d, want >= 8 from Capacity", cap(got))
	}
}

func TestForkJoin_WorkerFailurePropagates(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(2).Build()
	got, err := runForkJoin(fj, func() {
		fj.Go(func() int { return 1 })
		fj.Go(func() int { c.Fail("boom"); return 0 })
	})
	wantErr(t, err, "boom")
	wantErr(t, fj.Err(), "boom")
	if got != nil {
		t.Errorf("results = %v, want none on failure", got)
	}
}

func TestForkJoin_FirstErrorWins(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(2).Build()
	_, err := runForkJoin(fj, func() {
		fj.Go(func() int {
			for !fj.Stopped() {
				runtime.Gosched()
			}
			c.Fail("second")
			return 0
		})
		fj.Go(func() int { c.Fail("first"); return 0 })
	})
	wantErr(t, err, "first")
	if strings.Contains(err.Error(), "second") {
		t.Errorf("err = %v, want the first failure only", err)
	}
}

// Both main-thread adds throw the recorded error; the rejected work never runs.
func wantAddsRejected(t *testing.T, fj *ForkJoin[int], substr string) {
	t.Helper()
	var ran atomic.Bool
	wantErr(t, c.Rescue(func() { fj.Go(func() int { ran.Store(true); return 0 }) }), substr)
	wantErr(t, c.Rescue(func() { fj.AddResult(1) }), substr)
	if ran.Load() {
		t.Error("work accepted after failure")
	}
}

func TestForkJoin_AddsPanicAfterWorkerFailure(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(2).Build()
	_, err := runForkJoin(fj, func() {
		fj.Go(func() int { c.Fail("boom"); return 0 })
		for !fj.Stopped() {
			runtime.Gosched()
		}
		wantAddsRejected(t, fj, "boom")
	})
	wantErr(t, err, "boom")
}

func TestForkJoin_AddsPanicAfterExternalStop(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(2).Build()
	_, err := runForkJoin(fj, func() {
		fj.Go(func() int { return 1 })
		fj.Stop(errors.New("cancelled"))
		wantAddsRejected(t, fj, "cancelled")
	})
	wantErr(t, err, "cancelled")
}

func TestForkJoin_AddsPanicAfterResults(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(2).Build()
	err := c.Rescue(func() {
		defer fj.Cleanup()
		fj.Go(func() int { return 1 })
		if got := fj.Results(); !slices.Equal(got, []int{1}) {
			t.Errorf("results = %v, want [1]", got)
		}
		wantAddsRejected(t, fj, "already collected")
		c.Fail("later")
	})
	wantErr(t, err, "later")
	wantErr(t, c.Rescue(func() { fj.Results() }), "already collected")
	fj.shutdown() // a second close would crash the process
}

// Workers observing a sibling's failure must bail quietly: a panic in a pool goroutine has no recover
// above it and kills the process. One worker finishes after the failure, another starts after it.
func TestForkJoin_LateWorkersAfterFailureDoNotCrash(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(2).Build()
	failGate := make(chan Nothing)
	_, err := runForkJoin(fj, func() {
		fj.Go(func() int {
			for !fj.Stopped() {
				runtime.Gosched()
			}
			return 1 // its result send happens after the failure
		})
		fj.Go(func() int { <-failGate; c.Fail("boom"); return 0 })
		go func() {
			time.Sleep(20 * time.Millisecond) // let the third Go park on a slot
			close(failGate)
		}()
		fj.Go(func() int { return 2 }) // admitted only once the failure frees a slot
	})
	wantErr(t, err, "boom")
}

func TestForkJoin_CleanupOnMainPanicReaps(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(2).Build()
	err := c.Rescue(func() {
		defer fj.Cleanup()
		fj.Go(func() int { // in flight at the panic; Cleanup's reap waits for it
			for !fj.Stopped() {
				runtime.Gosched()
			}
			return 1
		})
		c.Fail("main")
	})
	wantErr(t, err, "main")
	wantErr(t, fj.Err(), "main")
	wantErr(t, wantReaped(t, fj), "main")
}

func TestForkJoin_CleanupWithoutResultsReaps(t *testing.T) {
	fj := ForkJoinBuilder[int]().Parallelism(2).Build()
	func() {
		defer fj.Cleanup()
		fj.Go(func() int { return 1 })
	}()
	if err := wantReaped(t, fj); err != errAbandoned {
		t.Errorf("collector err = %v, want errAbandoned", err)
	}
	if fj.Err() != errAbandoned {
		t.Errorf("Err = %v, want errAbandoned: the ctx node must be released", fj.Err())
	}
}

// The zero builder is the default; a chain never mutates its source, so one base builder serves many Builds.
func TestForkJoinBuilder_DefaultsAndValueSemantics(t *testing.T) {
	base := ForkJoinBuilder[int]()
	sized := base.Capacity(5).Parallelism(1)
	if base != (ForkJoinBuilder[int]()) {
		t.Errorf("chain mutated its source: %+v", base)
	}
	fj := NewForkJoin[int]()
	if cap(fj.resultCh) != DefaultParallelism || fj.pool.Parallelism() != DefaultParallelism {
		t.Errorf("default parallelism = %d/%d, want %d", cap(fj.resultCh), fj.pool.Parallelism(), DefaultParallelism)
	}
	if fj.Stopped() {
		t.Errorf("default ctx already cancelled: %v", fj.Err())
	}
	fj.Cleanup()
	fj = sized.Build()
	if cap(fj.results) != 5 || fj.pool.Parallelism() != 1 {
		t.Errorf("sized: cap = %d, parallelism = %d, want 5/1", cap(fj.results), fj.pool.Parallelism())
	}
	fj.Cleanup()
}

func TestForkJoin_Preconditions(t *testing.T) {
	for name, fn := range map[string]func(){
		"parallelism 0": func() { ForkJoinBuilder[int]().Parallelism(0).Build() },
		"capacity -1":   func() { ForkJoinBuilder[int]().Parallelism(2).Capacity(-1).Build() },
		"Stop nil":      func() { ForkJoinBuilder[int]().Parallelism(2).Build().Stop(nil) },
		"ctx nil":       func() { ForkJoinBuilder[int]().Context(nil).Build() },
		"pool nil":      func() { ForkJoinBuilder[int]().Pool(nil).Build() },
		"pool + parallelism": func() {
			ForkJoinBuilder[int]().Pool(NewRoutinePoolWithParallelism(1)).Parallelism(1).Build()
		},
	} {
		if err := c.Rescue(fn); err == nil {
			t.Errorf("%s: no precondition failure", name)
		}
	}
}

func TestForkJoin_SharedPoolCapsAndIsolatesResults(t *testing.T) {
	const P = 2
	const N = 30
	pool := NewRoutinePoolWithParallelism(P)
	a := ForkJoinBuilder[int]().Pool(pool).Build()
	b := ForkJoinBuilder[int]().Pool(pool).Build()
	if cap(a.resultCh) != P || cap(b.resultCh) != P {
		t.Errorf("resultCh caps = %d/%d, want the pool's %d", cap(a.resultCh), cap(b.resultCh), P)
	}
	var pt peakTracker
	var gotA []int
	var errA error
	gotB, errB := runForkJoin(b, func() {
		gotA, errA = runForkJoin(a, func() {
			for i := range N {
				a.Go(func() int { pt.enter(); defer pt.leave(); return i })
				b.Go(func() int { pt.enter(); defer pt.leave(); return 100 + i })
			}
		})
	})
	if errA != nil || errB != nil {
		t.Fatalf("errs = %v / %v", errA, errB)
	}
	slices.Sort(gotA)
	slices.Sort(gotB)
	wantA := make([]int, N)
	wantB := make([]int, N)
	for i := range N {
		wantA[i] = i
		wantB[i] = 100 + i
	}
	if !slices.Equal(gotA, wantA) || !slices.Equal(gotB, wantB) {
		t.Errorf("results mixed or lost: a = %v, b = %v", gotA, gotB)
	}
	if pt.peak.Load() > P {
		t.Errorf("peak inflight across both = %d, want <= %d", pt.peak.Load(), P)
	}
}

func TestForkJoin_SharedPoolFailureIsolation(t *testing.T) {
	pool := NewRoutinePoolWithParallelism(2)
	ok := ForkJoinBuilder[int]().Pool(pool).Build()
	bad := ForkJoinBuilder[int]().Pool(pool).Build()
	gotOk, errOk := runForkJoin(ok, func() {
		_, errBad := runForkJoin(bad, func() {
			ok.Go(func() int { return 1 })
			bad.Go(func() int { c.Fail("boom"); return 0 })
			ok.Go(func() int { return 2 })
		})
		wantErr(t, errBad, "boom")
		if ok.Stopped() {
			t.Error("failure leaked into the sibling instance")
		}
	})
	if errOk != nil {
		t.Fatalf("sibling err = %v", errOk)
	}
	slices.Sort(gotOk)
	if !slices.Equal(gotOk, []int{1, 2}) {
		t.Errorf("sibling results = %v, want [1 2]", gotOk)
	}
}

// Hashing's recursion shape: an inner ForkJoin is created on the main thread while the outer's workers
// hold pool slots. One slot makes any slot-holding wait a deadlock, so passing proves main never holds one.
func TestForkJoin_SharedPoolNestedFromMain(t *testing.T) {
	pool := NewRoutinePoolWithParallelism(1)
	outer := ForkJoinBuilder[int]().Pool(pool).Build()
	got, err := runForkJoin(outer, func() {
		for i := range 3 {
			outer.Go(func() int { return i })
			inner := ForkJoinBuilder[int]().Pool(pool).Build()
			sub, err := runForkJoin(inner, func() {
				for j := range 3 {
					inner.Go(func() int { return 10*(i+1) + j })
				}
			})
			c.Check(err)
			sum := 0
			for _, v := range sub {
				sum += v
			}
			outer.AddResult(sum)
		}
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{0, 1, 2, 33, 63, 93}) {
		t.Errorf("results = %v, want [0 1 2 33 63 93]", got)
	}
}

func TestForkJoin_ParentCancelStopsChild(t *testing.T) {
	pool := NewRoutinePoolWithParallelism(2)
	parent := ForkJoinBuilder[int]().Pool(pool).Build()
	child := ForkJoinBuilder[int]().Pool(pool).Context(parent.Context()).Build()
	release := make(chan Nothing)
	_, err := runForkJoin(child, func() {
		child.Go(func() int { <-release; return 1 })
		parent.Stop(errors.New("parent failed"))
		if !child.Stopped() {
			t.Error("child not stopped by the parent's failure")
		}
		wantAddsRejected(t, child, "parent failed")
		close(release) // Results waits for the worker before throwing
	})
	wantErr(t, err, "parent failed")
	wantErr(t, wantReaped(t, child), "parent failed")
}

func TestForkJoin_ContextOutsideCancel(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	fj := ForkJoinBuilder[int]().Context(ctx).Build()
	_, err := runForkJoin(fj, func() {
		fj.Go(func() int { return 1 })
		cancel(errors.New("outside"))
		wantAddsRejected(t, fj, "outside")
	})
	wantErr(t, err, "outside")
}

func TestForkJoin_ChildFailureLeavesParent(t *testing.T) {
	pool := NewRoutinePoolWithParallelism(2)
	parent := ForkJoinBuilder[int]().Pool(pool).Build()
	got, err := runForkJoin(parent, func() {
		child := ForkJoinBuilder[int]().Pool(pool).Context(parent.Context()).Build()
		_, childErr := runForkJoin(child, func() {
			child.Go(func() int { c.Fail("child boom"); return 0 })
		})
		wantErr(t, childErr, "child boom")
		if parent.Stopped() {
			t.Error("child failure cancelled the parent")
		}
		parent.AddResult(7)
	})
	if err != nil {
		t.Fatalf("parent err = %v", err)
	}
	if !slices.Equal(got, []int{7}) {
		t.Errorf("parent results = %v, want [7]", got)
	}
}
