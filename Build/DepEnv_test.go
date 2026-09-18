package Build

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

const (
	winDepEnvScript  = "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\n>v.txt echo %DEP_VER%\r\n"
	unixDepEnvScript = "echo x >> \"$BUFA_TEST_COUNTER\"\nprintf '%s' \"$DEP_VER\" > v.txt\n"
)

func TestBuild_DepEnvVars_ScriptEnv(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "P", "src")
	f.write(t, filepath.Join("P", Store.EnvFileName), "DEP_VER=1.2\n")
	f.write(t, filepath.Join("C", "BUFA"), "[deps]\nbld = ['/P']\n"+bufaToml(winDepEnvScript, unixDepEnvScript))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("C")

	if got := strings.TrimSpace(f.readOut(t, h, "v.txt")); got != "1.2" {
		t.Errorf("consumer script saw DEP_VER = %q, want 1.2", got)
	}
	f.requireStillCached(t, "C", 2)
}

func TestBuild_DepEnvVars_GeneratedFile(t *testing.T) {
	// The provider's script generates BUFA.env; the trailing space rides echo and must trim away.
	win := "@echo off\r\necho x>>\"%BUFA_TEST_COUNTER%\"\r\necho DEP_VER=7.7 >" + Store.EnvFileName + "\r\n"
	unix := "echo x >> \"$BUFA_TEST_COUNTER\"\necho 'DEP_VER=7.7 ' > " + Store.EnvFileName + "\n"
	f := newFixture(t)
	f.write(t, filepath.Join("P", "BUFA"), bufaToml(win, unix))
	f.write(t, filepath.Join("C", "BUFA"), "[deps]\nbld = ['/P']\n"+bufaToml(winDepEnvScript, unixDepEnvScript))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("C")

	if got := strings.TrimSpace(f.readOut(t, h, "v.txt")); got != "7.7" {
		t.Errorf("consumer script saw DEP_VER = %q, want 7.7 from the generated file", got)
	}
}

func TestBuild_DepEnvVars_EditRekeysConsumer(t *testing.T) {
	f := newFixture(t)
	f.unit(t, "P", "src")
	f.write(t, filepath.Join("P", Store.EnvFileName), "DEP_VER=1\n")
	f.write(t, filepath.Join("C", "BUFA"), "[deps]\nbld = ['/P']\n"+bufaToml(winDepEnvScript, unixDepEnvScript))
	skipIfNoSymlinks(t, f.builder().store)

	f.builder().Build("C")
	if r := f.countRuns(t); r != 2 {
		t.Fatalf("first build: %d runs, want 2 (provider + consumer)", r)
	}

	f.write(t, filepath.Join("P", Store.EnvFileName), "DEP_VER=2\n")
	h := f.builder().Build("C")
	if r := f.countRuns(t); r != 4 {
		t.Errorf("a BUFA.env edit must re-key the consumer through the dep hash: %d runs, want 4", r)
	}
	if got := strings.TrimSpace(f.readOut(t, h, "v.txt")); got != "2" {
		t.Errorf("consumer script saw DEP_VER = %q, want the edited 2", got)
	}
}

func TestBuild_DepEnvVars_ShellProviderExports(t *testing.T) {
	win := "@echo off\r\n>v.txt echo %SHELL_VER%\r\n"
	unix := "printf '%s' \"$SHELL_VER\" > v.txt\n"
	f := newFixture(t)
	f.wrapperProvider(t, "P")
	f.write(t, filepath.Join("P", Store.EnvFileName), "SHELL_VER=sv\n")
	f.write(t, filepath.Join("U", "BUFA"), "shell = \"/P\"\n"+bufaToml(win, unix))
	b := f.builder()
	skipIfNoSymlinks(t, b.store)

	h := b.Build("U")

	if got := strings.TrimSpace(f.readOut(t, h, "v.txt")); got != "sv" {
		t.Errorf("consumer script saw SHELL_VER = %q, want the implied shell provider's export", got)
	}
}

func TestDirtyBuild_DepEnvVars_DirectOnly(t *testing.T) {
	// Bracketed: a batch file expands an undefined %CVAR% to empty, so a bare echo would print "ECHO is off."
	winSee := "@echo off\r\n>seen.txt echo [%CVAR%]\r\n"
	unixSee := "printf '%s' \"[$CVAR]\" > seen.txt\n"
	f := newFixture(t)
	f.write(t, filepath.Join("C", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("C", Store.EnvFileName), "CVAR=from-c\n")
	f.write(t, filepath.Join("B", "BUFA"), "[deps]\nbld = ['/C']\n"+bufaToml(winSee, unixSee))
	f.write(t, filepath.Join("A", "BUFA"), "[deps]\nbld = ['/B']\n"+bufaToml(winSee, unixSee))

	f.dirtyBuilder().Build("A")

	if got := strings.TrimSpace(f.readSrc(t, "B/seen.txt")); got != "[from-c]" {
		t.Errorf("direct dependent saw CVAR = %q, want [from-c]", got)
	}
	// No transitivity: the grand-dependent's script sees no such var.
	if got := strings.TrimSpace(f.readSrc(t, "A/seen.txt")); got != "[]" {
		t.Errorf("indirect dependent saw CVAR = %q, want []", got)
	}
}

func TestDirtyBuild_DepEnvVars_LayersAndChaining(t *testing.T) {
	win := "@echo off\r\n>l.txt echo %LAYER%\r\n>o.txt echo %DEPONLY%\r\n>r.txt echo %FROMROOT%\r\n>b.txt echo %BB%\r\n"
	unix := "printf '%s' \"$LAYER\" > l.txt\nprintf '%s' \"$DEPONLY\" > o.txt\n" +
		"printf '%s' \"$FROMROOT\" > r.txt\nprintf '%s' \"$BB\" > b.txt\n"
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nR = 'rv'\nLAYER = 'root'\n")
	f.write(t, filepath.Join("D1", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("D1", Store.EnvFileName),
		"LAYER=${LAYER}+d1\nDEPONLY=d1\nFROMROOT=${R}!\nBB=${BUFA_BUILD_DIR}\n")
	f.write(t, filepath.Join("D2", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("D2", Store.EnvFileName), "LAYER=${LAYER}+d2\n")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = ['/D1', '/D2']\n[env]\nLAYER = '${LAYER}+dir'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("U")

	// Layer order root → deps in deps.bld order → dir, each self-reference chaining on the layer below.
	if got := strings.TrimSpace(f.readSrc(t, "U/l.txt")); got != "root+d1+d2+dir" {
		t.Errorf("script saw LAYER = %q, want root+d1+d2+dir", got)
	}
	if got := strings.TrimSpace(f.readSrc(t, "U/o.txt")); got != "d1" {
		t.Errorf("script saw DEPONLY = %q, want d1", got)
	}
	if got := strings.TrimSpace(f.readSrc(t, "U/r.txt")); got != "rv!" {
		t.Errorf("script saw FROMROOT = %q, want the root var expanded in the dep value", got)
	}
	if got := strings.TrimSpace(f.readSrc(t, "U/b.txt")); got != "U" {
		t.Errorf("script saw BB = %q, want the bufa var expanded in the dep value", got)
	}
}

// Each dep file is a closed unit: its own forward refs fail, a ref to a name only a later layer
// defines fails as plain undefined — never as "(re)defined later".
func TestDirtyBuild_DepEnvVars_FileIsAClosedUnit(t *testing.T) {
	unsetEnv(t, "Y")
	f := newFixture(t)
	f.write(t, filepath.Join("D", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("D", Store.EnvFileName), "X=${Y}\nY=1\n")
	f.write(t, filepath.Join("U", "BUFA"), "[deps]\nbld = ['/D']\n"+bufaToml(winEcho, unixEcho))

	err := c.Rescue(func() { f.dirtyBuilder().Build("U") })

	requireErrorContains(t, err, "[env] variable 'X' of '"+filepath.Join("D", Store.EnvFileName)+"'\n")
	requireErrorContains(t, err, "references [env] variable 'Y' (re)defined later")

	f.write(t, filepath.Join("D", Store.EnvFileName), "X=${Y}\n")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = ['/D']\n[env]\nY = '1'\n"+bufaToml(winEcho, unixEcho))

	err = c.Rescue(func() { f.dirtyBuilder().Build("U") })

	requireErrorContains(t, err, "undefined variable 'Y'")
}

// A dep chains on the layers below like a self-reference: a dir redefinition never gates its refs.
func TestDirtyBuild_DepEnvVars_DirOverrideDoesNotGateDepRef(t *testing.T) {
	win := "@echo off\r\n>x.txt echo %X%\r\n>s.txt echo %S%\r\n"
	unix := "printf '%s' \"$X\" > x.txt\nprintf '%s' \"$S\" > s.txt\n"
	f := newFixture(t)
	f.write(t, Store.SrcRootFileName, "[env]\nS = 'root'\n")
	f.write(t, filepath.Join("D", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("D", Store.EnvFileName), "X=${S}!\n")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = ['/D']\n[env]\nS = 'dir'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("U")

	// X baked the root S at the dep's layer; the script's S is the dir override.
	if got := strings.TrimSpace(f.readSrc(t, "U/x.txt")); got != "root!" {
		t.Errorf("script saw X = %q, want root!", got)
	}
	if got := strings.TrimSpace(f.readSrc(t, "U/s.txt")); got != "dir" {
		t.Errorf("script saw S = %q, want dir", got)
	}
}

// Blind siblings: a later dep plainly overwriting an earlier dep's export is an error; chaining is legal.
func TestDirtyBuild_DepEnvVars_DepOverwritePolicy(t *testing.T) {
	win := "@echo off\r\n>t.txt echo %TDIRS%\r\n"
	unix := "printf '%s' \"$TDIRS\" > t.txt\n"
	f := newFixture(t)
	f.write(t, filepath.Join("D1", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("D1", Store.EnvFileName), "TDIRS=d1\n")
	f.write(t, filepath.Join("D2", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("D2", Store.EnvFileName), "TDIRS=d2;${TDIRS}\n")
	f.write(t, filepath.Join("U", "BUFA"), "[deps]\nbld = ['/D1', '/D2']\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("U")

	if got := strings.TrimSpace(f.readSrc(t, "U/t.txt")); got != "d2;d1" {
		t.Errorf("script saw TDIRS = %q, want the chained d2;d1", got)
	}

	f.write(t, filepath.Join("D2", Store.EnvFileName), "TDIRS=d2\n")
	err := c.Rescue(func() { f.dirtyBuilder().Build("U") })
	requireErrorContains(t, err, "[env] variable 'TDIRS' of '"+filepath.Join("D2", Store.EnvFileName)+
		"' overwrites 'TDIRS' of '"+filepath.Join("D1", Store.EnvFileName)+"'")

	// A case-variant plain overwrite still clobbers the folded Windows name — same error.
	f.write(t, filepath.Join("D2", Store.EnvFileName), "Tdirs=d2\n")
	requireErrorContains(t, c.Rescue(func() { f.dirtyBuilder().Build("U") }), "overwrites 'TDIRS'")

	// Redefining with the identical value clobbers nothing — allowed.
	f.write(t, filepath.Join("D2", Store.EnvFileName), "TDIRS=d1\n")
	if err := c.Rescue(func() { f.dirtyBuilder().Build("U") }); err != nil {
		t.Errorf("an identical-value redefinition must build: %v", err)
	}
	if got := strings.TrimSpace(f.readSrc(t, "U/t.txt")); got != "d1" {
		t.Errorf("script saw TDIRS = %q, want d1", got)
	}
}

// A dir namesake of a dep export is a legal consumer override, even a case-variant one (a dep layer
// is foreign like the inherited env, which the root↔dir case check never covered either).
func TestDirtyBuild_DepEnvVars_DirOverridesDep(t *testing.T) {
	win := "@echo off\r\n>v.txt echo %VER%\r\n"
	unix := "printf '%s' \"$VER\" > v.txt\n"
	f := newFixture(t)
	f.write(t, filepath.Join("D", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("D", Store.EnvFileName), "VER=dep\n")
	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = ['/D']\n[env]\nVER = 'dir'\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("U")

	if got := strings.TrimSpace(f.readSrc(t, "U/v.txt")); got != "dir" {
		t.Errorf("script saw VER = %q, want the dir override", got)
	}

	f.write(t, filepath.Join("U", "BUFA"),
		"[deps]\nbld = ['/D']\n[env]\nver = 'dir2'\n"+bufaToml(win, unix))

	if err := c.Rescue(func() { f.dirtyBuilder().Build("U") }); err != nil {
		t.Errorf("case-variant dir override of a dep export must build: %v", err)
	}
}

func TestDirtyBuild_DepEnvVars_SrcDepsIgnored(t *testing.T) {
	win := "@echo off\r\n>seen.txt echo [%SVAR%]\r\n"
	unix := "printf '%s' \"[$SVAR]\" > seen.txt\n"
	f := newFixture(t)
	f.write(t, filepath.Join("S", "BUFA"), "")
	f.write(t, filepath.Join("S", Store.EnvFileName), "SVAR=sv\n")
	f.write(t, filepath.Join("U", "BUFA"), "[deps]\nsrc = ['/S']\n"+bufaToml(win, unix))

	f.dirtyBuilder().Build("U")

	if got := strings.TrimSpace(f.readSrc(t, "U/seen.txt")); got != "[]" {
		t.Errorf("script saw SVAR = %q, want [] — a src dep's BUFA.env must not export", got)
	}
}

func TestDirtyBuild_DepEnvVars_MalformedFails(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join("D", "BUFA"), bufaToml(winEcho, unixEcho))
	f.write(t, filepath.Join("D", Store.EnvFileName), "NOEQ\n")
	f.write(t, filepath.Join("U", "BUFA"), "[deps]\nbld = ['/D']\n"+bufaToml(winEcho, unixEcho))

	err := c.Rescue(func() { f.dirtyBuilder().Build("U") })

	requireErrorContains(t, err, "Env file '"+filepath.Join("D", Store.EnvFileName)+"'\n")
	requireErrorContains(t, err, "has no '='")
}
