package contract

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"time"
)

func logErrorSkipStack(skip int, msg string, args ...any) {
	// Handle is called directly (to control the stack skip), so the level gate must be checked
	// explicitly — otherwise these logs fire even at the default "none" level.
	h := slog.Default().Handler()
	if !h.Enabled(context.Background(), slog.LevelError) {
		return
	}
	var pcs [1]uintptr
	runtime.Callers(skip, pcs[:])
	r := slog.NewRecord(time.Now(), slog.LevelError, msg, pcs[0])
	r.Add(args...)
	_ = h.Handle(context.Background(), r)
}

func rawAssert(reason string, condition bool, format string, args ...any) {
	if !condition {
		msg := fmt.Sprintf("%s: %s", reason, fmt.Sprintf(format, args...))
		logErrorSkipStack(4, msg)
		panic(msg)
	}
}

func Fail(format string, args ...any) {
	rawAssert("FAILED", false, format, args...)
}

func Assert(condition bool, format string, args ...any) {
	rawAssert("Assertion FAILED", condition, format, args...)
}

func Require(condition bool, format string, args ...any) {
	rawAssert("Pre-Condition FAILED", condition, format, args...)
}

func Check(err error) {
	if err != nil {
		logErrorSkipStack(3, "Check failed", "err", err)
		panic(err)
	}
}

func Checkf(err error, format string, args ...any) {
	if err != nil {
		msg := fmt.Sprintf(format, args...)
		wrapped := fmt.Errorf("%s\n%w", msg, err)
		logErrorSkipStack(3, "Check failed", "reason", msg, "err", err)
		panic(wrapped)
	}
}

// Panics with err as-is, so Catch/Rescue surface the typed value unwrapped.
func Error(err error) {
	Require(err != nil, "Error call expects non-nil err, use Check for nil-ok calls")
	logErrorSkipStack(3, "Error", "err", err)
	panic(err)
}

func Errorf(err error, format string, args ...any) {
	Require(err != nil, "Errorf call expects non-nil err, use Checkf for nil-ok calls")
	msg := fmt.Sprintf(format, args...)
	wrapped := fmt.Errorf("%s\n%w", msg, err)
	logErrorSkipStack(3, "Errorf", "reason", msg, "err", err)
	panic(wrapped)
}

func Check2[T any](v T, err error) T {
	if err != nil {
		logErrorSkipStack(3, "Check2 failed", "err", err)
		panic(err)
	}
	return v
}

type checkCtx struct {
	format string
	args   []any
}

// Two calls because Go forbids extra args alongside a multi-value call — the spread stays
// Check2's sole argument:
//
//	data := c.With("read '%s'", p).Check2(vfs.ReadFile(fs, p))
func With(format string, args ...any) checkCtx {
	return checkCtx{format, args}
}

func (c checkCtx) Check2[T any](v T, err error) T {
	if err != nil {
		msg := fmt.Sprintf(c.format, c.args...)
		wrapped := fmt.Errorf("%s\n%w", msg, err)
		logErrorSkipStack(3, "Check2 failed", "reason", msg, "err", err)
		panic(wrapped)
	}
	return v
}

// Deferred, so a deep panic accumulates a breadcrumb trail as it climbs:
//
//	defer c.Context("build %s", srcDir)
//
// A non-error/non-string panic value passes through unwrapped.
func Context(format string, args ...any) {
	r := recover()
	if r == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	panic(fmt.Errorf("... %s\n%w", msg, PanicToError(r)))
}

func PanicToError(r any) error {
	switch v := r.(type) {
	case nil:
		return nil
	case error:
		return v
	case string:
		return errors.New(v)
	default:
		panic(r)
	}
}

func Catch(err *error) {
	panicErr := PanicToError(recover())
	if *err != nil && panicErr != nil {
		logErrorSkipStack(6, "Catch: CODE BUG! joining prior error with recovered panic", "prior", *err, "panic", panicErr)
		*err = errors.Join(*err, panicErr)
		return
	}
	Require(*err == nil, "Catch: *err must be nil on entry, was %v", *err)
	if panicErr != nil {
		logErrorSkipStack(6, "Catch: recovered panic as error", "err", panicErr)
	}
	*err = panicErr
}

func Rescue(fn func()) (err error) {
	defer Catch(&err)
	fn()
	return nil
}
