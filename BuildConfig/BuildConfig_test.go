package BuildConfig

import (
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/dimagog/bufa"
	c "github.com/dimagog/bufa/internal/contract"
)

func decode(t *testing.T, doc string) BufaConfig {
	t.Helper()
	var cfg BufaConfig
	DecodeConfigOrScript([]byte(doc), "BUFA", &cfg)
	return cfg
}

func bothSections(sec func(platform string) string) string {
	return sec("windows") + sec("unix")
}

func allSections(sec func(platform string) string) string {
	return bothSections(sec) + sec("linux") + sec("macos")
}

func platTable(suffix, body string) func(string) string {
	return func(platform string) string { return "[" + platform + suffix + "]\n" + body }
}

func TestApplyPlatformSettings_BoolOverride(t *testing.T) {
	cases := []struct {
		name, doc           string
		unsafe, largeOutput bool
	}{
		{name: "explicit false beats root true",
			doc:    "unsafe = true\nlargeOutput = true\n" + bothSections(platTable("", "unsafe = false\nlargeOutput = false\n")),
			unsafe: false, largeOutput: false},
		{name: "explicit true beats root false",
			doc:    bothSections(platTable("", "unsafe = true\nlargeOutput = true\n")),
			unsafe: true, largeOutput: true},
		{name: "absent key keeps root",
			doc:    "unsafe = true\nlargeOutput = true\n" + bothSections(platTable("", "cmd = 'x'\n")),
			unsafe: true, largeOutput: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := decode(t, tc.doc)
			if cfg.Unsafe != tc.unsafe || cfg.LargeOutput != tc.largeOutput {
				t.Errorf("unsafe=%v largeOutput=%v, want %v/%v", cfg.Unsafe, cfg.LargeOutput, tc.unsafe, tc.largeOutput)
			}
		})
	}
}

func TestApplyPlatformSettings_CacheDirOverride(t *testing.T) {
	cases := []struct {
		name, doc string
		cacheDir  bool
	}{
		{name: "explicit false beats root true",
			doc: "deps.cacheDir = true\n" + bothSections(platTable(".deps", "cacheDir = false\n")), cacheDir: false},
		{name: "explicit true beats root false",
			doc: bothSections(platTable(".deps", "cacheDir = true\n")), cacheDir: true},
		{name: "absent key keeps root",
			doc: "deps.cacheDir = true\n" + bothSections(platTable(".deps", "src = ['x']\n")), cacheDir: true},
		{name: "off by default", doc: "cmd = 'x'\n", cacheDir: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if cfg := decode(t, tc.doc); cfg.Deps.CacheDir != tc.cacheDir {
				t.Errorf("Deps.CacheDir = %v, want %v", cfg.Deps.CacheDir, tc.cacheDir)
			}
		})
	}
}

func TestApplyPlatformSettings_CmdOverride(t *testing.T) {
	cfg := decode(t, "cmd = 'root'\n"+bothSections(platTable("", "cmd = 'plat'\n")))
	if cfg.Cmd.Script != "plat" {
		t.Errorf("Cmd = %q, want the platform override", cfg.Cmd.Script)
	}
	if cfg := decode(t, "cmd = 'root'\n"); cfg.Cmd.Script != "root" {
		t.Errorf("root-only Cmd = %q, want root", cfg.Cmd.Script)
	}
}

func TestPlatformSections(t *testing.T) {
	want := map[string][]string{
		"windows": {"windows"},
		"linux":   {"unix", "linux"},
		"darwin":  {"unix", "macos"},
	}[runtime.GOOS]
	if want == nil {
		want = []string{"unix"}
	}
	if !slices.Equal(PlatformSections, want) {
		t.Errorf("PlatformSections = %v, want %v", PlatformSections, want)
	}
	if wantNames := "[" + strings.Join(want, "]/[") + "]"; PlatformSectionNames != wantNames {
		t.Errorf("PlatformSectionNames = %q, want %q", PlatformSectionNames, wantNames)
	}
}

// Platform-independent: runs the unix→linux chain on any host.
func foldAs(t *testing.T, doc string, sections ...string) BufaConfig {
	t.Helper()
	var cfg BufaConfig
	meta := c.Check2(toml.Decode(doc, &cfg))
	cfg.Env.orderByDocument(meta.Keys(), "env")
	cfg.applyPlatformSettings(meta.Keys(), sections)
	return cfg
}

func TestApplyPlatformSettings_SubSectionFoldsAfterUnix(t *testing.T) {
	cases := []struct{ name, doc, cmd string }{
		{name: "leaf beats unix", doc: "cmd = 'root'\n[unix]\ncmd = 'unix'\n[linux]\ncmd = 'linux'\n", cmd: "linux"},
		{name: "unix without leaf", doc: "cmd = 'root'\n[unix]\ncmd = 'unix'\n[linux]\nunsafe = true\n", cmd: "unix"},
		{name: "leaf without unix", doc: "cmd = 'root'\n[linux]\ncmd = 'linux'\n", cmd: "linux"},
		{name: "other leaf ignored", doc: "cmd = 'root'\n[unix]\ncmd = 'unix'\n[macos]\ncmd = 'macos'\n", cmd: "unix"},
		{name: "document order irrelevant",
			doc: "cmd = 'root'\n[linux]\ncmd = 'linux'\n[unix]\ncmd = 'unix'\n", cmd: "linux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if cfg := foldAs(t, tc.doc, "unix", "linux"); cfg.Cmd.Script != tc.cmd {
				t.Errorf("Cmd = %q, want %q", cfg.Cmd.Script, tc.cmd)
			}
		})
	}
}

func TestApplyPlatformSettings_SubSectionListsAndEnv(t *testing.T) {
	cfg := foldAs(t, "[[deps.ext]]\nurl = 'http://x.invalid/r'\nhash = 'Fr'\n"+
		"[deps]\nsrc = ['r']\n[filters]\nsrc = ['-r']\n[env]\nA = 'r'\nB = 'r'\n"+
		"[[unix.deps.ext]]\nurl = 'http://x.invalid/u'\nhash = 'Fu'\n"+
		"[unix.deps]\nsrc = ['u']\n[unix.filters]\nsrc = ['-u']\n[unix.env]\nB = 'u'\nC = 'u'\n"+
		"[[linux.deps.ext]]\nurl = 'http://x.invalid/l'\nhash = 'Fl'\n"+
		"[linux.deps]\nsrc = ['l']\n[linux.filters]\nsrc = ['-l']\n[linux.env]\nD = 'l'\nC = 'l'\n",
		"unix", "linux")

	if len(cfg.Deps.Ext) != 3 ||
		cfg.Deps.Ext[0].Hash != "Fr" || cfg.Deps.Ext[1].Hash != "Fu" || cfg.Deps.Ext[2].Hash != "Fl" {
		t.Errorf("Deps.Ext = %v, want root, unix, linux", cfg.Deps.Ext)
	}
	if !slices.Equal(cfg.Deps.Src, []string{"r", "u", "l"}) || !slices.Equal(cfg.Filters.Src, []string{"-r", "-u", "-l"}) {
		t.Errorf("Deps.Src/Filters.Src = %v/%v, want root, unix, linux", cfg.Deps.Src, cfg.Filters.Src)
	}
	want := envPairs(envLiterals("A", "r", "B", "u", "C", "l", "D", "l"))
	if got := envPairs(cfg.Env); !slices.Equal(got, want) {
		t.Errorf("Env = %v, want %v (unix override, then linux's; C in unix's position)", got, want)
	}
}

func TestApplyPlatformSettings_MostSpecificSectionWins(t *testing.T) {
	// Every leaf spelled: the running platform's must win over [unix] (or [unix] itself on another unix).
	leaves := "[windows]\ncmd = 'leaf'\n[linux]\ncmd = 'leaf'\n[macos]\ncmd = 'leaf'\n"
	want := "leaf"
	if slices.Equal(PlatformSections, []string{"unix"}) {
		want = "unix"
	}
	if cfg := decode(t, "cmd = 'root'\n[unix]\ncmd = 'unix'\n"+leaves); cfg.Cmd.Script != want {
		t.Errorf("Cmd = %q, want %q for sections %v", cfg.Cmd.Script, want, PlatformSections)
	}
}

func TestCmdDisabled(t *testing.T) {
	if cfg := decode(t, "cmd = false\n"); !cfg.Cmd.Disabled || !cfg.IsScriptOptional() {
		t.Errorf("cmd = false must be script-optional: %+v", cfg.Cmd)
	}

	cfg := decode(t, "cmd = 'root'\n[[deps.ext]]\nurl = 'http://x.invalid/a.jar'\nhash = 'Faa'\n"+
		bothSections(platTable("", "cmd = false\n")))
	if !cfg.Cmd.Disabled || cfg.Cmd.Script != "" || !cfg.IsScriptOptional() {
		t.Errorf("platform cmd = false must override the root script: %+v", cfg.Cmd)
	}

	cfg = decode(t, "cmd = false\n"+bothSections(platTable("", "cmd = 'plat'\n")))
	if cfg.Cmd.Disabled || cfg.Cmd.Script != "plat" || cfg.IsScriptOptional() {
		t.Errorf("platform script must override root cmd = false: %+v", cfg.Cmd)
	}

	// Ext deps alone no longer make the script optional — cmd = false is the only opt-in.
	cfg = decode(t, "[[deps.ext]]\nurl = 'http://x.invalid/a.jar'\nhash = 'Faa'\n")
	if cfg.IsScriptOptional() {
		t.Error("ext-only config without cmd = false must not be script-optional")
	}

	for _, tc := range []struct{ name, doc, want string }{
		{name: "cmd = true", doc: "cmd = true\n", want: "cmd = false"},
		{name: "cmd = 5", doc: "cmd = 5\n", want: "script string or false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg BufaConfig
			err := c.Rescue(func() { DecodeConfigOrScript([]byte(tc.doc), "BUFA", &cfg) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestApplyPlatformSettings_EnvMergePlatformWins(t *testing.T) {
	cfg := decode(t, "[env]\nA = '1'\nB = '2'\n"+
		bothSections(platTable(".env", "B = '9'\nC = '3'\n")))
	want := []envPair[EnvVar]{{"A", EnvVar{Value: "1"}}, {"B", EnvVar{Value: "9"}}, {"C", EnvVar{Value: "3"}}}
	if got := envPairs(cfg.Env); !slices.Equal(got, want) {
		t.Errorf("Env = %v, want %v", got, want)
	}
}

type envPair[V EnvValue] struct {
	name  string
	value V
}

// Document order flattened, so a table compares and prints as a slice.
func envPairs[V EnvValue](table EnvTable[V]) []envPair[V] {
	pairs := make([]envPair[V], 0, table.Len())
	for name, value := range table.All() {
		pairs = append(pairs, envPair[V]{name, value})
	}
	return pairs
}

func envLiterals(nameValuePairs ...string) EnvTable[EnvVar] {
	var table EnvTable[EnvVar]
	for i := 0; i < len(nameValuePairs); i += 2 {
		table.Set(nameValuePairs[i], EnvVar{Value: nameValuePairs[i+1]})
	}
	return table
}

func TestEnvTable_DocumentOrderWithPlatformFold(t *testing.T) {
	// Platform sections first in the document: their new keys still follow the top-level ones.
	cfg := decode(t, bothSections(platTable(".env", "C = '3'\nB = '9'\nD = { file = 'd' }\n"))+
		"[Env]\nB = '2'\nA = { file = 'a' }\n")
	want := []envPair[EnvVar]{
		{"B", EnvVar{Value: "9"}},
		{"A", EnvVar{File: "a"}},
		{"C", EnvVar{Value: "3"}},
		{"D", EnvVar{File: "d"}},
	}
	if got := envPairs(cfg.Env); !slices.Equal(got, want) {
		t.Errorf("Env = %v, want %v", got, want)
	}
	if got := decode(t, "cmd = 'x'\n").Env; got.Len() != 0 {
		t.Errorf("Env = %v without [env], want zero", got)
	}
	if got := decode(t, "[env]\n").Env; got.Len() != 0 {
		t.Errorf("Env = %v for an empty [env], want empty", got)
	}
}

func TestEnvTable_SetAndGet(t *testing.T) {
	table := envLiterals("A", "1", "B", "2")
	table.Set("A", EnvVar{Value: "9"}) // position kept
	table.Set("C", EnvVar{Value: "3"}) // appended
	want := envPairs(envLiterals("A", "9", "B", "2", "C", "3"))
	if got := envPairs(table); !slices.Equal(got, want) {
		t.Errorf("table = %v, want %v", got, want)
	}
	if v, ok := table.Get("B"); !ok || v.Value != "2" {
		t.Errorf("Get(B) = %+v, %v", v, ok)
	}
	if v, ok := table.Get("b"); ok || v.Value != "" {
		t.Errorf("Get(b) = %+v, %v — want an exact-case miss", v, ok)
	}
	var zero EnvTable[EnvVar]
	if _, ok := zero.Get("A"); ok {
		t.Error("Get on a zero table hit")
	}
}

func TestEnvTable_RootDocumentOrder(t *testing.T) {
	cfg, err := decodeRoot("[env]\nZ = '1'\nA = '2'\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []envPair[string]{{"Z", "1"}, {"A", "2"}}
	if got := envPairs(cfg.Env); !slices.Equal(got, want) {
		t.Errorf("Env = %v, want %v", got, want)
	}
}

func TestEnvTable_DuplicateCaseVariantTables(t *testing.T) {
	// The decoder EqualFold-matches [env] and [ENV] to one field; the second decode used to silently
	// replace the first table (or panic "Reorder: N keys for a map of M" when it held 2+ entries).
	cases := []struct{ name, doc string }{
		{"second table replaces first", "[env]\nA = '1'\n[ENV]\nB = '2'\n"},
		{"former Reorder panic", "[env]\nA = '1'\n[ENV]\nB = '2'\nC = '3'\n"},
		{"platform section", "[" + PlatformSections[0] + ".env]\nA = '1'\n" +
			"[" + strings.ToUpper(PlatformSections[0]) + ".ENV]\nB = '2'\n"},
		{"most specific section", "[" + PlatformSections[len(PlatformSections)-1] + ".env]\nA = '1'\n" +
			"[" + strings.ToUpper(PlatformSections[len(PlatformSections)-1]) + ".ENV]\nB = '2'\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg BufaConfig
			err := c.Rescue(func() { DecodeConfigOrScript([]byte(tc.doc), "BUFA", &cfg) })
			if err == nil || !strings.Contains(err.Error(), "declared more than once") {
				t.Errorf("want duplicate-table error, got %v", err)
			}
		})
	}

	if _, err := decodeRoot("[env]\nA = '1'\n[ENV]\nB = '2'\n"); err == nil ||
		!strings.Contains(err.Error(), "declared more than once") {
		t.Errorf("root marker: want duplicate-table error, got %v", err)
	}
}

func platLists(platform string) string {
	return "[" + platform + ".deps]\nsrc = ['c']\nbld = ['d']\nexport = ['f']\n" +
		"[[" + platform + ".deps.ext]]\nurl = 'http://x.invalid/p.jar'\nhash = 'Fbb'\n" +
		"[" + platform + ".filters]\nsrc = ['+u']\nbld = ['+v']\ndirty = ['+w']\n"
}

func TestApplyPlatformSettings_ListsConcatRootFirst(t *testing.T) {
	cfg := decode(t, "[deps]\nsrc = ['a']\nbld = ['b']\nexport = ['e']\n"+
		"[[deps.ext]]\nurl = 'http://x.invalid/r.jar'\nhash = 'Faa'\n"+
		"[filters]\nsrc = ['-x']\nbld = ['-y']\ndirty = ['-z']\n"+
		bothSections(platLists))

	if !slices.Equal(cfg.Deps.Src, []string{"a", "c"}) || !slices.Equal(cfg.Deps.Bld, []string{"b", "d"}) {
		t.Errorf("Deps src/bld = %v/%v, want root first then platform", cfg.Deps.Src, cfg.Deps.Bld)
	}
	if !slices.Equal(cfg.Deps.Export, []string{"e", "f"}) {
		t.Errorf("Deps.Export = %v, want root first then platform", cfg.Deps.Export)
	}
	if len(cfg.Deps.Ext) != 2 || cfg.Deps.Ext[0].Hash != "Faa" || cfg.Deps.Ext[1].Hash != "Fbb" {
		t.Errorf("Deps.Ext = %v, want root entry then platform entry", cfg.Deps.Ext)
	}
	if !slices.Equal(cfg.Filters.Src, []string{"-x", "+u"}) ||
		!slices.Equal(cfg.Filters.Bld, []string{"-y", "+v"}) ||
		!slices.Equal(cfg.Filters.Dirty, []string{"-z", "+w"}) {
		t.Errorf("Filters = %+v, want root rules first then platform", cfg.Filters)
	}
}

func TestApplyPlatformSettings_SectionsCleared(t *testing.T) {
	cfg := decode(t, "cmd = 'root'\n"+allSections(platTable("", "cmd = 'plat'\nunsafe = true\n"))+
		allSections(platTable(".env", "X = '1'\n")))
	sections := map[string]BaseConfig{"Windows": cfg.Windows, "Unix": cfg.Unix, "Linux": cfg.Linux, "MacOS": cfg.MacOS}
	for name, section := range sections {
		if !reflect.DeepEqual(section, BaseConfig{}) {
			t.Errorf("section %s not zeroed post-fold: %+v", name, section)
		}
	}
}

func TestDecodeStrict_CaseInsensitiveSection(t *testing.T) {
	// [Windows] decodes case-insensitively; without the fold it would read as absent.
	cfg := decode(t, "Cmd = 'root'\n[Windows]\nUnsafe = true\nCmd = 'plat'\n[Unix]\nUnsafe = true\nCmd = 'plat'\n")
	if !cfg.Unsafe || cfg.Cmd.Script != "plat" {
		t.Errorf("unsafe=%v cmd=%q, want capital-spelled section to decode AND override", cfg.Unsafe, cfg.Cmd.Script)
	}
}

func TestDecodeStrict_UnknownKeys(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{name: "top level", doc: "unknwn = 1\n", want: "unknwn"},
		{name: "under deps", doc: "[deps]\nfoo = ['x']\n", want: "deps.foo"},
		{name: "under section", doc: "[windows]\nbogus = 1\n", want: "windows.bogus"},
		{name: "under section filters", doc: "[windows.filters]\nsrcc = ['x']\n", want: "windows.filters.srcc"},
		{name: "under leaf section", doc: "[macos]\nbogus = 1\n", want: "macos.bogus"},
		{name: "nested leaf", doc: "[unix.linux]\ncmd = 'x'\n", want: "unix.linux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg BufaConfig
			err := c.Rescue(func() { DecodeStrict([]byte(tc.doc), "BUFA", &cfg) })
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "BUFA") {
				t.Errorf("want error naming %q and the file, got %v", tc.want, err)
			}
		})
	}
}

func TestDecodeStrict_ValidDocClean(t *testing.T) {
	doc := "unsafe = false\nlargeOutput = false\ncmd = 'x'\n" +
		"[env]\nX = { file = 'f.ver' }\nY = 'lit'\n" +
		"[deps]\nsrc = ['a']\n[filters]\nsrc = ['-x']\n" +
		bothSections(platLists)
	var cfg BufaConfig
	if err := c.Rescue(func() { DecodeStrict([]byte(doc), "BUFA", &cfg) }); err != nil {
		t.Errorf("valid doc rejected: %v", err)
	}
}

func TestDecodeConfigOrScript_RawScriptDetection(t *testing.T) {
	assertBrokenConfig := func(t *testing.T, doc string, line int) {
		t.Helper()
		var cfg BufaConfig
		err := c.Rescue(func() { DecodeConfigOrScript([]byte(doc), "some/BUFA", &cfg) })
		want := fmt.Sprintf("Cannot parse file 'some/BUFA' as toml: line %d", line)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "NAME=") {
			t.Errorf("want error containing %q + the NAME= hint, got %v", want, err)
		}
	}

	// Broken config: a key consumed (LastKey) or a whole-line prefix that yields a key — never a script.
	for _, tc := range []struct {
		name, doc string
		line      int
	}{
		{"bare value on line 1", "cmd = tar -xf\n", 1},
		{"bool typo", "largeOutput = tru\n", 1},
		{"unterminated inline table", "cmd = { file = \"x\"\n", 1},
		{"unterminated multi-line string", "cmd = '''\necho hi\n", 2},
		{"bad table header after a statement", "largeOutput = true\n\n[env\nNU_VER = \"0.114.1\"\n", 4},
		{"no = after a statement", "cmd = 'x'\nlargeOutput true\n", 2},
		{"table then line without =", "[env]\nfoo\n", 2},
	} {
		t.Run("broken/"+tc.name, func(t *testing.T) { assertBrokenConfig(t, tc.doc, tc.line) })
	}

	for _, tc := range []struct{ name, doc string }{
		{"nu print", "print \"Hello\"\n"},
		{"nu let", "let x = 1\n"},
		{"pwsh assign", "$x = 1\n"},
		{"cmd echo off CRLF", "@echo off\r\necho hi\r\n"},
		{"bash set -e", "set -e\n"},
		{"bash export", "export X=1\n"},
		{"cmd setlocal", "setlocal\n"},
		{"cmd cls", "cls\n"},
		{"cmd cls no newline", "cls"},
		{"bash test", "[ -f x ]\n"},
		{"bash double test", "[[ -f x ]]\n"},
		{"shebang comment blank then command", "#!/bin/sh\n# comment\n\nset -e\necho hi\n"},
		// The first statement breaks before its '=': no TOML evidence — the accepted residual hole.
		{"first statement without =", "largeOutput true\n"},
		{"unterminated header as first line", "[env\nX = 1\n"},
	} {
		t.Run("script/"+tc.name, func(t *testing.T) {
			cfg := decode(t, tc.doc)
			want := BufaConfig{BaseConfig: BaseConfig{Unsafe: true, Cmd: Cmd{Script: tc.doc}}}
			if !reflect.DeepEqual(cfg, want) {
				t.Errorf("raw script must be Script + Unsafe only:\n got %+v\nwant %+v", cfg, want)
			}
		})
	}

	// Unsupported raw scripts: a leading NAME=… line reads as TOML — a valid value satisfies the prefix rule, an
	// invalid one sets LastKey.
	for _, tc := range []struct {
		name, doc string
		line      int
	}{
		{"valid value", "X=1\necho $X\n", 2},
		{"invalid value", "X=abc\necho\n", 1},
		{"value then command", "FOO=bar cmd\n", 1},
		{"after a comment", "# c\nX=1\necho\n", 3},
		{"CRLF", "X=1\r\necho\r\n", 2},
	} {
		t.Run("unsupported/"+tc.name, func(t *testing.T) { assertBrokenConfig(t, tc.doc, tc.line) })
	}
}

func TestDecodeConfigOrScript_ParseableStaysStrict(t *testing.T) {
	var cfg BufaConfig
	err := c.Rescue(func() { DecodeConfigOrScript([]byte("x = 1\n"), "BUFA", &cfg) })
	if err == nil || !strings.Contains(err.Error(), "x") {
		t.Errorf("want unknown-key error, got %v", err)
	}

	if cfg := decode(t, "cmd = 'c'\n"); cfg.Cmd.Script != "c" {
		t.Errorf("valid TOML must decode as config: Cmd = %q", cfg.Cmd.Script)
	}
}

func TestTomlKeysSet_Prefixes(t *testing.T) {
	// meta.Keys records only full paths — the presence check must cover prefixes.
	var cfg BufaConfig
	meta := c.Check2(toml.Decode("[windows.deps]\nsrc = ['a']\n", &cfg))
	set := newTomlKeysSet(meta.Keys())
	if !set.containsTomlKey("windows") || !set.containsTomlKey("windows", "deps") ||
		!set.containsTomlKey("windows", "deps", "src") {
		t.Error("prefixes of a defined key must read as defined")
	}
	if set.containsTomlKey("windows", "cmd") || set.containsTomlKey("unix") {
		t.Error("undefined keys must not read as defined")
	}
}

func decodeRoot(doc string) (RootConfig, error) {
	cfg := DefaultRootConfig()
	err := c.Rescue(func() {
		DecodeStrict([]byte(doc), ".BUFA", &cfg)
		cfg.Validate()
	})
	return cfg, err
}

func TestRootConfig_Validate_Env(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{name: "file-sourced table", doc: "[env]\nX = { file = 'v.ver' }\n", want: "'X': must be a literal string"},
		{name: "non-string literal", doc: "[env]\nX = 42\n", want: "'X': must be a literal string, got int64"},
		{name: "empty literal", doc: "[env]\nX = ' '\n", want: "is empty"},
		{name: "multi-line literal", doc: "[env]\nX = \"\"\"1\n2\"\"\"\n", want: "single line"},
		{name: "empty variable name", doc: "[env]\n'' = 'x'\n", want: "must not be empty"},
		{name: "case-folded duplicate", doc: "[env]\nVer = '1'\nVER = '2'\n", want: "duplicate"},
		{name: "reserved BUFA_ prefix", doc: "[env]\nbufa_thing = '1'\n", want: "reserved BUFA_ prefix"},
		{name: "negative ttl", doc: "[unsafe]\nttl = '-1s'\n", want: "must not be negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeRoot(tc.doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRootConfig_Validate_TrimsLiterals(t *testing.T) {
	cfg, err := decodeRoot("[env]\nA = ' 1.2 '\nB = \"\"\"x\n\"\"\"\n")
	if err != nil {
		t.Fatalf("valid root [env] rejected: %v", err)
	}
	want := []envPair[string]{{"A", "1.2"}, {"B", "x"}}
	if got := envPairs(cfg.Env); !slices.Equal(got, want) {
		t.Errorf("Env = %v, want trimmed literals %v", got, want)
	}
}

func TestRootConfig_Validate_NoEnv(t *testing.T) {
	cfg, err := decodeRoot("")
	if err != nil || cfg.Env.Len() != 0 {
		t.Errorf("empty marker: err=%v Env=%v, want nil/nil", err, cfg.Env)
	}
}

func TestApplyPlatformSettings_ShellOverride(t *testing.T) {
	if cfg := decode(t, "shell = 'root'\n"+bothSections(platTable("", "shell = 'plat'\n"))); cfg.Shell != "plat" {
		t.Errorf("Shell = %q, want the platform override", cfg.Shell)
	}
	if cfg := decode(t, "shell = 'root'\n"+bothSections(platTable("", "cmd = 'x'\n"))); cfg.Shell != "root" {
		t.Errorf("Shell = %q, want root kept when the section has no shell", cfg.Shell)
	}
	if cfg := decode(t, "shell = 'root'\n"+bothSections(platTable("", "shell = ''\n"))); cfg.Shell != "" {
		t.Errorf("Shell = %q, want an explicit empty override (presence, not truthiness)", cfg.Shell)
	}
}

func TestRootConfig_ShellAndCatalogue(t *testing.T) {
	cfg, err := decodeRoot("shell = 'nu'\n[shells]\nnu = '/build/nu'\npwsh = '/build/pwsh'\n")
	if err != nil {
		t.Fatalf("valid root shell config rejected: %v", err)
	}
	if cfg.Shell != "nu" || cfg.Shells["nu"] != "/build/nu" || cfg.Shells["pwsh"] != "/build/pwsh" {
		t.Errorf("Shell = %q, Shells = %v", cfg.Shell, cfg.Shells)
	}
	if _, err := decodeRoot("shell = '/build/nu'\n"); err != nil {
		t.Fatalf("absolute root shell rejected: %v", err)
	}
	cases := []struct{ name, doc, want string }{
		{name: "alias is a path", doc: "[shells]\n'./x' = '/p'\n", want: "must be a bare name"},
		{name: "empty alias", doc: "[shells]\n'' = '/p'\n", want: "must be a bare name"},
		{name: "value is a name", doc: "[shells]\nnu = 'bash'\n", want: "must be absolute"},
		{name: "value is relative", doc: "[shells]\nnu = './build/nu'\n", want: "must be absolute"},
		{name: "shell is unknown alias", doc: "shell = 'nu'\n", want: "must be a [shells] alias or an absolute"},
		{name: "shell is relative", doc: "shell = './build/nu'\n", want: "must be a [shells] alias or an absolute"},
		{name: "platform section", doc: "[windows]\nshell = 'cmd'\n", want: "Unknown key"},
		{name: "leaf platform section", doc: "[macos]\nshell = 'bash'\n", want: "Unknown key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeRoot(tc.doc)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRootConfig_GetHash_ShellKeysCatalogueDoesNot(t *testing.T) {
	base, _ := decodeRoot("")
	shell, _ := decodeRoot("shell = '/build/shell'\n")
	catalogue, _ := decodeRoot("[shells]\nnu = '/build/nu'\n")
	if base.GetHash() == shell.GetHash() {
		t.Error("the marker's shell value must change the root hash")
	}
	if base.GetHash() != catalogue.GetHash() {
		t.Error("the catalogue must not change the root hash: aliases key through the dep manifest")
	}
}

func TestRootConfig_GetHash_MinorVersionKeysPatchDoesNot(t *testing.T) {
	defer func(v string) { Bufa.Version = v }(Bufa.Version)
	cfg, _ := decodeRoot("")
	hashAt := func(version string) string {
		Bufa.Version = version
		return cfg.GetHash()
	}
	if hashAt("1.2.3") != hashAt("1.2.4") {
		t.Error("a patch bump must not change the root hash")
	}
	if hashAt("1.2.3") == hashAt("1.3.0") {
		t.Error("a minor bump must change the root hash")
	}
}

func TestIsShellPath(t *testing.T) {
	for value, want := range map[string]bool{
		"nu": false, "cmd": false, "my-shell": false,
		"/build/nu": true, "./tools/nu": true, ".": true, "build/nu": true, `tools\nu`: true,
	} {
		if got := IsShellPath(value); got != want {
			t.Errorf("IsShellPath(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestShellDef_DecodeAndValidate(t *testing.T) {
	valid := "exe = './nu'\next = '.nu'\nrun = ['-n', '${script}']\nshell = []\npostShell = ['-n', '-e', \"source '${script}'\"]\n" +
		"prompt = { env = 'PROMPT_COMMAND', default = '' }\nmove = 'mv'\ncopy = 'cp'\n"
	def := DecodeShellDef([]byte(valid), "BUFA.shell")
	if def.Exe != "./nu" || def.Ext != ".nu" || !slices.Equal(def.Run, []string{"-n", ScriptPlaceholder}) ||
		def.Shell == nil || len(def.Shell) != 0 || len(def.PostShell) != 3 || def.Prompt.Env != "PROMPT_COMMAND" ||
		def.Move != "mv" || def.Copy != "cp" || def.Name != "" {
		t.Errorf("decoded %+v", def)
	}
	def.Name = "runtime-only"
	var encoded strings.Builder
	if err := toml.NewEncoder(&encoded).Encode(def); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded.String(), "name =") {
		t.Errorf("encoded runtime-only Name:\n%s", encoded.String())
	}
	minimal := DecodeShellDef([]byte("exe = 'x'\next = '.x'\nrun = ['${script}']\n"), "BUFA.shell")
	if minimal.Shell != nil || minimal.PostShell != nil || minimal.Prompt.Env != "" || minimal.Move != "" {
		t.Errorf("absent optionals must stay nil/zero: %+v", minimal)
	}

	cases := []struct{ name, doc, want string }{
		{name: "no exe", doc: "ext = '.x'\nrun = ['${script}']\n", want: "exe is required"},
		{name: "no ext", doc: "exe = 'x'\nrun = ['${script}']\n", want: "ext is required"},
		{name: "ext without dot", doc: "exe = 'x'\next = 'x'\nrun = ['${script}']\n", want: "'.'-prefixed"},
		{name: "no run", doc: "exe = 'x'\next = '.x'\n", want: "run is required"},
		{name: "run without script", doc: "exe = 'x'\next = '.x'\nrun = ['-c']\n", want: "no argument references ${script}"},
		{name: "postShell without script", doc: "exe = 'x'\next = '.x'\nrun = ['${script}']\npostShell = ['-i']\n", want: "postShell must pass the script"},
		{name: "prompt default only", doc: "exe = 'x'\next = '.x'\nrun = ['${script}']\nprompt = { default = 'x' }\n", want: "without prompt.env"},
		{name: "move alone", doc: "exe = 'x'\next = '.x'\nrun = ['${script}']\nmove = 'mv'\n", want: "set together"},
		{name: "runtime name", doc: "name = 'x'\nexe = 'x'\next = '.x'\nrun = ['${script}']\n", want: "Unknown key"},
		{name: "unknown key", doc: "exe = 'x'\next = '.x'\nrun = ['${script}']\nargs = []\n", want: "Unknown key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Rescue(func() { DecodeShellDef([]byte(tc.doc), "BUFA.shell") })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDecodeEnvFile(t *testing.T) {
	doc := "\uFEFF# tool exports\r\n\r\nVER=1.2 \r\nPATH_ADD==C:\\x=y\r\n  ANCHOR=#5\r\nSPACED=  v  \r\n"
	got := envPairs(DecodeEnvFile([]byte(doc), "P/BUFA.env"))
	// The value is verbatim after the first '=' of the trimmed line: inner '=' and '#' kept, leading
	// spaces kept, trailing spaces gone with the line trim.
	want := []envPair[string]{{"VER", "1.2"}, {"PATH_ADD", "=C:\\x=y"}, {"ANCHOR", "#5"}, {"SPACED", "  v"}}
	if !slices.Equal(got, want) {
		t.Errorf("DecodeEnvFile = %v, want %v", got, want)
	}

	if got := DecodeEnvFile(nil, "P/BUFA.env").Len(); got != 0 {
		t.Errorf("empty file: %d vars, want 0", got)
	}
}

func TestDecodeEnvFile_Errors(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{name: "no equals", doc: "VER\n", want: "line 1 'VER' has no '='"},
		{name: "empty value", doc: "A=1\nVER=\n", want: "line 2: variable 'VER' has empty value"},
		{name: "spaces-only value", doc: "VER=   \n", want: "has empty value"},
		{name: "exact duplicate", doc: "VER=1\nVER=2\n", want: "line 2: duplicate variable 'VER'"},
		{name: "case-folded duplicate", doc: "VER=1\nver=2\n", want: "duplicate variable 'ver'"},
		{name: "empty name", doc: "=x\n", want: "must not be empty"},
		{name: "reserved BUFA_ prefix", doc: "BUFA_X=1\n", want: "reserved BUFA_ prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Rescue(func() { DecodeEnvFile([]byte(tc.doc), "P/BUFA.env") })
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Env file 'P/BUFA.env'") {
				t.Errorf("want error containing %q naming the file, got %v", tc.want, err)
			}
		})
	}
}
