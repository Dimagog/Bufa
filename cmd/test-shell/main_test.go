package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_ScriptThenSession(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("FAKE_PROMPT", "> ")
	t.Setenv("FAKE_VAR", "v1")
	script := filepath.Join(dir, "s.fake")
	scriptBody := []byte("# comment\necho hi ${FAKE_VAR}\nwrite out.txt a\nappend out.txt b\n")
	if err := os.WriteFile(script, scriptBody, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"-i", script}, strings.NewReader("env FAKE_VAR\nenv NOPE\nexit 3\n"), &out, &errOut)
	if code != 3 {
		t.Errorf("exit code = %d, want 3; stderr: %s", code, errOut.String())
	}
	if got, want := out.String(), "hi v1\n> FAKE_VAR=v1\n> NOPE unset\n> "; got != want {
		t.Errorf("out = %q, want %q", got, want)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "out.txt")); string(data) != "a\nb\n" {
		t.Errorf("out.txt = %q, want a, b", data)
	}
}

func TestRun_Errors(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{filepath.Join(t.TempDir(), "missing")}, nil, &out, &errOut); code != 2 {
		t.Errorf("missing script: code = %d, want 2", code)
	}
	if code := run([]string{"a", "b", "c"}, nil, &out, &errOut); code != 2 {
		t.Errorf("bad usage: code = %d, want 2", code)
	}
	errOut.Reset()
	code := run([]string{"-i"}, strings.NewReader("bogus\n"), &out, &errOut)
	if code != 2 || !strings.Contains(errOut.String(), "unknown command") {
		t.Errorf("unknown command: code = %d, stderr %q", code, errOut.String())
	}
	if code := run([]string{"-i"}, strings.NewReader("echo eof\n"), &out, &errOut); code != 0 {
		t.Errorf("EOF ends a session cleanly: code = %d", code)
	}
}
