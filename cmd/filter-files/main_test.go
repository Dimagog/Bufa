package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

func mustWriteFile(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	mustMkdirAll(t, filepath.Dir(full))
	if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", full, err)
	}
}

func buildTree(t *testing.T, root string) {
	t.Helper()
	for _, rel := range []string{
		"a.md",
		".hidden",
		"__pycache__/x.pyc",
		"dir/b.md",
		"dir/.secret",
	} {
		mustWriteFile(t, root, rel)
	}
}

func TestRun_WalkMode(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root)

	var out, errOut bytes.Buffer
	err := run(
		[]string{"--rule", "+**", "--rule", "-**/.**", "--rule", "-**/_**", root},
		strings.NewReader(""),
		&out,
		&errOut,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, errOut.String())
	}

	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{"a.md", "dir/b.md"}
	if !slices.Equal(got, want) {
		t.Errorf("output =\n%v\nwant\n%v", got, want)
	}
}

func TestRun_WalkMode_ShortRuleFlag(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root)

	var out, errOut bytes.Buffer
	err := run(
		[]string{"-r", "+**", "-r", "-**/.**", "-r", "-**/_**", root},
		strings.NewReader(""),
		&out,
		&errOut,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, errOut.String())
	}

	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{"a.md", "dir/b.md"}
	if !slices.Equal(got, want) {
		t.Errorf("output =\n%v\nwant\n%v", got, want)
	}
}

func TestRun_DefaultRulesViaCli(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root)

	var out, errOut bytes.Buffer
	err := run(
		[]string{"--rule", "-**/.**", "--rule", "-**/_**", root},
		strings.NewReader(""),
		&out,
		&errOut,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, errOut.String())
	}
	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{"a.md", "dir/b.md"}
	if !slices.Equal(got, want) {
		t.Errorf("output =\n%v\nwant\n%v", got, want)
	}
}

func TestRun_StdinMode(t *testing.T) {
	in := strings.NewReader("a.md\n.hidden\ndir/b.md\n__pycache__/x.pyc\n\n")
	var out, errOut bytes.Buffer
	err := run(
		[]string{"--rule", "+**", "--rule", "-**/.**", "--rule", "-**/_**", "--stdin"},
		in,
		&out,
		&errOut,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, errOut.String())
	}
	// Stdin mode preserves input order (no sort).
	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{"a.md", "dir/b.md"}
	if !slices.Equal(got, want) {
		t.Errorf("output =\n%v\nwant\n%v", got, want)
	}
}

func TestRun_UsageError_NoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run([]string{}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Errorf("want error, got nil; stderr=%q", errOut.String())
	}
}

func TestRun_UsageError_StdinWithRoot(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run([]string{"--stdin", t.TempDir()}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Errorf("want error, got nil; stderr=%q", errOut.String())
	}
}

func TestRun_ParseError_BadRule(t *testing.T) {
	var out, errOut bytes.Buffer
	err := run(
		[]string{"--rule", "garbage", t.TempDir()},
		strings.NewReader(""),
		&out,
		&errOut,
	)
	if err == nil {
		t.Errorf("want error, got nil; stderr=%q", errOut.String())
	}
	if err != nil && !strings.Contains(err.Error(), "garbage") {
		t.Errorf("err = %q, want it to mention the bad rule", err.Error())
	}
}

func TestRun_RulesFile(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root)

	rulesPath := filepath.Join(t.TempDir(), "rules.txt")
	body := "" +
		"; leading comment\n" +
		"\n" +
		"+**\n" +
		"# hash comment\n" +
		"-**/.**\n" +
		"   \n" +
		"-**/_**\n"
	if err := os.WriteFile(rulesPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var out, errOut bytes.Buffer
	err := run([]string{"--rules", rulesPath, root}, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, errOut.String())
	}
	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{"a.md", "dir/b.md"}
	if !slices.Equal(got, want) {
		t.Errorf("output =\n%v\nwant\n%v", got, want)
	}
}

func TestRun_RulesFile_Missing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-rules.txt")
	var out, errOut bytes.Buffer
	err := run([]string{"--rules", missing, t.TempDir()}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Errorf("want error, got nil; stderr=%q", errOut.String())
	}
}

func TestRun_RulesAndRule_Conflict(t *testing.T) {
	rulesPath := filepath.Join(t.TempDir(), "rules.txt")
	if err := os.WriteFile(rulesPath, []byte("+**\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var out, errOut bytes.Buffer
	err := run(
		[]string{"--rules", rulesPath, "--rule", "+**", t.TempDir()},
		strings.NewReader(""),
		&out,
		&errOut,
	)
	if err == nil {
		t.Errorf("want error, got nil; stderr=%q", errOut.String())
	}
	if err != nil && (!strings.Contains(err.Error(), "--rules") || !strings.Contains(err.Error(), "--rule")) {
		t.Errorf("err = %q, want it to mention both flags", err.Error())
	}
}

func TestRun_NoRules_UsesDefaults(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root)

	var out, errOut bytes.Buffer
	err := run([]string{root}, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatalf("unexpected error: %v; stderr=%q", err, errOut.String())
	}
	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	want := []string{"a.md", "dir/b.md"}
	if !slices.Equal(got, want) {
		t.Errorf("output =\n%v\nwant\n%v (DefaultRules should exclude dot/underscore segments)", got, want)
	}
}

func TestRun_WalkMode_MissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	var out, errOut bytes.Buffer
	err := run(
		[]string{"--rule", "+**", missing},
		strings.NewReader(""),
		&out,
		&errOut,
	)
	if err == nil {
		t.Errorf("want error, got nil; stderr=%q", errOut.String())
	}
}
