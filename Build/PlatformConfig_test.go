package Build

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dimagog/bufa/BuildConfig"
	c "github.com/dimagog/bufa/internal/contract"
)

// [unix] carries a failing script; the running platform's leaf section ([windows]/[linux]/[macos]) must win.
func TestBuild_LeafSectionOverridesUnix(t *testing.T) {
	leaf := BuildConfig.PlatformSections[len(BuildConfig.PlatformSections)-1]
	if leaf == "unix" {
		t.Skip("no leaf section for " + runtime.GOOS)
	}
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"), "cmd = '''"+byOS(winFail, unixFail)+"'''\n"+
		"[unix]\ncmd = '''"+unixFail+"'''\n"+
		"["+leaf+"]\ncmd = '''"+byOS("@echo off\r\necho hi>r.txt\r\n", "printf hi > r.txt\n")+"'''\n")
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("P")

	got := c.Check2(os.ReadFile(filepath.Join(f.bld, "out", h, "r.txt")))
	if strings.TrimSpace(string(got)) != "hi" {
		t.Errorf("build output = %q, want hi from the [%s] script", got, leaf)
	}
}

func platformDepsToml(name string) string {
	return "[" + name + ".deps]\nsrc = ['../C']\nbld = ['../D']\n"
}

func TestBuild_PlatformDeps_SrcBldConcatResolve(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "Q", "BUFA"),
		"[deps]\nsrc = ['../A']\nbld = ['../B']\n"+platformDepsToml("windows")+platformDepsToml("unix"))

	cfg := f.builder().getBuildConfig(filepath.Join("P", "Q"))

	wantSrc := []string{filepath.Join("P", "A"), filepath.Join("P", "C")}
	wantBld := []string{filepath.Join("P", "B"), filepath.Join("P", "D")}
	if !slices.Equal(cfg.Deps.Src, wantSrc) || !slices.Equal(cfg.Deps.Bld, wantBld) {
		t.Errorf("deps src/bld = %v/%v, want %v/%v — platform deps must concat after root and resolve relative",
			cfg.Deps.Src, cfg.Deps.Bld, wantSrc, wantBld)
	}
}

func TestBuild_UnknownKeyRejected(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"), "unknwn = 1\n"+bufaToml(winEcho, unixEcho))

	err := c.Rescue(func() { f.builder().getBuildConfig("P") })
	if err == nil || !strings.Contains(err.Error(), "unknwn") {
		t.Errorf("want unknown-key error naming unknwn, got %v", err)
	}
}

func TestBuild_PlatformEnvMergedAndResolved(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "v.ver"), "1.0")
	f.write(t, filepath.Join("P", "e.ver"), "2.0")
	section := "VER = '9.9'\nEXTRA = { file = 'e.ver' }\n"
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nVER = { file = 'v.ver' }\n"+
			extToml("http://x.invalid/a-${VER}-${EXTRA}.jar", pinA)+
			"[windows.env]\n"+section+"[unix.env]\n"+section)

	cfg := f.builder().getBuildConfig("P")

	// VER: the platform override in the top-level key's position; EXTRA: file-sourced, resolved against the build dir.
	want := []string{"VER=9.9", "EXTRA=2.0"}
	if got := envLiteralPairs(cfg.Env); !slices.Equal(got, want) {
		t.Errorf("Env = %v, want %v", got, want)
	}
	wantUrl := "http://x.invalid/a-9.9-2.0.jar"
	if cfg.Deps.Ext[0].Url != wantUrl || cfg.Deps.Ext[0].Name != "a-9.9-2.0.jar" {
		t.Errorf("ext url/name = %q/%q, want interpolated from the merged env", cfg.Deps.Ext[0].Url, cfg.Deps.Ext[0].Name)
	}
}

func TestBuild_PlatformEnv_CrossSectionCaseFoldCollision(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nVER = '1'\n[windows.env]\nver = '2'\n[unix.env]\nver = '2'\n")

	err := c.Rescue(func() { f.builder().getBuildConfig("P") })
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("want duplicate [env] error, got %v", err)
	}
}

func TestBuild_ScriptForms(t *testing.T) {
	script := "printf hi > r.txt\n"
	if runtime.GOOS == "windows" {
		script = "@echo off\r\necho hi>r.txt\r\n"
	}
	cases := []struct{ name, bufa string }{
		{name: "root cmd", bufa: "cmd = '''" + script + "'''\n"},
		{name: "raw script", bufa: script}, // the BUFA IS the script — not parseable as TOML
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write(t, filepath.Join("P", "BUFA"), tc.bufa)
			b := f.builder()
			skipIfNoSymlinks(t, b.store)

			h := b.Build("P")

			got := c.Check2(os.ReadFile(filepath.Join(f.bld, "out", h, "r.txt")))
			if strings.TrimSpace(string(got)) != "hi" {
				t.Errorf("build output = %q, want hi", got)
			}
		})
	}
}
