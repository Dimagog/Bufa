package contract

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func mustPanic(t *testing.T, fn func()) any {
	t.Helper()
	var r any
	func() {
		defer func() { r = recover() }()
		fn()
	}()
	return r
}

func TestFail_PanicsWithFormattedMessage(t *testing.T) {
	r := mustPanic(t, func() { Fail("oops %d", 7) })
	s, ok := r.(string)
	if !ok {
		t.Fatalf("panic value type = %T, want string", r)
	}
	if !strings.Contains(s, "FAILED") || !strings.Contains(s, "oops 7") {
		t.Errorf("panic message = %q, want it to contain FAILED and oops 7", s)
	}
}

func TestAssert(t *testing.T) {
	if r := mustPanic(t, func() { Assert(true, "should not fire") }); r != nil {
		t.Errorf("Assert(true) panicked: %v", r)
	}
	r := mustPanic(t, func() { Assert(false, "value=%d", 42) })
	s, _ := r.(string)
	if !strings.Contains(s, "Assertion FAILED") || !strings.Contains(s, "value=42") {
		t.Errorf("panic message = %q, want Assertion FAILED and value=42", s)
	}
}

func TestRequire(t *testing.T) {
	if r := mustPanic(t, func() { Require(true, "should not fire") }); r != nil {
		t.Errorf("Require(true) panicked: %v", r)
	}
	r := mustPanic(t, func() { Require(false, "x=%s", "y") })
	s, _ := r.(string)
	if !strings.Contains(s, "Pre-Condition FAILED") || !strings.Contains(s, "x=y") {
		t.Errorf("panic message = %q, want Pre-Condition FAILED and x=y", s)
	}
}

func TestCheck_NilNoPanic(t *testing.T) {
	if r := mustPanic(t, func() { Check(nil) }); r != nil {
		t.Errorf("Check(nil) panicked: %v", r)
	}
}

func TestCheck_BarePanicsWithErrAsIs(t *testing.T) {
	sentinel := errors.New("boom")
	r := mustPanic(t, func() { Check(sentinel) })
	if r != sentinel {
		t.Errorf("Check panicked with %v (%T), want sentinel as-is", r, r)
	}
}

func TestCheckf_WrapsAndPreservesChain(t *testing.T) {
	sentinel := errors.New("disk full")
	r := mustPanic(t, func() { Checkf(sentinel, "write %s", "file.txt") })
	err, ok := r.(error)
	if !ok {
		t.Fatalf("panic value type = %T, want error", r)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(panic, sentinel) = false, want true")
	}
	msg := err.Error()
	if !strings.Contains(msg, "write file.txt") || !strings.Contains(msg, "disk full") {
		t.Errorf("err = %q, want it to contain context and original message", msg)
	}
}

func TestWithCheck2_NilReturnsValue(t *testing.T) {
	var got int
	if r := mustPanic(t, func() { got = With("ctx %s", "x").Check2(7, error(nil)) }); r != nil {
		t.Errorf("With(...).Check2(nil err) panicked: %v", r)
	}
	if got != 7 {
		t.Errorf("Check2 returned %d, want 7", got)
	}
}

func TestWithCheck2_WrapsAndPreservesChain(t *testing.T) {
	sentinel := errors.New("no such file")
	r := mustPanic(t, func() { _ = With("read '%s'", "/a/BUFA.toml").Check2(0, sentinel) })
	err, ok := r.(error)
	if !ok {
		t.Fatalf("panic value type = %T, want error", r)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(panic, sentinel) = false, want chain preserved")
	}
	msg := err.Error()
	if !strings.Contains(msg, "read '/a/BUFA.toml'") || !strings.Contains(msg, "no such file") {
		t.Errorf("err = %q, want context + original message", msg)
	}
}

func TestContext_NoPanicIsNoOp(t *testing.T) {
	if r := mustPanic(t, func() { Context("ctx %d", 1) }); r != nil {
		t.Errorf("Context with no panic in flight panicked: %v", r)
	}
}

func TestContext_WrapsAndPreservesChain(t *testing.T) {
	sentinel := errors.New("no such file")
	frame := func() {
		defer Context("build %q", "/a/b")
		Checkf(sentinel, "read %s", "BUFA.toml")
	}
	r := mustPanic(t, frame)
	err, ok := r.(error)
	if !ok {
		t.Fatalf("panic value type = %T, want error", r)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(panic, sentinel) = false, want chain preserved")
	}
	msg := err.Error()
	if !strings.Contains(msg, `build "/a/b"`) || !strings.Contains(msg, "read BUFA.toml") || !strings.Contains(msg, "no such file") {
		t.Errorf("err = %q, want context + leaf message", msg)
	}
}

func TestContext_AccumulatesAcrossFrames(t *testing.T) {
	sentinel := errors.New("boom")
	inner := func() {
		defer Context("build %q", "/a/b")
		Check(sentinel)
	}
	outer := func() {
		defer Context("build %q", "/a")
		inner()
	}
	r := mustPanic(t, outer)
	err, _ := r.(error)
	msg := err.Error()
	// Outermost context appears first, each on its own line prefixed with "... ".
	if !strings.Contains(msg, "... build \"/a\"\n... build \"/a/b\"\n") {
		t.Errorf("err = %q, want both frames' context, newline-joined", msg)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(panic, sentinel) = false, want chain preserved")
	}
}

func TestContext_PreservesTypedErrorViaAs(t *testing.T) {
	sentinel := &os.PathError{Op: "open", Path: "x", Err: errors.New("nope")}
	frame := func() {
		defer Context("build %q", "/a")
		Error(sentinel)
	}
	r := mustPanic(t, frame)
	err, _ := r.(error)
	var pe *os.PathError
	if !errors.As(err, &pe) {
		t.Errorf("errors.As(*PathError) = false, want typed error reachable through wrap")
	}
}

func TestContext_NonErrorPanicPassesThrough(t *testing.T) {
	frame := func() {
		defer Context("build %q", "/a")
		panic(42)
	}
	r := mustPanic(t, frame)
	if r != 42 {
		t.Errorf("re-panic value = %v, want 42 passed through unwrapped", r)
	}
}

func TestPanicToError(t *testing.T) {
	if err := PanicToError(nil); err != nil {
		t.Errorf("PanicToError(nil) = %v, want nil", err)
	}
	sentinel := errors.New("boom")
	if err := PanicToError(sentinel); err != sentinel {
		t.Errorf("PanicToError(error) did not return as-is")
	}
	if err := PanicToError("string panic"); err == nil || err.Error() != "string panic" {
		t.Errorf("PanicToError(string) = %v, want error wrapping the string", err)
	}
	r := mustPanic(t, func() { _ = PanicToError(42) })
	if r != 42 {
		t.Errorf("PanicToError(int) re-panic value = %v, want 42", r)
	}
}

func TestCatch_NoPanicLeavesErrNil(t *testing.T) {
	noPanic := func() (err error) {
		defer Catch(&err)
		return
	}
	if err := noPanic(); err != nil {
		t.Errorf("no-panic path err = %v, want nil", err)
	}
}

func TestCatch_RecoversCheckPanic(t *testing.T) {
	sentinel := errors.New("io")
	run := func() (err error) {
		defer Catch(&err)
		Checkf(sentinel, "doing %s", "thing")
		return
	}
	err := run()
	if err == nil {
		t.Fatal("want recovered err, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is = false, want chain preserved")
	}
}

func TestCatch_RecoversStringPanic(t *testing.T) {
	run := func() (err error) {
		defer Catch(&err)
		Fail("bad %s", "state")
		return
	}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "FAILED") || !strings.Contains(err.Error(), "bad state") {
		t.Errorf("err = %v, want it to wrap the Fail string", err)
	}
}

func TestCatch_MergesPreExistingErrAndPanic(t *testing.T) {
	preExisting := errors.New("pre-existing")
	panicErr := errors.New("from panic")
	run := func() (err error) {
		defer Catch(&err)
		err = preExisting
		Checkf(panicErr, "wrapped")
		return
	}
	err := run()
	if err == nil {
		t.Fatal("want merged err, got nil")
	}
	if !errors.Is(err, preExisting) {
		t.Errorf("errors.Is(merged, preExisting) = false")
	}
	if !errors.Is(err, panicErr) {
		t.Errorf("errors.Is(merged, panicErr) = false")
	}
}

func TestCatch_PreExistingErrNoPanicFailsRequire(t *testing.T) {
	run := func() (err error) {
		defer Catch(&err)
		err = errors.New("pre-existing")
		return
	}
	r := mustPanic(t, func() { _ = run() })
	s, _ := r.(string)
	if !strings.Contains(s, "Pre-Condition FAILED") || !strings.Contains(s, "Catch") {
		t.Errorf("panic message = %q, want Pre-Condition FAILED about Catch", s)
	}
}

func TestRescue_NoPanicReturnsNil(t *testing.T) {
	if err := Rescue(func() {}); err != nil {
		t.Errorf("Rescue(no-panic) = %v, want nil", err)
	}
}

func TestRescue_ReturnsRecoveredErr(t *testing.T) {
	sentinel := errors.New("boom")
	err := Rescue(func() { Checkf(sentinel, "ctx") })
	if err == nil || !errors.Is(err, sentinel) {
		t.Errorf("Rescue returned err = %v, want chain with sentinel", err)
	}
}

func TestRescue_NonContractPanicRePanics(t *testing.T) {
	r := mustPanic(t, func() { _ = Rescue(func() { panic(42) }) })
	if r != 42 {
		t.Errorf("re-panic value = %v, want 42", r)
	}
}
