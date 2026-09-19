package main

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Runtime"
)

func parseCLI(t *testing.T, args ...string) *CLI {
	t.Helper()
	var cli CLI
	if _, err := newParser(&cli, nil, io.Discard, io.Discard).Parse(args); err != nil {
		t.Fatalf("parse %q: %v", strings.Join(args, " "), err)
	}
	return &cli
}

func TestBuildFlags_ForceRebuild(t *testing.T) {
	cases := []struct {
		args []string
		want Runtime.DirScope
	}{
		{[]string{"build", "x"}, Runtime.ScopeNone},
		{[]string{"x", "--force"}, Runtime.ScopeTarget}, // default command
		{[]string{"-f", "x"}, Runtime.ScopeTarget},
		{[]string{"build", "--force-all"}, Runtime.ScopeAll},
		{[]string{"-F"}, Runtime.ScopeAll},
		{[]string{"-f", "-F"}, Runtime.ScopeAll}, // -all wins
	}
	for _, tt := range cases {
		if got := parseCLI(t, tt.args...).Build.forceRebuild(); got != tt.want {
			t.Errorf("bufa %s: forceRebuild = %v, want %v", strings.Join(tt.args, " "), got, tt.want)
		}
	}
	if got := parseCLI(t, "dirty", "-fF").Dirty.forceRebuild(); got != Runtime.ScopeAll {
		t.Errorf("bufa dirty -fF: forceRebuild = %v, want ScopeAll", got)
	}
}

func TestGlobalFlags_StartDir(t *testing.T) {
	if got := parseCLI(t, "build", "x"); got.StartDir != "" || !slices.Equal(got.Build.Dirs, []string{"x"}) {
		t.Errorf("bufa build x: StartDir=%q Dirs=%q, want \"\" (cwd) and [x]", got.StartDir, got.Build.Dirs)
	}
	// Global: accepted before or after the command, on every command.
	for _, args := range [][]string{
		{"--start-dir=s", "build", "x"}, {"--dir", "s", "x"}, {"x", "--dir=s"}, {"build", "x", "--start-dir=s"},
	} {
		if got := parseCLI(t, args...); got.StartDir != "s" || !slices.Equal(got.Build.Dirs, []string{"x"}) {
			t.Errorf("bufa %s: StartDir=%q Dirs=%q, want \"s\" and [x]", strings.Join(args, " "), got.StartDir, got.Build.Dirs)
		}
	}
	for _, args := range [][]string{{"dirty", "--dir=s"}, {"gc", "--start-dir=s"}, {"--dir=s", "hash", "p"}} {
		if got := parseCLI(t, args...).StartDir; got != "s" {
			t.Errorf("bufa %s: StartDir=%q, want \"s\"", strings.Join(args, " "), got)
		}
	}
}

func TestBuildArgs_MultipleDirs(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"build"}, nil},
		{[]string{"build", "a", "b"}, []string{"a", "b"}},
		{[]string{"a", "b"}, []string{"a", "b"}}, // default command
		// Flags before or after the list only: kong fills a slice positional from consecutive
		// tokens, so `a -f b` is a parse error.
		{[]string{"-f", "a", "b"}, []string{"a", "b"}},
		{[]string{"a", "b", "-f"}, []string{"a", "b"}},
		{[]string{"b", "a", "b"}, []string{"a", "b"}}, // alias
		{[]string{"a", "gc"}, []string{"a", "gc"}},    // command names win as the first token only
	}
	for _, tt := range cases {
		if got := parseCLI(t, tt.args...).Build.Dirs; !slices.Equal(got, tt.want) {
			t.Errorf("bufa %s: Dirs = %q, want %q", strings.Join(tt.args, " "), got, tt.want)
		}
	}
	if got := parseCLI(t, "dirty", "a", "b").Dirty.Dirs; !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("bufa dirty a b: Dirs = %q, want [a b]", got)
	}
}

func TestNukeFlags_ScopeIsExclusive(t *testing.T) {
	if !parseCLI(t, "nuke", "--cache-only", "--yes").Nuke.CacheOnly {
		t.Error("--cache-only must parse")
	}
	for _, args := range [][]string{
		{"nuke", "--cache-only", "--global"},
		{"nuke", "--cache-only", "--global-only"},
		{"nuke", "--global", "--global-only"},
	} {
		var cli CLI
		if _, err := newParser(&cli, nil, io.Discard, io.Discard).Parse(args); err == nil {
			t.Errorf("bufa %s must be a parse error: the scope flags are mutually exclusive", strings.Join(args, " "))
		}
	}
}

func TestCheckFlags_ScopeIsExclusive(t *testing.T) {
	if got := parseCLI(t, "check").Check; got.NoGlobal || got.GlobalOnly {
		t.Error("check must verify both the store and the global cache by default")
	}
	if !parseCLI(t, "check", "--no-global", "-f").Check.NoGlobal || !parseCLI(t, "check", "--global-only").Check.GlobalOnly {
		t.Error("--no-global and --global-only must parse")
	}
	var cli CLI
	if _, err := newParser(&cli, nil, io.Discard, io.Discard).Parse([]string{"check", "--no-global", "--global-only"}); err == nil {
		t.Error("bufa check --no-global --global-only must be a parse error: the scope flags are mutually exclusive")
	}
}

func TestGcFlags_NoSize(t *testing.T) {
	if parseCLI(t, "gc").Gc.NoSize {
		t.Error("gc must measure freed bytes by default")
	}
	if !parseCLI(t, "gc", "--no-size").Gc.NoSize {
		t.Error("--no-size must parse")
	}
}

func TestBuildFlags_Shell(t *testing.T) {
	cases := []struct {
		args []string
		want Runtime.BuildMode
	}{
		{[]string{"build", "x"}, Runtime.ModeBuild},
		{[]string{"x", "--shell"}, Runtime.ModeShell}, // default command
		{[]string{"-s", "x"}, Runtime.ModeShell},
		{[]string{"build", "--post-shell"}, Runtime.ModePostShell},
		{[]string{"-S"}, Runtime.ModePostShell},
	}
	for _, tt := range cases {
		if got := parseCLI(t, tt.args...).Build.buildMode(); got != tt.want {
			t.Errorf("bufa %s: buildMode = %v, want %v", strings.Join(tt.args, " "), got, tt.want)
		}
	}
	if got := parseCLI(t, "dirty", "--post-shell").Dirty.buildMode(); got != Runtime.ModePostShell {
		t.Errorf("bufa dirty --post-shell: buildMode = %v, want ModePostShell", got)
	}
	var cli CLI
	if _, err := newParser(&cli, nil, io.Discard, io.Discard).Parse([]string{"build", "--shell", "--post-shell"}); err == nil {
		t.Error("bufa build --shell --post-shell must be a parse error: the shell flags are mutually exclusive")
	}
}
