package logging

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, time.May, 16, 0, 54, 35, 998_000_000, time.UTC)

func render(t *testing.T, level slog.Level, msg string, pc uintptr, attrs ...slog.Attr) string {
	t.Helper()
	var buf bytes.Buffer
	h := newHandler(&buf, slog.LevelDebug)
	r := slog.NewRecord(fixedTime, level, msg, pc)
	r.AddAttrs(attrs...)
	if err := h.Handle(t.Context(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	return buf.String()
}

func newHandler(buf *bytes.Buffer, lvl slog.Level) *prettyHandler {
	return &prettyHandler{out: buf, mu: new(sync.Mutex), level: lvl, addSource: true}
}

func assertEq(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s\n got: %q\nwant: %q", what, got, want)
	}
}

// Order is owned by the constant alone, so only set equality is checkable.
func TestLevelNamesInSync(t *testing.T) {
	seen := make(map[string]bool, len(levels))
	for _, name := range strings.Split(LevelNames, ", ") {
		if seen[name] {
			t.Errorf("duplicate %q in LevelNames", name)
		}
		seen[name] = true
		if _, ok := levels[name]; !ok {
			t.Errorf("LevelNames has %q, missing from levels map", name)
		}
	}
	for name := range levels {
		if !seen[name] {
			t.Errorf("levels map has %q, missing from LevelNames", name)
		}
	}
}

func TestBasicLine(t *testing.T) {
	got := render(t, slog.LevelInfo, "Combined bld deps hash:", 0,
		slog.String("dir", "ANTLR"), slog.String("hash", "v4"))
	assertEq(t, "basic line", got,
		"2026-05-16 00:54:35.998 INFO  Combined bld deps hash: dir=ANTLR hash=v4\n")
}

func TestLevelPadding(t *testing.T) {
	cases := []struct {
		lvl  slog.Level
		want string
	}{
		{slog.LevelDebug, "DEBUG"},
		{slog.LevelInfo, "INFO "},
		{slog.LevelWarn, "WARN "},
		{slog.LevelError, "ERROR"},
	}
	for _, c := range cases {
		if got := levelLabel(c.lvl); got != c.want {
			t.Errorf("levelLabel(%v) = %q, want %q", c.lvl, got, c.want)
		}
		if len(c.want) != 5 {
			t.Errorf("label %q not width 5", c.want)
		}
	}
	// padded label + separator ⇒ a single visible gap
	got := render(t, slog.LevelError, "boom", 0)
	assertEq(t, "error line", got, "2026-05-16 00:54:35.998 ERROR boom\n")
}

func TestMsgAlwaysRaw(t *testing.T) {
	got := render(t, slog.LevelInfo, "a b: c=d", 0)
	assertEq(t, "raw msg", got, "2026-05-16 00:54:35.998 INFO  a b: c=d\n")
}

func TestAttrQuoting(t *testing.T) {
	got := render(t, slog.LevelInfo, "msg", 0,
		slog.String("err", "open x: no such file"),
		slog.String("hash", "v4"))
	want := "2026-05-16 00:54:35.998 INFO  msg" +
		` err="open x: no such file" hash=v4` + "\n"
	assertEq(t, "attr quoting", got, want)
}

func TestSourceSuffix(t *testing.T) {
	var pcs [1]uintptr
	n := runtime.Callers(1, pcs[:]) // PC of this line
	if n != 1 {
		t.Fatalf("runtime.Callers n=%d", n)
	}
	fs := runtime.CallersFrames(pcs[:])
	f, _ := fs.Next()
	wantSuffix := " (" + filepath.ToSlash(f.File) + ":" +
		strconv.Itoa(f.Line) + ")\n"

	got := render(t, slog.LevelInfo, "msg", pcs[0])
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("source suffix\n got: %q\nwant suffix: %q", got, wantSuffix)
	}
	if strings.Contains(got, "\\") {
		t.Errorf("source path has backslash: %q", got)
	}
}

func TestEnabled(t *testing.T) {
	none := newHandler(&bytes.Buffer{}, levelNone)
	if none.Enabled(t.Context(), slog.LevelInfo) {
		t.Error("levelNone should suppress Info")
	}
	if none.Enabled(t.Context(), slog.LevelError) {
		t.Error("levelNone should suppress Error")
	}
	info := newHandler(&bytes.Buffer{}, slog.LevelInfo)
	if info.Enabled(t.Context(), slog.LevelDebug) {
		t.Error("info level should suppress Debug")
	}
	if !info.Enabled(t.Context(), slog.LevelInfo) {
		t.Error("info level should allow Info")
	}
}

func TestWithAttrsAndGroup(t *testing.T) {
	var buf bytes.Buffer
	base := newHandler(&buf, slog.LevelDebug)

	h := base.WithAttrs([]slog.Attr{slog.String("pre", "1")})
	r := slog.NewRecord(fixedTime, slog.LevelInfo, "m", 0)
	r.AddAttrs(slog.String("post", "2"))
	if err := h.Handle(t.Context(), r); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertEq(t, "WithAttrs", buf.String(),
		"2026-05-16 00:54:35.998 INFO  m pre=1 post=2\n")

	buf.Reset()
	g := base.WithGroup("grp")
	r2 := slog.NewRecord(fixedTime, slog.LevelInfo, "m", 0)
	r2.AddAttrs(slog.String("k", "v"))
	if err := g.Handle(t.Context(), r2); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	assertEq(t, "WithGroup", buf.String(),
		"2026-05-16 00:54:35.998 INFO  m grp.k=v\n")

	if base.WithGroup("") != slog.Handler(base) {
		t.Error("WithGroup(\"\") should return the same handler")
	}
}

func TestSourceEnabled(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", false}, // also covers unset (Getenv -> "")
		{"1", true},
		{"t", true},
		{"true", true},
		{"TRUE", true},
		{" true ", true}, // trimmed
		{"0", false},
		{"false", false},
		{"yes", false}, // not a ParseBool truthy
		{"garbage", false},
	}
	for _, c := range cases {
		t.Setenv("LOG_SOURCE", c.env)
		if got := sourceEnabled(); got != c.want {
			t.Errorf("sourceEnabled() with LOG_SOURCE=%q = %v, want %v", c.env, got, c.want)
		}
	}
}
