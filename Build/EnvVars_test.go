package Build

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

func TestBuild_EnvVars_Validation(t *testing.T) {
	cases := []struct {
		name, toml, want string
		files            map[string]string
	}{
		{name: "unknown table key", toml: "[env]\nX = { files = 'v.ver' }\n", want: "got key"},
		{name: "empty table", toml: "[env]\nX = { }\n", want: "exactly one"},
		{name: "wrong value type", toml: "[env]\nX = 42\n", want: "got int64"},
		{name: "empty file path", toml: "[env]\nX = { file = '' }\n", want: "empty file"},
		{name: "escaping file", toml: "[env]\nX = { file = '../v.ver' }\n", want: "strictly-local"},
		{name: "reserved file BUFA", toml: "[env]\nX = { file = 'BUFA' }\n", want: "reserved"},
		{name: "reserved file suffix", toml: "[env]\nX = { file = 'v.BUFA' }\n", want: "reserved"},
		{name: "empty variable name", toml: "[env]\n'' = 'x'\n", want: "must not be empty"},
		{name: "case-folded duplicate", toml: "[env]\nVer = '1'\nVER = '2'\n", want: "duplicate"},
		{name: "reserved BUFA_ prefix", toml: "[env]\nbufa_thing = '1'\n", want: "reserved BUFA_ prefix"},
		{name: "absent file", toml: "[env]\nX = { file = 'v.ver' }\n", want: "does not exist"},
		{name: "empty file content", toml: "[env]\nX = { file = 'v.ver' }\n",
			files: map[string]string{"v.ver": "  \n"}, want: "is empty"},
		{name: "multi-line file", toml: "[env]\nX = { file = 'v.ver' }\n",
			files: map[string]string{"v.ver": "1.0\n2.0\n"}, want: "single line"},
		{name: "empty literal", toml: "[env]\nX = ''\n", want: "is empty"},
		{name: "multi-line literal", toml: "[env]\nX = \"\"\"1\n2\"\"\"\n", want: "single line"},
		{name: "undefined variable", toml: extToml("http://x.invalid/a-${VER}.jar", pinA), want: "undefined"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write(t, filepath.Join("P", "BUFA"), tc.toml)
			for name, data := range tc.files {
				f.write(t, filepath.Join("P", name), data)
			}
			requireErrorContains(t, c.Rescue(func() { f.builder().getBuildConfig("P") }), tc.want)
		})
	}
}

func TestBuild_EnvVars_VirtualConfig(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"), "")
	f.write(t, filepath.Join("P", "V.BUFA"), "[env]\nX = { file = 'v.ver' }\n")
	requireErrorContains(t, c.Rescue(func() { f.builder().getBuildConfig(filepath.Join("P", "V")) }), "has no own source")

	f.write(t, filepath.Join("P", "W.BUFA"),
		"[env]\nVER = '3'\n"+extToml("http://x.invalid/a-${VER}.jar", pinA))
	cfg := f.builder().getBuildConfig(filepath.Join("P", "W"))
	if cfg.Deps.Ext[0].Url != "http://x.invalid/a-3.jar" || cfg.Deps.Ext[0].Name != "a-3.jar" {
		t.Errorf("virtual literal interpolation: url %q, name %q", cfg.Deps.Ext[0].Url, cfg.Deps.Ext[0].Name)
	}
}

func TestBuild_EnvVars_UrlInterpolation(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "antlr.ver"), "4.13.2\r\n") // CRLF-authored: TrimSpace handles it
	f.write(t, filepath.Join("P", "unused.ver"), "x")
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nANTLR_VER = { file = 'antlr.ver' }\nUNUSED = { file = 'unused.ver' }\nJAVA_VER = '26.0.1'\n"+
			extToml("https://www.antlr.org/download/antlr-${ANTLR_VER}-complete.jar", pinA)+
			extToml("http://x.invalid/jdk-${JAVA_VER}.zip", pinB))

	cfg := f.builder().getBuildConfig("P")

	if got := cfg.Deps.Ext[0].Url; got != "https://www.antlr.org/download/antlr-4.13.2-complete.jar" {
		t.Errorf("file-sourced url = %q", got)
	}
	if got := cfg.Deps.Ext[0].Name; got != "antlr-4.13.2-complete.jar" {
		t.Errorf("default name = %q, want it derived from the interpolated url", got)
	}
	if got := cfg.Deps.Ext[1].Url; got != "http://x.invalid/jdk-26.0.1.zip" {
		t.Errorf("literal url = %q", got)
	}
	if got := cfg.Deps.Ext[1].Name; got != "jdk-26.0.1.zip" {
		t.Errorf("literal default name = %q", got)
	}
	want := []string{"ANTLR_VER=4.13.2", "UNUSED=x", "JAVA_VER=26.0.1"}
	if got := envLiteralPairs(cfg.Env); !slices.Equal(got, want) {
		t.Errorf("Env = %v, want every entry rewritten to its literal, in document order: %v", got, want)
	}
}

// name=value in document order; a still-file-sourced entry shows as name={file}.
func envLiteralPairs(table envTable) []string {
	pairs := make([]string, 0, table.Len())
	for name, v := range table.All() {
		if v.File != "" {
			pairs = append(pairs, name+"={"+v.File+"}")
		} else {
			pairs = append(pairs, name+"="+v.Value)
		}
	}
	return pairs
}

func TestBuild_EnvVars_UrlReferenceToReferenceIsStatic(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nA = '1'\nB = '${A}'\nC = '$${A}'\n"+extToml("http://x.invalid/a-${B}.jar", pinA))
	requireErrorContains(t, c.Rescue(func() { f.builder().getBuildConfig("P") }), "'B' whose value '${A}' holds '${'")

	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nA = '1'\nC = '$${A}'\n"+extToml("http://x.invalid/a-${C}.jar", pinA))
	requireErrorContains(t, c.Rescue(func() { f.builder().getBuildConfig("P") }), "'C' whose value '$${A}' holds '${'")
}

const (
	winRefScript = "@echo off\r\n>a.txt echo %A%\r\n>b.txt echo %B%\r\n>c.txt echo %C%\r\n>d.txt echo %D%\r\n" +
		">bd.txt echo %BUFA_BUILD_DIR%\r\n"
	unixRefScript = "printf '%s' \"$A\" > a.txt\nprintf '%s' \"$B\" > b.txt\nprintf '%s' \"$C\" > c.txt\n" +
		"printf '%s' \"$D\" > d.txt\nprintf '%s' \"$BUFA_BUILD_DIR\" > bd.txt\n"
)

// Z before A: sorted-key export would see A's reference to Z as a forward reference.
const refToml = "[env]\nZ = 'z'\nA = '${Z}a'\nB = '${A}${Z}'\nC = '$${A}${B}'\nD = '${BUFA_BUILD_DIR}'\n"

var refWant = map[string]string{"a.txt": "za", "b.txt": "zaz", "c.txt": "${A}zaz"}

func TestBuild_EnvVars_Interpolation(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"), refToml+bufaToml(winRefScript, unixRefScript))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("P")

	for name, want := range refWant {
		if got := strings.TrimSpace(f.readOut(t, h, name)); got != want {
			t.Errorf("script saw %s = %q, want %q", name, got, want)
		}
	}
	bldDir := strings.TrimSpace(f.readOut(t, h, "bd.txt"))
	if got := strings.TrimSpace(f.readOut(t, h, "d.txt")); bldDir == "" || got != bldDir {
		t.Errorf("script saw D = %q, want the bufa var it references, %q", got, bldDir)
	}
}

func TestDirtyBuild_EnvVars_Interpolation(t *testing.T) {
	t.Setenv("ENVREF_TEST", "os")
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		strings.Replace(refToml, "'${Z}a'", "'${Z}a${ENVREF_TEST}'", 1)+bufaToml(winRefScript, unixRefScript))

	f.dirtyBuilder().Build("P")

	for name, want := range map[string]string{"a.txt": "zaos", "b.txt": "zaosz", "c.txt": "${A}zaosz"} {
		if got := strings.TrimSpace(f.readSrc(t, "P/"+name)); got != want {
			t.Errorf("script saw %s = %q, want %q", name, got, want)
		}
	}
	bldDir := strings.TrimSpace(f.readSrc(t, "P/bd.txt"))
	if got := strings.TrimSpace(f.readSrc(t, "P/d.txt")); bldDir == "" || got != bldDir {
		t.Errorf("script saw D = %q, want the bufa var it references, %q", got, bldDir)
	}
}

// t.Setenv registers the restore; the tests need the name absent, not set.
func unsetEnv(t *testing.T, name string) {
	t.Setenv(name, "")
	os.Unsetenv(name)
}

func TestBuild_EnvVars_InterpolationErrors(t *testing.T) {
	unsetEnv(t, "A") // an ambient A would resolve the self reference
	unsetEnv(t, "ENVREF_NOPE")
	cases := []struct {
		name, toml, want string
	}{
		{name: "forward reference", toml: "[env]\nA = '${Z}'\nZ = '1'\n",
			want: "references [env] variable 'Z' (re)defined later"},
		{name: "forward reference to platform-only", toml: "[env]\nA = '${Z} x'\n[windows.env]\nZ = 'w'\n[unix.env]\nZ = 'u'\n",
			want: "references [env] variable 'Z' (re)defined later"},
		{name: "unsafe forward reference", toml: "unsafe = true\n[env]\nA = '${Z}'\nZ = '1'\n",
			want: "references [env] variable 'Z' (re)defined later"},
		{name: "self reference", toml: "[env]\nA = '${A}'\n", want: "undefined variable 'A'"},
		{name: "undefined OS variable", toml: "[env]\nA = '${ENVREF_NOPE}'\n", want: "undefined variable 'ENVREF_NOPE'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.write(t, filepath.Join("P", "BUFA"), tc.toml+bufaToml(winEcho, unixEcho))
			err := c.Rescue(func() { f.dirtyBuilder().Build("P") })
			requireErrorContains(t, err, "[env] variable 'A' of 'P'\n")
			requireErrorContains(t, err, tc.want)
		})
	}
}

func TestBuild_EnvVars_InterpolationFoldsLikeOS(t *testing.T) {
	unsetEnv(t, "z") // an ambient z would resolve the Unix exact-case miss
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nZ = 'z'\nA = '${z}'\n"+bufaToml(winEcho, unixEcho))

	err := c.Rescue(func() { f.dirtyBuilder().Build("P") })

	if runtime.GOOS == "windows" {
		if err != nil {
			t.Errorf("case-folded reference must resolve on Windows: %v", err)
		}
	} else {
		requireErrorContains(t, err, "undefined variable 'z'")
	}
}

func TestDirtyBuild_EnvVars_FileValueExpands(t *testing.T) {
	win := "@echo off\r\n>a.txt echo %A%\r\n"
	unix := "printf '%s' \"$A\" > a.txt\n"
	f := newFixture(t)
	f.write(t, filepath.Join("P", "a.ver"), "${Z} $${Z}\n")
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nZ = 'z'\nA = { file = 'a.ver' }\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	if got := strings.TrimSpace(f.readSrc(t, "P/a.txt")); got != "z ${Z}" {
		t.Errorf("script saw A = %q, want the file contents expanded like a literal", got)
	}
}

func TestDirtyBuild_EnvVars_PlatformFoldKeepsPosition(t *testing.T) {
	win := "@echo off\r\n>a.txt echo %A%\r\n>b.txt echo %B%\r\n>c.txt echo %C%\r\n"
	unix := "printf '%s' \"$A\" > a.txt\nprintf '%s' \"$B\" > b.txt\nprintf '%s' \"$C\" > c.txt\n"
	section := "A = 'w'\nC = '${B}c'\n"
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		"[windows.env]\n"+section+"[unix.env]\n"+section+"[env]\nA = '1'\nB = '${A}b'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	for name, want := range map[string]string{"a.txt": "w", "b.txt": "wb", "c.txt": "wbc"} {
		if got := strings.TrimSpace(f.readSrc(t, "P/"+name)); got != want {
			t.Errorf("script saw %s = %q, want %q", name, got, want)
		}
	}
}

func TestDirtyBuild_RootEnvVars_Interpolation(t *testing.T) {
	win := "@echo off\r\n>s.txt echo %S%\r\n>t.txt echo %T%\r\n>d.txt echo %D%\r\n"
	unix := "printf '%s' \"$S\" > s.txt\nprintf '%s' \"$T\" > t.txt\nprintf '%s' \"$D\" > d.txt\n"
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nS = 'root'\nT = '${S}!'\n")
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nD = '${T}${S}'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	for name, want := range map[string]string{"s.txt": "root", "t.txt": "root!", "d.txt": "root!root"} {
		if got := strings.TrimSpace(f.readSrc(t, "P/"+name)); got != want {
			t.Errorf("script saw %s = %q, want %q", name, got, want)
		}
	}
}

func TestDirtyBuild_RootEnvVars_SelfReferenceChainsRoot(t *testing.T) {
	win := "@echo off\r\n>d.txt echo %D%\r\n"
	unix := "printf '%s' \"$D\" > d.txt\n"
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nD = 'root'\n")
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nD = '${D} plus'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	// A self-reference sees the root layer's value: the dir redefinition builds on it, not an error.
	if got := strings.TrimSpace(f.readSrc(t, "P/d.txt")); got != "root plus" {
		t.Errorf("script saw D = %q, want %q", got, "root plus")
	}
}

func TestDirtyBuild_RootEnvVars_SelfReferenceChainsBothLayers(t *testing.T) {
	t.Setenv("CHAIN_TEST", "os")
	t.Setenv("DISCARD_TEST", "os")
	win := "@echo off\r\n>c.txt echo %CHAIN_TEST%\r\n>x.txt echo %DISCARD_TEST%\r\n"
	unix := "printf '%s' \"$CHAIN_TEST\" > c.txt\nprintf '%s' \"$DISCARD_TEST\" > x.txt\n"
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nCHAIN_TEST = '${CHAIN_TEST}+root'\nDISCARD_TEST = '${DISCARD_TEST}+root'\n")
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nCHAIN_TEST = '${CHAIN_TEST}+dir'\nDISCARD_TEST = 'fixed'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	// A root self-reference is exempt from "(re)defined later" even when the dir redefines the key:
	// the dir either chains on the root value or deliberately discards it.
	if got := strings.TrimSpace(f.readSrc(t, "P/c.txt")); got != "os+root+dir" {
		t.Errorf("script saw CHAIN_TEST = %q, want %q", got, "os+root+dir")
	}
	if got := strings.TrimSpace(f.readSrc(t, "P/x.txt")); got != "fixed" {
		t.Errorf("script saw DISCARD_TEST = %q, want %q", got, "fixed")
	}
}

func TestDirtyBuild_RootEnvVars_RedefinedAfterUse(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nS = 'root'\nT = '${S}!'\n")
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nS = 'dir'\n"+bufaToml(winEcho, unixEcho))

	err := c.Rescue(func() { f.dirtyBuilder().Build("P") })

	// T would bake the root S while the script sees the dir S.
	requireErrorContains(t, err, "[env] variable 'T' of '"+Store.SrcRootFileName+"'\n")
	requireErrorContains(t, err, "references [env] variable 'S' (re)defined later")
}

func TestDirtyBuild_RootEnvVars_ForwardReferenceToDir(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nR = '${D}'\n")
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nD = '1'\n"+bufaToml(winEcho, unixEcho))

	err := c.Rescue(func() { f.dirtyBuilder().Build("P") })

	requireErrorContains(t, err, "[env] variable 'R' of '"+Store.SrcRootFileName+"'\n")
	requireErrorContains(t, err, "references [env] variable 'D' (re)defined later")
}

func TestDirtyBuild_RootEnvVars_PlatformOnlyOverrideAfterUse(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nA = 'root'\n")
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nB = '${A} B'\n[windows.env]\nA = 'plat'\n[unix.env]\nA = 'plat'\n"+bufaToml(winEcho, unixEcho))

	err := c.Rescue(func() { f.dirtyBuilder().Build("P") })

	// B would bake the root A while the script sees the platform A (appended after B in the dir table).
	requireErrorContains(t, err, "[env] variable 'B' of 'P'\n")
	requireErrorContains(t, err, "references [env] variable 'A' (re)defined later")
}

func TestBuild_EnvVars_LoneDollarVerbatim(t *testing.T) {
	f := newFixture(t)
	url := "http://x.invalid/a$b/c$5.jar$?x=${}&y=${oops"
	f.write(t, filepath.Join("P", "BUFA"), extToml(url, pinA, "name = 'a.jar'"))

	cfg := f.builder().getBuildConfig("P")

	if got := cfg.Deps.Ext[0].Url; got != url {
		t.Errorf("unbraced $ must pass verbatim: url = %q, want %q", got, url)
	}
}

func TestBuild_EnvVars_EscapedReference(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nVER = '9'\n"+extToml("http://x.invalid/a-$${VER}.jar?x=$${oops", pinA, "name = 'a.jar'"))

	cfg := f.builder().getBuildConfig("P")

	want := "http://x.invalid/a-${VER}.jar?x=${oops"
	if got := cfg.Deps.Ext[0].Url; got != want {
		t.Errorf("escaped url = %q, want %q", got, want)
	}
}

func TestBuild_EnvVars_PlatformMergedUrlInterpolation(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "v.ver"), "1.2")
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nVER = { file = 'v.ver' }\n"+
			"[[windows.deps.ext]]\nurl = 'http://x.invalid/tool-${VER}.exe'\nhash = '"+pinB+"'\n"+
			"[[unix.deps.ext]]\nurl = 'http://x.invalid/tool-${VER}'\nhash = '"+pinB+"'\n")

	cfg := f.builder().getBuildConfig("P")

	wantUrl, wantName := "http://x.invalid/tool-1.2.exe", "tool-1.2.exe"
	if runtime.GOOS != "windows" {
		wantUrl, wantName = "http://x.invalid/tool-1.2", "tool-1.2"
	}
	if got := cfg.Deps.Ext[0].Url; got != wantUrl {
		t.Errorf("platform-merged url = %q, want %q", got, wantUrl)
	}
	if got := cfg.Deps.Ext[0].Name; got != wantName {
		t.Errorf("platform-merged name = %q, want %q", got, wantName)
	}
}

func TestBuild_EnvVars_EndToEndFetch(t *testing.T) {
	data := []byte("JARBYTES7")
	f, _, url, hits := extFixture(t, &data)
	url = strings.TrimSuffix(url, "art.bin") + "art-${VER}.bin" // the server answers every path
	f.write(t, filepath.Join("P", "v.ver"), "7")
	f.write(t, filepath.Join("P", "BUFA"),
		"cmd = false\n[env]\nVER = { file = 'v.ver' }\n"+extToml(url, pinOf(data), "export = true"))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("P")

	published := filepath.Join(f.bld, "out", h, "art-7.bin")
	if got := c.Check2(os.ReadFile(published)); !bytes.Equal(got, data) {
		t.Errorf("published bytes = %q, want %q", got, data)
	}
	if *hits != 1 {
		t.Fatalf("server hits = %d, want 1", *hits)
	}

	if h2 := f.builder().Build("P"); h2 != h {
		t.Errorf("repeat build hash %q != %q", h2, h)
	}
	if *hits != 1 {
		t.Errorf("repeat build hit the network: %d hits", *hits)
	}
}

const (
	winEnvScript  = "@echo off\r\necho %FILE_VER%>v.txt\r\necho %LIT_VER%>l.txt\r\n"
	unixEnvScript = "printf '%s' \"$FILE_VER\" > v.txt\nprintf '%s' \"$LIT_VER\" > l.txt\n"
)

const envScriptToml = "[env]\nFILE_VER = { file = 'v.ver' }\nLIT_VER = '26.0.1'\n"

func TestBuild_EnvVars_ScriptEnv(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "v.ver"), "9.9")
	f.write(t, filepath.Join("P", "BUFA"), envScriptToml+bufaToml(winEnvScript, unixEnvScript))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("P")

	for name, want := range map[string]string{"v.txt": "9.9", "l.txt": "26.0.1"} {
		got := c.Check2(os.ReadFile(filepath.Join(f.bld, "out", h, name)))
		if strings.TrimSpace(string(got)) != want {
			t.Errorf("script saw %s = %q, want %q", name, got, want)
		}
	}
}

func TestDirtyBuild_EnvVars_ScriptEnv(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("P", "v.ver"), "9.9")
	f.write(t, filepath.Join("P", "BUFA"), envScriptToml+bufaToml(winEnvScript, unixEnvScript))

	f.dirtyBuilder().Build("P")

	if got := f.readSrc(t, "P/v.txt"); got != "9.9" {
		t.Errorf("script saw FILE_VER = %q, want 9.9", got)
	}
	if got := f.readSrc(t, "P/l.txt"); got != "26.0.1" {
		t.Errorf("script saw LIT_VER = %q, want 26.0.1", got)
	}
}

func TestBuild_EnvVars_OverridesInherited(t *testing.T) {
	t.Setenv("ENVOVERRIDE_TEST", "ambient")
	win := "@echo off\r\necho %ENVOVERRIDE_TEST%>seen.txt\r\n"
	unix := "printf '%s' \"$ENVOVERRIDE_TEST\" > seen.txt\n"
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nENVOVERRIDE_TEST = '${ENVOVERRIDE_TEST} plus'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	if got := strings.TrimSpace(f.readSrc(t, "P/seen.txt")); got != "ambient plus" {
		t.Errorf("script saw %q, want the [env] value chained on the inherited one", got)
	}
}

func TestBuild_EnvVars_OverridesBufaControlledPath(t *testing.T) {
	sep := string(os.PathListSeparator)
	win := "@echo off\r\necho %PATH%>seen.txt\r\n"
	unix := "printf '%s' \"$PATH\" > seen.txt\n"
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		"[env]\nPATH = '${PATH}"+sep+"xtool'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	got := strings.TrimSpace(f.readSrc(t, "P/seen.txt"))
	if !strings.HasSuffix(got, sep+"xtool") || len(got) <= len(sep+"xtool") {
		t.Errorf("script saw PATH = %q, want the inherited PATH extended with xtool", got)
	}
}

func TestBuild_EnvVars_UnsafeOverridesInherited(t *testing.T) {
	t.Setenv("ENVOVERRIDE_TEST", "ambient")
	win := "@echo off\r\necho %ENVOVERRIDE_TEST%>seen.txt\r\n"
	unix := "printf '%s' \"$ENVOVERRIDE_TEST\" > seen.txt\n"
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"),
		"unsafe = true\n[env]\nENVOVERRIDE_TEST = 'override'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	if got := f.readSrc(t, "P/seen.txt"); got != "override" {
		t.Errorf("unsafe script saw %q, want the [env] value to override the inherited one", got)
	}
}

const (
	winRootEnvScript  = "@echo off\r\necho %ROOT_VER%>r.txt\r\necho %SHARED%>s.txt\r\n"
	unixRootEnvScript = "printf '%s' \"$ROOT_VER\" > r.txt\nprintf '%s' \"$SHARED\" > s.txt\n"
)

const rootEnvToml = "[env]\nROOT_VER = ' r1 '\nSHARED = 'root'\n"

func TestBuild_RootEnvVars_ScriptEnv(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, rootEnvToml)
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nSHARED = 'dir'\n"+bufaToml(winRootEnvScript, unixRootEnvScript))
	f.write(t, filepath.Join("Q", "BUFA"), bufaToml(winRootEnvScript, unixRootEnvScript))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	hP := b.Build("P")
	hQ := b.Build("Q")

	for name, want := range map[string]string{"r.txt": "r1", "s.txt": "dir"} {
		if got := strings.TrimSpace(f.readOut(t, hP, name)); got != want {
			t.Errorf("P script saw %s = %q, want %q (dir [env] overrides root)", name, got, want)
		}
	}
	for name, want := range map[string]string{"r.txt": "r1", "s.txt": "root"} {
		if got := strings.TrimSpace(f.readOut(t, hQ, name)); got != want {
			t.Errorf("Q script saw %s = %q, want %q (root [env] alone)", name, got, want)
		}
	}
}

func TestDirtyBuild_RootEnvVars_ScriptEnv(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, rootEnvToml)
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nSHARED = 'dir'\n"+bufaToml(winRootEnvScript, unixRootEnvScript))

	f.dirtyBuilder().Build("P")

	if got := f.readSrc(t, "P/r.txt"); got != "r1" {
		t.Errorf("script saw ROOT_VER = %q, want r1", got)
	}
	if got := f.readSrc(t, "P/s.txt"); got != "dir" {
		t.Errorf("script saw SHARED = %q, want dir (dir [env] overrides root)", got)
	}
}

func TestBuild_RootEnvVars_CaseFoldCollision(t *testing.T) {
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nVER = '1'\n")
	f.write(t, filepath.Join("P", "BUFA"), "[env]\nver = '2'\n"+bufaToml(winEcho, unixEcho))

	err := c.Rescue(func() { f.dirtyBuilder().Build("P") })
	if err == nil || !strings.Contains(err.Error(), "differs only in case") {
		t.Errorf("want case-fold collision error, got %v", err)
	}
}

func TestBuild_RootEnvVars_OverridesInherited(t *testing.T) {
	t.Setenv("ROOTOVERRIDE_TEST", "ambient")
	win := "@echo off\r\necho %ROOTOVERRIDE_TEST%>seen.txt\r\n"
	unix := "printf '%s' \"$ROOTOVERRIDE_TEST\" > seen.txt\n"
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nROOTOVERRIDE_TEST = 'root'\n")
	f.write(t, filepath.Join("P", "BUFA"), bufaToml(win, unix))

	f.dirtyBuilder().Build("P")

	if got := strings.TrimSpace(f.readSrc(t, "P/seen.txt")); got != "root" {
		t.Errorf("script saw %q, want the root [env] value to override the inherited one", got)
	}
}

func TestBuild_RootEnvVars_RekeyEveryDir(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "src") // no [env] of its own
	skipIfNoSymlinks(t, f.builder().store)

	k1 := f.builder().Build("U")
	f.builder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("unchanged root [env] must hit the cache: %d runs, want 1", r)
	}

	f.write(t, Store.SrcRootFileName, "[env]\nX = '1'\n")
	k2 := f.builder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("root [env] change must re-key every dir: %d runs, want 2", r)
	}
	if k1 != k2 {
		t.Errorf("identical output under a new root [env] must dedup to the same D: %q vs %q", k1, k2)
	}

	f.write(t, Store.SrcRootFileName, "[env]\nX = '2'\n")
	f.builder().Build("U")
	if r := f.countRuns(t); r != 3 {
		t.Fatalf("root [env] value change must re-key: %d runs, want 3", r)
	}

	f.write(t, Store.SrcRootFileName, "")
	f.builder().Build("U")
	if r := f.countRuns(t); r != 3 {
		t.Fatalf("reverted root [env] must hit the earlier B<combined>: %d runs, want 3", r)
	}
}

func TestDirtyBuild_RootEnvVars_ChangeReruns(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "U", "src") // no [env] of its own

	f.dirtyBuilder().Build("U")
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 1 {
		t.Fatalf("unchanged root [env] must skip: %d runs, want 1", r)
	}

	f.write(t, Store.SrcRootFileName, "[env]\nX = '1'\n")
	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("root [env] change must invalidate the skip hash: %d runs, want 2", r)
	}

	f.dirtyBuilder().Build("U")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("repeat under the new root [env] must skip: %d runs, want 2", r)
	}
}
