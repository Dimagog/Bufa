package Util

import (
	"runtime"

	c "github.com/dimagog/bufa/internal/contract"
)

var DefaultParallelism = runtime.NumCPU()

type RoutinePool struct {
	parallelism int
	// Nothing takes no space, so the channel's memory is constant regardless of parallelism
	slots chan Nothing
}

func NewRoutinePool() *RoutinePool {
	return &RoutinePool{
		parallelism: DefaultParallelism,
		slots:       make(chan Nothing, DefaultParallelism),
	}
}

func NewRoutinePoolWithParallelism(parallelism int) *RoutinePool {
	c.Require(parallelism >= 1, "RoutinePool: parallelism must be >= 1")
	return &RoutinePool{
		parallelism: parallelism,
		slots:       make(chan Nothing, parallelism),
	}
}

func (p *RoutinePool) Parallelism() int {
	return p.parallelism
}

func (p *RoutinePool) Go(task func()) {
	p.slots <- Nothing{}

	go func() {
		defer func() { <-p.slots }()
		task()
	}()
}
