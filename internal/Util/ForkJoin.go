package Util

import (
	"context"
	"errors"
	"sync"

	c "github.com/dimagog/bufa/internal/contract"
)

// Proper usage:
//
// fj = NewForkJoin[V]() // or ForkJoinBuilder[V]().Context(ctx).Pool(p).Capacity(n).Build()
// defer fj.Cleanup() // must be deferred directly: recover() sees nothing from a nested call
// fj.Go(func() V { ... })
// fj.AddResult(v)
// ...
// ... both main thread and workers may panic or call fj.Stop(err)
//     to signal failure and terminate early; cancelling ctx does the same from outside
// results := fj.Results()

var errCollected = errors.New("ForkJoin: Results already collected")
var errAbandoned = errors.New("ForkJoin: abandoned without Results")

type ForkJoin[V any] struct {
	pool     *RoutinePool
	results  []V
	resultCh chan V
	doneCh   chan Nothing    // closed by the collector once resultCh is drained
	ctx      context.Context // the first-error cell: cancelled with the cause, inherits the parent's
	cancel   context.CancelCauseFunc
	wg       sync.WaitGroup
	shutOnce sync.Once
}

func NewForkJoin[V any]() *ForkJoin[V] {
	return ForkJoinBuilder[V]().Build()
}

// Zero value builds the default: Background ctx, own pool of DefaultParallelism, no pre-sizing.
type forkJoinBuilder[V any] struct {
	ctx         context.Context
	pool        *RoutinePool
	parallelism int
	capacity    int
}

func ForkJoinBuilder[V any]() forkJoinBuilder[V] {
	return forkJoinBuilder[V]{}
}

func (b forkJoinBuilder[V]) Context(ctx context.Context) forkJoinBuilder[V] {
	c.Require(ctx != nil, "ctx must not be nil")
	b.ctx = ctx
	return b
}

func (b forkJoinBuilder[V]) Pool(pool *RoutinePool) forkJoinBuilder[V] {
	c.Require(pool != nil, "pool must not be nil")
	b.pool = pool
	return b
}

func (b forkJoinBuilder[V]) Parallelism(parallelism int) forkJoinBuilder[V] {
	c.Require(parallelism > 0, "parallelism must be > 0")
	b.parallelism = parallelism
	return b
}

func (b forkJoinBuilder[V]) Capacity(capacity int) forkJoinBuilder[V] {
	c.Require(capacity >= 0, "capacity must be >= 0")
	b.capacity = capacity
	return b
}

func (b forkJoinBuilder[V]) Build() *ForkJoin[V] {
	c.Require(b.pool == nil || b.parallelism == 0, "Pool and Parallelism are mutually exclusive")
	if b.pool == nil {
		if b.parallelism == 0 {
			b.parallelism = DefaultParallelism
		}
		b.pool = NewRoutinePoolWithParallelism(b.parallelism)
	}
	if b.ctx == nil {
		b.ctx = context.Background()
	}
	fj := &ForkJoin[V]{
		pool:     b.pool,
		results:  make([]V, 0, b.capacity),
		resultCh: make(chan V, b.pool.Parallelism()),
		doneCh:   make(chan Nothing),
	}
	fj.ctx, fj.cancel = context.WithCancelCause(b.ctx)
	go fj.collector()
	return fj
}

// For children that must stop when this instance does.
func (fj *ForkJoin[V]) Context() context.Context {
	return fj.ctx
}

func (fj *ForkJoin[V]) Stop(err error) {
	c.Require(err != nil, "Stop requires real error")
	fj.cancel(err) // first cause wins
}

func (fj *ForkJoin[V]) Stopped() bool {
	return fj.ctx.Err() != nil
}

func (fj *ForkJoin[V]) Err() error {
	return context.Cause(fj.ctx)
}

func (fj *ForkJoin[V]) Go(task func() V) {
	c.Check(fj.Err())

	fj.wg.Add(1)
	fj.pool.Go(func() {
		defer fj.wg.Done()

		// Never panics: nothing recovers in a pool goroutine, so a throw here kills the process.
		if fj.Stopped() {
			return
		}
		var res V
		err := c.Rescue(func() { res = task() })
		if err != nil {
			fj.Stop(err)
		} else {
			fj.resultCh <- res
		}
	})
}

func (fj *ForkJoin[V]) AddResult(v V) {
	c.Check(fj.Err())
	fj.resultCh <- v
}

func (fj *ForkJoin[V]) collector() {
	for v := range fj.resultCh {
		fj.results = append(fj.results, v)
	}
	close(fj.doneCh)
}

func (fj *ForkJoin[V]) shutdown() {
	fj.shutOnce.Do(func() {
		fj.wg.Wait()
		close(fj.resultCh)
	})
}

func (fj *ForkJoin[V]) Cleanup() {
	r := recover()
	if r == nil {
		fj.cancel(errAbandoned) // no-op after Results; otherwise releases the ctx node too
		fj.shutdown()
		return
	}
	err := c.PanicToError(r)
	fj.Stop(err)
	fj.shutdown()
	panic(err)
}

func (fj *ForkJoin[V]) Results() []V {
	fj.shutdown()
	<-fj.doneCh
	c.Check(fj.Err())
	fj.cancel(errCollected) // later Go/AddResult throw; also releases the ctx node
	return fj.results
}
