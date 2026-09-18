package Runtime

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/UnsafeIO"
	c "github.com/dimagog/bufa/internal/contract"
)

// Creates dir (and parents) holding a root marker; returns dir.
func markedDir(t *testing.T, dir string) string {
	t.Helper()
	c.Check(os.MkdirAll(dir, 0o755))
	c.Check(os.WriteFile(filepath.Join(dir, Store.SrcRootFileName), nil, 0o644))
	return dir
}

func realPath(p string) string {
	return c.Check2(filepath.EvalSymlinks(p))
}

func TestPrepare_FindsMarker(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", "") // isolate from user env
	parent := t.TempDir()
	root := markedDir(t, filepath.Join(parent, "proj"))
	sub := filepath.Join(root, "a", "b")
	c.Check(os.MkdirAll(sub, 0o755))
	t.Chdir(sub)

	rc := PrepareConfig(true, false, false, io.Discard)

	if got, want := realPath(rc.SrcRoot), realPath(root); got != want {
		t.Errorf("SrcRoot=%q, want %q", got, want)
	}
	wantBld := filepath.Join(parent, "proj.BUFA")
	if rc.BldRoot != wantBld {
		t.Errorf("bldRoot=%q, want sibling %q", rc.BldRoot, wantBld)
	}
	srcRealPath, ok := rc.SrcFS.(UnsafeIO.GetRealPath)
	if !ok {
		t.Fatal("SrcFS must expose RealPath for OS-backed copies")
	}
	if got, want := c.Check2(srcRealPath.RealPath(Store.SrcRootFileName)), filepath.Join(root, Store.SrcRootFileName); filepath.Clean(got) != filepath.Clean(want) {
		t.Errorf("SrcFS RealPath=%q, want %q", got, want)
	}
	// bldRoot is no longer created eagerly — nothing should exist on the FS yet.
	if _, err := os.Stat(wantBld); err == nil {
		t.Error("the sibling build root must not be created eagerly")
	}
	if rc.BuildStartTimeUTC.Location() != time.UTC {
		t.Errorf("BuildStartTime location=%s, want UTC", rc.BuildStartTimeUTC.Location())
	}
}

func TestPrepare_EnvOverride(t *testing.T) {
	root := t.TempDir()
	c.Check(os.WriteFile(filepath.Join(root, Store.SrcRootFileName), nil, 0o644))
	altBld := t.TempDir()
	t.Setenv("BUFA_BUILD_ROOT", altBld)
	t.Chdir(root)

	rc := PrepareConfig(true, false, false, io.Discard)

	wantBld := filepath.Join(altBld, filepath.Base(root)+".BUFA")
	if rc.BldRoot != wantBld {
		t.Errorf("BUFA_BUILD_ROOT not honored: bldRoot=%q, want %q", rc.BldRoot, wantBld)
	}
	if _, err := os.Stat(root + Store.BuildRootDirSuffix); err == nil {
		t.Error("the sibling build root must not be created when env overrides")
	}
}

func TestReadRootConfig_ParsesTTL(t *testing.T) {
	srcFS := vfs.NewMemMapFs()
	c.Check(vfs.WriteFile(srcFS, Store.SrcRootFileName, []byte("[unsafe]\nttl = \"20m\"\n"), 0o644))

	if got := readRootConfig(srcFS).Unsafe.TTL; got != 20*time.Minute {
		t.Errorf("RootConfig.Unsafe.TTL=%s, want 20m", got)
	}
}

func TestReadRootConfig_ParsesEnv(t *testing.T) {
	srcFS := vfs.NewMemMapFs()
	c.Check(vfs.WriteFile(srcFS, Store.SrcRootFileName, []byte("[env]\nJAVA_VER = ' 26 '\n"), 0o644))

	if got, _ := readRootConfig(srcFS).Env.Get("JAVA_VER"); got != "26" {
		t.Errorf("RootConfig.Env[JAVA_VER]=%q, want trimmed literal 26", got)
	}
}

func TestReadRootConfig_Default(t *testing.T) {
	srcFS := vfs.NewMemMapFs()
	c.Check(vfs.WriteFile(srcFS, Store.SrcRootFileName, nil, 0o644))

	if got := readRootConfig(srcFS).Unsafe.TTL; got != 15*time.Minute {
		t.Errorf("RootConfig.Unsafe.TTL=%s, want 15m", got)
	}
}

// Absence means the same thing as an empty marker file: defaults.
func TestReadRootConfig_AbsentUsesDefaults(t *testing.T) {
	if got := readRootConfig(vfs.NewMemMapFs()).Unsafe.TTL; got != 15*time.Minute {
		t.Errorf("RootConfig.Unsafe.TTL=%s, want default 15m", got)
	}
}

func TestReadRootConfig_InvalidPanics(t *testing.T) {
	srcFS := vfs.NewMemMapFs()
	c.Check(vfs.WriteFile(srcFS, Store.SrcRootFileName, []byte("[unsafe]\nttl = \"-1s\"\n"), 0o644))

	if err := c.Rescue(func() { readRootConfig(srcFS) }); err == nil {
		t.Fatal("invalid RootConfig toml must panic")
	}
}

func TestReadRootConfig_UnknownKeyPanics(t *testing.T) {
	cases := []struct{ name, toml, want string }{
		{name: "typo under unsafe", toml: "[unsafe]\nttll = \"1m\"\n", want: "unsafe.ttll"},
		// The root marker has no platform sections — [windows]/[unix] are BUFA-only.
		{name: "platform section", toml: "[windows]\ncmd = 'x'\n", want: "windows"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srcFS := vfs.NewMemMapFs()
			c.Check(vfs.WriteFile(srcFS, Store.SrcRootFileName, []byte(tc.toml), 0o644))

			err := c.Rescue(func() { readRootConfig(srcFS) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want unknown-key error naming %q, got %v", tc.want, err)
			}
		})
	}
}

func TestBuildResultDir(t *testing.T) {
	bldRoot := t.TempDir()
	rc := Config{BldRoot: bldRoot}

	got := rc.BuildResultDir("Dabc")
	want := filepath.Join(bldRoot, "out", "Dabc")
	if got != want {
		t.Errorf("BuildResultDir=%q, want %q", got, want)
	}
	if err := c.Rescue(func() { rc.BuildResultDir("") }); err == nil {
		t.Fatal("empty build hash must panic")
	}
}

func TestDefaultBldRoot_DriveRootDemandsEnv(t *testing.T) {
	// filepath.Dir(x) == x at the filesystem/drive root — the only condition the degenerate case fires on.
	var driveRoot string
	if runtime.GOOS == "windows" {
		driveRoot = filepath.VolumeName(c.Check2(os.Getwd())) + `\`
	} else {
		driveRoot = "/"
	}
	if filepath.Dir(driveRoot) != driveRoot {
		t.Fatalf("test premise broken: filepath.Dir(%q)=%q", driveRoot, filepath.Dir(driveRoot))
	}

	env := t.TempDir()
	t.Setenv("BUFA_BUILD_ROOT", env)
	got := defaultBldRoot(driveRoot)
	want := filepath.Join(env, Store.BuildRootDirSuffix)
	if got != want {
		t.Errorf("defaultBldRoot(%q)=%q, want %q (bare %s under $BUFA_BUILD_ROOT)",
			driveRoot, got, want, Store.BuildRootDirSuffix)
	}

	t.Setenv("BUFA_BUILD_ROOT", "")
	err := c.Rescue(func() { defaultBldRoot(driveRoot) })
	if err == nil {
		t.Fatal("a filesystem-root source without $BUFA_BUILD_ROOT must panic")
	}
	if !strings.Contains(err.Error(), "BUFA_BUILD_ROOT") {
		t.Errorf("panic must name $BUFA_BUILD_ROOT, got: %v", err)
	}
}

// Assert no marker of any kind sits above start before declaring a walk test valid.
func skipIfMarkersAbove(t *testing.T, start string) {
	t.Helper()
	for d := filepath.Dir(start); d != filepath.Dir(d); d = filepath.Dir(d) {
		for _, m := range []string{Store.SrcRootFileName, Store.GitRootMarker, Store.BuildConfigName} {
			if _, err := os.Lstat(filepath.Join(d, m)); err == nil {
				t.Skipf("%s sits in %s — cannot isolate the walk", m, d)
			}
		}
	}
}

func TestPrepare_MissingMarkerPanics(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", t.TempDir())
	startDir := t.TempDir()
	skipIfMarkersAbove(t, startDir)
	t.Chdir(startDir)
	err := c.Rescue(func() { PrepareConfig(true, false, false, io.Discard) })
	if err == nil {
		t.Fatal("missing all three markers must panic")
	}
	// The failure must name all three markers.
	for _, m := range []string{Store.SrcRootFileName, Store.GitRootMarker, Store.BuildConfigName} {
		if !strings.Contains(err.Error(), m) {
			t.Errorf("failure must name %s, got: %v", m, err)
		}
	}
}

func TestPrepare_GitDirAnchorsRoot(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", "") // isolate from user env
	parent := t.TempDir()
	skipIfMarkersAbove(t, parent)
	root := filepath.Join(parent, "proj")
	sub := filepath.Join(root, "a")
	c.Check(os.MkdirAll(filepath.Join(root, Store.GitRootMarker), 0o755))
	c.Check(os.MkdirAll(sub, 0o755))
	t.Chdir(sub)

	var buf strings.Builder
	rc := PrepareConfig(true, false, false, &buf)

	if rc.SrcRoot != root {
		t.Errorf("SrcRoot=%q, want repo root %q", rc.SrcRoot, root)
	}
	if !strings.Contains(buf.String(), "Src root: "+root+" ("+Store.GitRootMarker+")\n") {
		t.Errorf("Src root line must state the marker kind, got: %q", buf.String())
	}
	if !rc.rootAnchored {
		t.Error("a repository root is anchored")
	}
}

// Worktrees and submodules mark with a .git file, not a directory.
func TestPrepare_GitFileAnchorsRoot(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", "")
	parent := t.TempDir()
	skipIfMarkersAbove(t, parent)
	root := filepath.Join(parent, "wt")
	c.Check(os.MkdirAll(root, 0o755))
	c.Check(os.WriteFile(filepath.Join(root, Store.GitRootMarker), []byte("gitdir: elsewhere"), 0o644))
	t.Chdir(root)

	rc := PrepareConfig(true, false, false, io.Discard)

	if rc.SrcRoot != root {
		t.Errorf("SrcRoot=%q, want worktree root %q", rc.SrcRoot, root)
	}
}

func TestPrepare_TopmostBufaIsUnanchoredRoot(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", "")
	parent := t.TempDir()
	skipIfMarkersAbove(t, parent)
	top := filepath.Join(parent, "proj")
	sub := filepath.Join(top, "a", "b")
	c.Check(os.MkdirAll(sub, 0o755))
	c.Check(os.WriteFile(filepath.Join(top, Store.BuildConfigName), nil, 0o644))
	c.Check(os.WriteFile(filepath.Join(sub, Store.BuildConfigName), nil, 0o644))
	t.Chdir(sub)

	var buf strings.Builder
	rc := PrepareConfig(true, false, false, &buf)

	if rc.SrcRoot != top {
		t.Errorf("SrcRoot=%q, want topmost build dir %q", rc.SrcRoot, top)
	}
	if !strings.Contains(buf.String(), "(topmost "+Store.BuildConfigName+", never cached)") {
		t.Errorf("Src root line must flag an unanchored root, got: %q", buf.String())
	}
	if rc.rootAnchored {
		t.Error("a topmost-BUFA root is unanchored")
	}
}

// The .BUFA marker always wins, even when another marker sits nearer to the start dir.
func TestPrepare_RootMarkerBeatsNearerGit(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", "")
	parent := t.TempDir()
	umbrella := filepath.Join(parent, "top")
	repo := filepath.Join(umbrella, "repo")
	start := filepath.Join(repo, "src")
	c.Check(os.MkdirAll(filepath.Join(repo, Store.GitRootMarker), 0o755))
	c.Check(os.MkdirAll(start, 0o755))
	c.Check(os.WriteFile(filepath.Join(umbrella, Store.SrcRootFileName), nil, 0o644))
	t.Chdir(start)

	rc := PrepareConfig(true, false, false, io.Discard)

	if rc.SrcRoot != umbrella {
		t.Errorf("SrcRoot=%q, want umbrella %q (the .BUFA marker beats a nearer .git)", rc.SrcRoot, umbrella)
	}
}

// Regression: the walk's BUFA probe hit directories named like this very repo (a `Bufa` dir on the
// chain) case-insensitively and died on OSConfigExists' reserved-name panic — outside a project such
// a directory is legitimate and simply not a marker.
func TestWalk_BufaDirOnChainIgnored(t *testing.T) {
	base := t.TempDir()
	skipIfMarkersAbove(t, base)
	c.Check(os.MkdirAll(filepath.Join(base, Store.BuildConfigName), 0o755)) // directory, not marker
	proj := filepath.Join(base, "proj")
	c.Check(os.MkdirAll(filepath.Join(proj, Store.GitRootMarker), 0o755))
	start := filepath.Join(proj, "sub")
	c.Check(os.MkdirAll(start, 0o755))

	got, via := walkForSrcRoot(start)
	if got != proj || via != Store.GitRootMarker {
		t.Errorf("walk = %q via %s, want %q via %s (BUFA dir on chain must be skipped silently)",
			got, via, proj, Store.GitRootMarker)
	}
}

// The split-roots tripwire: a store next to a walked dir that is not the resolved root.
func TestPrepare_SplitRootsPanics(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", "")
	parent := t.TempDir()
	proj := filepath.Join(parent, "proj")
	c.Check(os.MkdirAll(proj, 0o755))
	c.Check(os.MkdirAll(filepath.Join(parent, "proj"+Store.BuildRootDirSuffix), 0o755))
	c.Check(os.WriteFile(filepath.Join(parent, Store.SrcRootFileName), nil, 0o644))
	t.Chdir(proj)

	err := c.Rescue(func() { PrepareConfig(true, false, false, io.Discard) })
	if err == nil {
		t.Fatal("a stale store next to a walked dir must be a hard error")
	}
	for _, want := range []string{"Build directory", proj, parent} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("split-roots error must name %q, got: %v", want, err)
		}
	}
}

// A store next to a chain dir ABOVE the resolved root is outside its tree — e.g. a live enclosing
// project's — and must not trip the split-roots check (probing stops at the resolved root).
func TestPrepare_StoreAboveRootNoError(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", "")
	parent := t.TempDir()
	skipIfMarkersAbove(t, parent)
	outer := filepath.Join(parent, "outer")
	proj := filepath.Join(outer, "proj")
	c.Check(os.MkdirAll(filepath.Join(proj, Store.GitRootMarker), 0o755))
	c.Check(os.MkdirAll(filepath.Join(parent, "outer"+Store.BuildRootDirSuffix), 0o755))
	t.Chdir(proj)

	rc := PrepareConfig(true, false, false, io.Discard)

	if rc.SrcRoot != proj {
		t.Fatalf("SrcRoot=%q, want %q", rc.SrcRoot, proj)
	}
}

// The resolved root's own sibling store is the normal layout, never an error.
func TestPrepare_OwnStoreNoError(t *testing.T) {
	t.Setenv("BUFA_BUILD_ROOT", "")
	parent := t.TempDir()
	proj := markedDir(t, filepath.Join(parent, "proj"))
	c.Check(os.MkdirAll(filepath.Join(parent, "proj"+Store.BuildRootDirSuffix), 0o755))
	t.Chdir(proj)

	rc := PrepareConfig(true, false, false, io.Discard)

	if rc.SrcRoot != proj {
		t.Fatalf("SrcRoot=%q, want %q", rc.SrcRoot, proj)
	}
}

func TestNewTest_CapturesBuildStartTime(t *testing.T) {
	srcFS := vfs.NewMemMapFs()
	c.Check(vfs.WriteFile(srcFS, Store.SrcRootFileName, nil, 0o644)) // NewTest resolves RootConfig from disk
	rc := NewTest(srcFS, vfs.NewMemMapFs(), t.TempDir(), t.TempDir(), io.Discard, true)

	if rc.BuildStartTimeUTC.IsZero() {
		t.Error("BuildStartTime must be captured at startup")
	}
	if rc.BuildStartTimeUTC.Location() != time.UTC {
		t.Errorf("BuildStartTime location=%s, want UTC", rc.BuildStartTimeUTC.Location())
	}
}

func TestNewTest_ResolvesRootConfig(t *testing.T) {
	srcFS := vfs.NewMemMapFs()
	c.Check(vfs.WriteFile(srcFS, Store.SrcRootFileName, []byte("[unsafe]\nttl = \"20m\"\n"), 0o644))
	rc := NewTest(srcFS, vfs.NewMemMapFs(), t.TempDir(), t.TempDir(), io.Discard, true)

	if got := rc.RootConfig.Unsafe.TTL; got != 20*time.Minute {
		t.Errorf("RootConfig.Unsafe.TTL=%s, want 20m", got)
	}
}

func TestConfig_PerDirScopesCoverEveryTarget(t *testing.T) {
	rc := Config{TargetDirs: []string{"a", "b"}}

	for _, tt := range []struct {
		scope DirScope
		a, c  bool
	}{
		{ScopeNone, false, false},
		{ScopeTarget, true, false},
		{ScopeAll, true, true},
	} {
		rc.ShowOutput = tt.scope
		rc.ForceRebuild = tt.scope
		for dir, want := range map[string]bool{"a": tt.a, "b": tt.a, "c": tt.c} {
			if got := rc.ShowOutputFor(dir); got != want {
				t.Errorf("scope %v: ShowOutputFor(%q)=%v, want %v", tt.scope, dir, got, want)
			}
			if got := rc.ForceRebuildFor(dir); got != want {
				t.Errorf("scope %v: ForceRebuildFor(%q)=%v, want %v", tt.scope, dir, got, want)
			}
		}
	}

	rc.ShowOutput = ScopeNone
	rc.ForceRebuild = ScopeNone
	rc.BuildMode = ModeShell
	for dir, want := range map[string]bool{"a": true, "b": true, "c": false} {
		if got := rc.ShellSessionFor(dir); got != want {
			t.Errorf("ModeShell: ShellSessionFor(%q)=%v, want %v", dir, got, want)
		}
		if got := rc.ForceRebuildFor(dir); got != want {
			t.Errorf("ModeShell: ForceRebuildFor(%q)=%v, want %v (a session implies --force)", dir, got, want)
		}
	}
	if got := rc.BuildModeFor("c"); got != ModeBuild {
		t.Errorf("BuildModeFor(non-target)=%v, want ModeBuild", got)
	}
}
