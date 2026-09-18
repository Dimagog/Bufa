// Package BuildConfig defines the parsed BUFA shape, shared by Build and Watcher.
package BuildConfig

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/dimagog/bufa"
	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

type BaseConfig struct {
	Cmd         Cmd              `toml:"cmd"`
	Deps        Deps             `toml:"deps"`
	Env         EnvTable[EnvVar] `toml:"env"`
	Filters     Filters          `toml:"filters"`
	LargeOutput bool             `toml:"largeOutput"`
	Shell       string           `toml:"shell"`
	Unsafe      bool             `toml:"unsafe"`
}

type BufaConfig struct {
	Hash                   string `toml:"-"`
	VirtualDir             bool   `toml:"-"`
	VirtualDirMaterialized bool   `toml:"-"`
	// Untagged embed: the decoder flattens it, so root-level keys land here.
	BaseConfig
	Windows BaseConfig `toml:"windows"`
	Unix    BaseConfig `toml:"unix"`
	Linux   BaseConfig `toml:"linux"`
	MacOS   BaseConfig `toml:"macos"`
}

type RootConfig struct {
	Unsafe RootUnsafe        `toml:"unsafe"`
	Env    EnvTable[string]  `toml:"env"`    // literals only: a { file = … } table fails at decode
	Shell  string            `toml:"shell"`  // "", a [shells] alias, or a /-prefixed provider dir
	Shells map[string]string `toml:"shells"` // alias → /-prefixed source-root provider dir
	// a BUFA-named source directory is ordinary content, not a reserved-name violation (building bufa itself)
	AllowBufaDir bool `toml:"allowBufaDir"`
}

func DefaultRootConfig() RootConfig {
	return RootConfig{
		Unsafe: RootUnsafe{
			TTL: 15 * time.Minute,
		},
	}
}

// Also rewrites each [env] literal to its trimmed value, so cached configs carry final values.
func (cfg *RootConfig) Validate() {
	c.Require(cfg.Unsafe.TTL >= 0, "[unsafe].ttl must not be negative")
	CheckEnvVarNames(cfg.Env)
	for key, value := range cfg.Env.All() {
		cfg.Env.Set(key, NormalizeEnvVarValue(key, value))
	}
	for _, alias := range slices.Sorted(maps.Keys(cfg.Shells)) {
		c.Require(alias != "" && !IsShellPath(alias), "[shells] alias '%s' must be a bare name", alias)
		c.Require(strings.HasPrefix(cfg.Shells[alias], "/"),
			"[shells] alias '%s' provider dir path must be absolute (start with '/'), got '%s'", alias, cfg.Shells[alias])
	}
	if cfg.Shell != "" && !strings.HasPrefix(cfg.Shell, "/") {
		_, aliasExists := cfg.Shells[cfg.Shell]
		c.Require(aliasExists,
			"shell '%s' must be a [shells] alias or an absolute provider dir path (start with '/')", cfg.Shell)
	}
}

// Only the settings that must re-key every dir: ttl, [shells], allowBufaDir, and the patch number
// are left out on purpose.
func (cfg RootConfig) GetHash() string {
	envEntries := make([]Hashing.NamedEntry, 0, cfg.Env.Len())
	for key, value := range cfg.Env.All() {
		envEntries = append(envEntries, Hashing.NamedEntry{Name: key, Hash: value})
	}
	majorMinor, _, found := strings.CutLast(Bufa.Version, ".")
	c.Assert(found, "bufa version '%s' is not x.y.z", Bufa.Version)
	return Hashing.CombineHashesInPlace([]Hashing.NamedEntry{
		{Name: "bufaVersion", Hash: majorMinor},
		{Name: "env", Hash: Hashing.CombineHashesInPlace(envEntries)},
		{Name: "shell", Hash: cfg.Shell},
	})
}

// A shell value is a provider dir path (dep grammar) when it looks like one; a bare word is a name
// — a [shells] alias, else a preset.
func IsShellPath(value string) bool {
	return strings.HasPrefix(value, ".") || strings.ContainsAny(value, `/\`)
}

type RootUnsafe struct {
	TTL time.Duration `toml:"ttl"`
}

type Deps struct {
	Src      []string `toml:"src"`
	Bld      []string `toml:"bld"`
	Ext      []ExtDep `toml:"ext"`
	Export   []string `toml:"export"`
	CacheDir bool     `toml:"cacheDir"`
}

type ExtDep struct {
	Url    string `toml:"url"`
	Hash   string `toml:"hash"`
	Name   string `toml:"name"`
	Large  bool   `toml:"large"`
	Export bool   `toml:"export"` // TOML shortcut for listing Name in deps.export; Build folds it in and clears it
}

type Cmd struct {
	Script   string // cmd = "script body"
	Disabled bool   // cmd = false: a deliberately scriptless dir (stage+publish only)
}

func (cmd *Cmd) UnmarshalTOML(data any) error {
	switch d := data.(type) {
	case string:
		cmd.Script = d
		return nil
	case bool:
		if d {
			return errors.New(`cmd = true is invalid: use a script string, or cmd = false for a scriptless dir`)
		}
		cmd.Disabled = true
		return nil
	default:
		return fmt.Errorf(`cmd must be a script string or false, got %T`, data)
	}
}

type EnvVar struct {
	Value string // literal:      X = "4.13.2"
	File  string // file-sourced: X = { file = "antlr.ver" }
}

func (v *EnvVar) UnmarshalTOML(data any) error {
	switch d := data.(type) {
	case string:
		v.Value = d
		return nil
	case map[string]any:
		if len(d) != 1 {
			return fmt.Errorf(`[env] variable table must hold exactly one { file = "path" } key, got %d keys`, len(d))
		}
		for key, value := range d {
			if !strings.EqualFold(key, "file") {
				return fmt.Errorf(`[env] variable must be "literal" or { file = "path" }, got key '%s'`, key)
			}
			file, ok := value.(string)
			if !ok {
				return fmt.Errorf("[env] variable file must be a string, got %T", value)
			}
			if file == "" {
				return errors.New("[env] variable has empty file")
			}
			v.File = file
		}
		return nil
	default:
		return fmt.Errorf(`[env] variable must be "literal" or { file = "path" }, got %T`, data)
	}
}

func CheckEnvVarNames[V EnvValue](envVars EnvTable[V]) {
	if envVars.Len() == 0 {
		return
	}
	// seen set is needed because it's case-insensitive
	seen := Util.NewSet[string]()
	for key := range envVars.Keys() {
		c.Require(key != "", "[env] variable name must not be empty")
		folded := strings.ToLower(key)
		c.Require(!seen.Contains(folded), "duplicate [env] variable '%s'", key)
		seen.Add(folded)
		c.Require(!strings.HasPrefix(folded, "bufa_"), "[env] variable '%s' uses bufa's reserved BUFA_ prefix", key)
	}
}

func NormalizeEnvVarValue(key, value string) string {
	value = strings.TrimSpace(value)
	c.Require(value != "", "[env] variable '%s' is empty", key)
	c.Require(!strings.ContainsAny(value, "\r\n"), "[env] variable '%s' must hold a single line", key)
	return value
}

// Spec: Doc/Specs/DepEnvVars.md
func DecodeEnvFile(data []byte, path string) EnvTable[string] {
	defer c.Context("Env file '%s'", path)
	var envVars EnvTable[string]
	// Own folded dup check: Set collapses an exact dup before CheckEnvVarNames could see it, and checking
	// here gives line-numbered errors; CheckEnvVarNames below still owns the empty-name and BUFA_ rules.
	seen := Util.NewSet[string]()
	lineNo := 0
	for line := range strings.SplitSeq(strings.TrimPrefix(string(data), "\uFEFF"), "\n") {
		lineNo++
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			name, value, found := strings.Cut(line, "=")
			c.Require(found, "line %d '%s' has no '='", lineNo, line)
			c.Require(value != "", "line %d: variable '%s' has empty value", lineNo, name)
			folded := strings.ToLower(name)
			c.Require(!seen.Contains(folded), "line %d: duplicate variable '%s'", lineNo, name)
			seen.Add(folded)
			envVars.Set(name, value)
		}
	}
	CheckEnvVarNames(envVars)
	return envVars
}

type Filters struct {
	Src   []string `toml:"src"`
	Bld   []string `toml:"bld"`
	Dirty []string `toml:"dirty"`
}

// Fold order for this platform, general to specific.
var PlatformSections = func() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"windows"}
	case "linux":
		return []string{"unix", "linux"}
	case "darwin":
		return []string{"unix", "macos"}
	}
	return []string{"unix"}
}()

// "[unix]/[macos]" — for error messages.
var PlatformSectionNames = "[" + strings.Join(PlatformSections, "]/[") + "]"

func (cfg *BufaConfig) platformSection(name string) *BaseConfig {
	switch name {
	case "windows":
		return &cfg.Windows
	case "unix":
		return &cfg.Unix
	case "linux":
		return &cfg.Linux
	case "macos":
		return &cfg.MacOS
	}
	c.Fail("no platform section '%s'", name)
	return nil
}

func (cfg *BufaConfig) applyPlatformSettings(keys []toml.Key, sections []string) {
	definedKeys := newTomlKeysSet(keys)
	for _, name := range sections {
		if definedKeys.containsTomlKey(name) {
			section := cfg.platformSection(name)
			// Ordered before the fold: an override then lands in its top-level key's position.
			section.Env.orderByDocument(keys, name, "env")
			cfg.BaseConfig.applySection(section, definedKeys, name)
		}
	}
	cfg.Windows = BaseConfig{}
	cfg.Unix = BaseConfig{}
	cfg.Linux = BaseConfig{}
	cfg.MacOS = BaseConfig{}
}

func (cfg *BaseConfig) applySection(section *BaseConfig, definedKeys tomlKeysSet, platSecName string) {
	if definedKeys.containsTomlKey(platSecName, "unsafe") {
		cfg.Unsafe = section.Unsafe
	}
	if definedKeys.containsTomlKey(platSecName, "largeOutput") {
		cfg.LargeOutput = section.LargeOutput
	}
	if definedKeys.containsTomlKey(platSecName, "cmd") {
		cfg.Cmd = section.Cmd
	}
	if definedKeys.containsTomlKey(platSecName, "shell") {
		cfg.Shell = section.Shell
	}
	if definedKeys.containsTomlKey(platSecName, "deps", "cacheDir") {
		cfg.Deps.CacheDir = section.Deps.CacheDir
	}
	cfg.Deps.Src = slices.Concat(cfg.Deps.Src, section.Deps.Src)
	cfg.Deps.Bld = slices.Concat(cfg.Deps.Bld, section.Deps.Bld)
	cfg.Deps.Ext = slices.Concat(cfg.Deps.Ext, section.Deps.Ext)
	cfg.Deps.Export = slices.Concat(cfg.Deps.Export, section.Deps.Export)
	cfg.Filters.Src = slices.Concat(cfg.Filters.Src, section.Filters.Src)
	cfg.Filters.Bld = slices.Concat(cfg.Filters.Bld, section.Filters.Bld)
	cfg.Filters.Dirty = slices.Concat(cfg.Filters.Dirty, section.Filters.Dirty)
	cfg.Env.override(section.Env)
}

func (cfg *BufaConfig) IsScriptOptional() bool {
	return cfg.Cmd.Disabled
}

// The BUFA.shell schema, shared by the embedded presets and a provider dir's published definition.
type ShellDef struct {
	Name      string      `toml:"-"`
	Exe       string      `toml:"exe"` // ./x or bin/x ⇒ inside the provider's output, bare ⇒ LookPath, absolute as is
	Ext       string      `toml:"ext"` // script file = BUFA<ext>
	Run       []string    `toml:"run"`
	Shell     []string    `toml:"shell"`     // nil ⇒ no --shell mode
	PostShell []string    `toml:"postShell"` // nil ⇒ no --post-shell mode
	Prompt    ShellPrompt `toml:"prompt"`    // zero ⇒ no prompt marker
	Move      string      `toml:"move"`      // BUFA_COPY_OR_MOVE verbs; both or neither
	Copy      string      `toml:"copy"`
}

type ShellPrompt struct {
	Env     string `toml:"env"`
	Default string `toml:"default"`
}

func (def ShellDef) IsBareExe() bool {
	return filepath.Base(def.Exe) == def.Exe
}

// The only argv placeholder: the script's absolute path.
const ScriptPlaceholder = "${script}"

func DecodeShellDef(data []byte, path string) ShellDef {
	var def ShellDef
	DecodeStrict(data, path, &def)
	def.Validate(path)
	return def
}

func (def ShellDef) Validate(path string) {
	defer c.Context("Shell definition '%s'", path)
	referencesScript := func(argv []string) bool {
		return slices.ContainsFunc(argv, func(arg string) bool { return strings.Contains(arg, ScriptPlaceholder) })
	}
	c.Require(def.Exe != "", "exe is required")
	c.Require(def.Ext != "", "ext is required")
	c.Require(strings.HasPrefix(def.Ext, ".") && !strings.ContainsAny(def.Ext, `/\`),
		"ext '%s' must be a '.'-prefixed file extension", def.Ext)
	c.Require(def.Run != nil, "run is required")
	c.Require(referencesScript(def.Run),
		"run must pass the script: no argument references %s", ScriptPlaceholder)
	c.Require(def.PostShell == nil || referencesScript(def.PostShell),
		"postShell must pass the script: no argument references %s", ScriptPlaceholder)
	c.Require(def.Prompt.Env != "" || def.Prompt.Default == "", "prompt.default is set without prompt.env")
	c.Require((def.Move == "") == (def.Copy == ""), "move and copy must be set together")
}

func DecodeStrict[T any](data []byte, path string, v *T) {
	meta := c.With("Parse toml '%s'", path).Check2(toml.Decode(string(data), v))
	finalizeDecode(meta, path, v)
}

// Post-decode hook for shapes that need the document's key order — which no UnmarshalTOML can see.
type decodeFinalizer interface {
	afterDecode(keys []toml.Key)
}

func finalizeDecode[T any](meta toml.MetaData, path string, v *T) {
	checkUnknownKeys(meta, path)
	if finalizer, ok := any(v).(decodeFinalizer); ok {
		finalizer.afterDecode(meta.Keys())
	}
}

func (cfg *RootConfig) afterDecode(keys []toml.Key) {
	cfg.Env.orderByDocument(keys, "env")
}

func (cfg *BufaConfig) afterDecode(keys []toml.Key) {
	cfg.Env.orderByDocument(keys, "env")
	cfg.applyPlatformSettings(keys, PlatformSections)
}

func DecodeConfigOrScript(data []byte, path string, cfg *BufaConfig) {
	doc := string(data)
	meta, err := toml.Decode(doc, cfg)
	if err != nil {
		var probe map[string]any
		if _, syntaxErr := toml.Decode(doc, &probe); syntaxErr != nil {
			c.Require(!startedAsToml(doc, syntaxErr),
				"Cannot parse file '%s' as %s\n(if it's a raw script it must not start as parsable TOML, e.g. begin with a NAME=… line)", path, syntaxErr)
			// The file IS the script; rebuild from scratch — decode may have left partial fields.
			// Unsafe: a raw script can't declare deps, so its toolchain must come from the inherited PATH.
			*cfg = BufaConfig{BaseConfig: BaseConfig{Unsafe: true, Cmd: Cmd{Script: doc}}}
			return
		}
		c.Checkf(err, "Parse toml '%s'", path)
	}
	finalizeDecode(meta, path, cfg)
}

// True when the file was being read as TOML when it broke: a key consumed (LastKey is the parser's current key,
// not the last successful one — "" between statements), or a whole-line prefix that yields a key.
func startedAsToml(doc string, err error) bool {
	var parseErr toml.ParseError
	if !errors.As(err, &parseErr) || parseErr.LastKey != "" {
		return true
	}
	// SplitAfter keeps the line terminators: a prefix cut to a bare '\r' does not parse.
	lines := strings.SplitAfter(doc, "\n")
	progress := false
	for i := 1; !progress && i <= min(parseErr.Position.Line-1, len(lines)); i++ {
		var probe map[string]any
		_, prefixErr := toml.Decode(strings.Join(lines[:i], ""), &probe)
		progress = prefixErr == nil && len(probe) > 0
	}
	return progress
}

func checkUnknownKeys(meta toml.MetaData, path string) {
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		names := make([]string, len(undecoded))
		for i, key := range undecoded {
			names[i] = key.String()
		}
		c.Fail("Unknown key(s) in '%s': %s", path, strings.Join(names, ", "))
	}
}

// Case-folded (the decoder matches EqualFold) and prefix-inclusive (meta.Keys holds full paths).
type tomlKeysSet struct{ keys Util.Set[string] }

func newTomlKeysSet(keys []toml.Key) tomlKeysSet {
	set := tomlKeysSet{Util.NewSet[string]()}
	for _, key := range keys {
		for i := range key {
			set.keys.Add(foldKey(key[:i+1]))
		}
	}
	return set
}

func (s tomlKeysSet) containsTomlKey(key ...string) bool {
	return s.keys.Contains(foldKey(key))
}

// NUL-separated: quoted key parts can contain dots.
func foldKey(key []string) string {
	return strings.ToLower(strings.Join(key, "\x00"))
}
