package Build

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/ArtifactCache"
	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Runtime"
	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

// The repo's Examples/ project, built against a scratch store — what Examples/buildall.cmd enumerates. No network
// in tests: archive-backed dirs require a cache hit; ambient providers require their bare executable on bufa's PATH.
// The one exception is clj, once its pins are cached: its Maven repo lives in the store, so a scratch one refetches.
type examplesProject struct {
	b        *Builder
	src, bld string
	cache    *ArtifactCache.Cache
}

const examplesRequiredEnv = "BUFA_TEST_EXAMPLES_REQUIRED"

// CI's examples job sets the variable: there a skip means nothing was verified, so it fails instead.
func failIfSkipped(t *testing.T) {
	if os.Getenv(examplesRequiredEnv) != "" {
		t.Cleanup(func() {
			if t.Skipped() {
				t.Errorf("skipped while $%s is set", examplesRequiredEnv)
			}
		})
	}
}

func openExamples(t *testing.T) examplesProject {
	failIfSkipped(t)
	examples := c.Check2(filepath.Abs(filepath.Join("..", "Examples")))
	srcFS := vfs.NewBasePathFs(vfs.NewOsFs(), examples)
	bld := t.TempDir()
	rc := Runtime.NewTest(srcFS, vfs.NewBasePathFs(vfs.NewOsFs(), bld), examples, bld, io.Discard, true)
	b := NewBuilder(&rc)
	skipIfNoSymlinks(t, b.store)
	return examplesProject{b: b, src: examples, bld: bld, cache: ArtifactCache.New(ArtifactCache.GetCacheDir())}
}

func (p examplesProject) hasPlatformCmd(dir string) bool {
	cfg := p.b.getBuildConfig(dir)
	return cfg.Cmd.Script != "" || cfg.Cmd.Disabled
}

// "" when dir has a command on this platform and each pinned archive is cached; unit is the `bufa` arg that caches it.
func (p examplesProject) unavailable(dir, unit string) string {
	if !p.hasPlatformCmd(dir) {
		return dir + " is unavailable on this platform"
	}
	for _, dep := range p.b.getBuildConfig(dir).Deps.Ext {
		if !p.cache.Contains(dep.Hash) {
			return dir + "'s pinned archive is not cached; run `bufa " + unit + "` in Examples/ once"
		}
	}
	return ""
}

func (p examplesProject) shellProviderOf(dir string) string {
	shell := p.b.shellFor(dir, p.b.getBuildConfig(dir))
	_, preset := decodeShellPreset(shell)
	c.Require(shell != "" && !preset, "unit %s: shell '%s' is not provider-backed", dir, shell)
	return shell
}

func (p examplesProject) shellProviderUnavailable(provider, unit string) string {
	reason := p.unavailable(provider, unit)
	if reason == "" {
		defPath := filepath.Join(p.src, provider, Store.ShellDefFileName)
		def := BuildConfig.DecodeShellDef(c.Check2(os.ReadFile(defPath)), defPath)
		if def.IsBareExe() {
			if _, err := exec.LookPath(def.Exe); err != nil {
				reason = def.Exe + " is not on bufa's PATH"
			}
		}
	}
	return reason
}

func (p examplesProject) output(key, name string) string {
	return strings.TrimSpace(string(c.Check2(os.ReadFile(filepath.Join(p.bld, "out", key, name)))))
}

func TestExamples_HelloUnits(t *testing.T) {
	p := openExamples(t)
	configs := c.Check2(filepath.Glob(filepath.Join(p.src, "hello", "*"+Store.VirtualConfigSuffix)))
	if len(configs) == 0 {
		t.Fatal("no hello/*.BUFA units found")
	}

	for _, cfgPath := range configs {
		unit := strings.TrimSuffix(filepath.Base(cfgPath), Store.VirtualConfigSuffix)
		dir := filepath.Join("hello", unit)
		// hello/all is a scriptless aggregator: no shell, no hello.txt.
		if !p.b.getBuildConfig(dir).Cmd.Disabled {
			t.Run(unit, func(t *testing.T) { p.testHelloUnit(t, unit, dir) })
		}
	}
}

func (p examplesProject) testHelloUnit(t *testing.T, unit, dir string) {
	// The one legitimate skip (hello/powershell off Windows), hence before failIfSkipped.
	if !p.hasPlatformCmd(dir) {
		t.Skip(dir + " is unavailable on this platform")
	}
	failIfSkipped(t)
	provider := p.shellProviderOf(dir)
	reason := p.unavailable(dir, "hello/"+unit)
	if reason == "" {
		reason = p.shellProviderUnavailable(provider, "hello/"+unit)
	}
	if reason != "" {
		t.Skip(reason)
	}
	key := p.b.Build(dir)
	hello := p.output(key, "hello.txt")
	dirTxt := p.output(key, "dir.txt")
	if want := "hello from " + filepath.Base(provider); hello != want || dirTxt != dir {
		t.Errorf("hello.txt = %q (want %q), dir.txt = %q (want %q)", hello, want, dirTxt, dir)
	}
}

// Each consumer gets its tools from its bld deps' published BUFA.env files alone.
func TestExamples_Toolchains(t *testing.T) {
	p := openExamples(t)
	for _, unit := range []string{"java", "go", "clj", "rust"} {
		t.Run(unit, func(t *testing.T) {
			failIfSkipped(t)
			reason := ""
			// rust links with an ambient linker a dev machine may lack.
			if unit == "rust" && os.Getenv(examplesRequiredEnv) == "" {
				reason = "rust is built by CI's examples job only"
			}
			if reason == "" {
				reason = p.shellProviderUnavailable(p.shellProviderOf(unit), unit)
			}
			for _, dep := range p.b.getBuildConfig(unit).Deps.Bld {
				if reason == "" {
					reason = p.unavailable(dep, unit)
				}
			}
			if reason != "" {
				t.Skip(reason)
			}

			hello := p.output(p.b.Build(unit), "hello.txt")
			if !strings.HasPrefix(hello, "hello from "+unit+" ") {
				t.Errorf("hello.txt = %q, want 'hello from %s <version>'", hello, unit)
			}
		})
	}
}

// Each deploy/* task copies hello/default's hello.txt into the dir its first argument names; the rest is echoed.
func TestExamples_DeployUnits(t *testing.T) {
	p := openExamples(t)
	configs := c.Check2(filepath.Glob(filepath.Join(p.src, "deploy", "*"+Store.VirtualConfigSuffix)))
	if len(configs) == 0 {
		t.Fatal("no deploy/*.BUFA units found")
	}
	var out bytes.Buffer
	p.b.Out = &out
	p.b.ShowOutput = Runtime.ScopeAll

	for _, cfgPath := range configs {
		unit := strings.TrimSuffix(filepath.Base(cfgPath), Store.VirtualConfigSuffix)
		dir := filepath.Join("deploy", unit)
		t.Run(unit, func(t *testing.T) {
			failIfSkipped(t)
			reason := p.shellProviderUnavailable(p.shellProviderOf(dir), dir)
			if reason == "" {
				reason = p.shellProviderUnavailable(p.shellProviderOf(filepath.Join("hello", "default")), "hello/default")
			}
			if reason != "" {
				t.Skip(reason)
			}
			drop := filepath.Join(t.TempDir(), "drop")
			out.Reset()
			p.b.BindArgs(dir, []string{drop, "v2", "final"})
			if key := p.b.Build(dir); key != "" {
				t.Errorf("a task publishes nothing, got %q", key)
			}
			hello := strings.TrimSpace(string(c.Check2(os.ReadFile(filepath.Join(drop, "hello.txt")))))
			if hello != "hello from nu" || !strings.Contains(out.String(), "deployed hello.txt to "+drop+" v2 final") {
				t.Errorf("hello.txt = %q, output:\n%s", hello, out.String())
			}
		})
	}
}
