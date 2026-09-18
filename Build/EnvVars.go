package Build

import (
	"iter"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

type envTable = BuildConfig.EnvTable[BuildConfig.EnvVar]

var safeInheritedEnv = func() Util.Set[string] {
	keep := Util.NewSet[string]()
	if runtime.GOOS != "windows" {
		return keep // bufa sets PATH/TMPDIR/HOME itself; a safe Unix script inherits nothing
	}
	// Doc/Specs/WindowsMinEnvVars.html's must-keep list minus ComSpec/Path/PATHEXT/TEMP/TMP — bufa sets its own.
	for _, name := range []string{"OS", "SystemDrive", "SystemRoot", "windir"} {
		keep.Add(foldEnvName(name))
	}
	return keep
}()

func (b *BuilderBase) resolveEnvVars(srcDir string, envVars envTable, virtualDir bool) {
	if envVars.Len() == 0 {
		return
	}
	BuildConfig.CheckEnvVarNames(envVars)
	for key, val := range envVars.All() {
		envVars.Set(key, BuildConfig.EnvVar{Value: b.resolveEnvVarValue(srcDir, key, val, virtualDir)})
	}
}

func (b *BuilderBase) resolveEnvVarValue(srcDir, key string, v BuildConfig.EnvVar, virtualDir bool) string {
	defer c.Context("[env] variable '%s' of '%s'", key, srcDir)

	value := v.Value
	if v.File != "" {
		c.Require(!virtualDir, "[env] variable '%s' is file-sourced, but virtual build config '%s' has no own source",
			key, Store.VirtualConfigPathForDir(srcDir))
		file := cleanLocalName("[env] source file", v.File)
		value = string(vfsx.ReadSmallFileFast(b.SrcFS, filepath.Join(srcDir, filepath.FromSlash(file))))
	}
	return BuildConfig.NormalizeEnvVarValue(key, value)
}

// Root, deps (in dep order), then dir entries, each expanded against the env so far. The root↔dir pair shares
// one definedLater; each dep file gets its own — its refs chain on the layers below and never gate a root ref.
func (b *BuilderBase) setAllEnvVars(env *Env, srcDir string, cfg BuildConfig.BufaConfig, depEnvs []depEnv) {
	rootEnv := b.RootConfig.Env
	if rootEnv.Len()+cfg.Env.Len()+len(depEnvs) == 0 {
		return
	}
	checkRootDirEnvCase(rootEnv, cfg.Env, srcDir)
	checkDepOverwrites(depEnvs)
	addDefinedLater := func(definedLater map[string]int, keys iter.Seq[string]) {
		for name := range keys {
			definedLater[foldEnvName(name)]++
		}
	}
	definedLater := make(map[string]int, rootEnv.Len()+cfg.Env.Len())
	addDefinedLater(definedLater, rootEnv.Keys())
	addDefinedLater(definedLater, cfg.Env.Keys())
	for name, value := range rootEnv.All() {
		definedLater[foldEnvName(name)]-- // order vs setExpanded immaterial (self-refs exempt); later entries see it
		env.setExpanded(name, value, Store.SrcRootFileName, definedLater)
	}
	for _, dep := range depEnvs {
		depDefinedLater := make(map[string]int, dep.vars.Len())
		addDefinedLater(depDefinedLater, dep.vars.Keys())
		for name, value := range dep.vars.All() {
			depDefinedLater[foldEnvName(name)]--
			env.setExpanded(name, value, dep.file, depDefinedLater)
		}
	}
	for name, v := range cfg.Env.All() {
		c.Assert(v.Value != "", "[env] variable '%s' of '%s' is unresolved", name, srcDir)
		definedLater[foldEnvName(name)]--
		env.setExpanded(name, v.Value, srcDir, definedLater)
	}
}

// A ref to a name a later entry (re)defines would otherwise silently resolve to a value the script won't see.
// A self-reference is exempt: each redefinition chains on (or deliberately discards) the layer below.
func (e *Env) setExpanded(key, value, srcDir string, definedLater map[string]int) {
	defer c.Context("[env] variable '%s' of '%s'", key, srcDir)
	foldedKey := foldEnvName(key)
	e.Set(key, expandRefs(value, func(ref string) string {
		folded := foldEnvName(ref)
		c.Require(folded == foldedKey || definedLater[folded] == 0,
			"references [env] variable '%s' (re)defined later — a value sees only earlier entries", ref)
		return e.resolve(ref, folded)
	}))
}

// Case-fold-only matches are rejected on every OS: the exported names would collide on Windows alone.
// Root↔dir only — a dep layer is foreign like the inherited env, which this check never covered either.
func checkRootDirEnvCase(rootEnv BuildConfig.EnvTable[string], dirEnv envTable, srcDir string) {
	if rootEnv.Len() > 0 && dirEnv.Len() > 0 {
		foldedRootEnvKeys := make(map[string]string, rootEnv.Len()) // lower(rootKey) → rootKey
		for rootKey := range rootEnv.Keys() {
			foldedRootEnvKeys[strings.ToLower(rootKey)] = rootKey
		}
		for key := range dirEnv.Keys() {
			if rootKey, found := foldedRootEnvKeys[strings.ToLower(key)]; found {
				c.Require(rootKey == key,
					"[env] variable '%s' of '%s' differs only in case from root %s [env] variable '%s'",
					key, srcDir, Store.SrcRootFileName, rootKey)
			}
		}
	}
}

// Blind siblings: a later dep may redefine an earlier dep's export (case-folded) only with the identical value
// or by chaining on the name (the PATH-prepend pattern) — a plain overwrite clobbers silently.
func checkDepOverwrites(depEnvs []depEnv) {
	if len(depEnvs) < 2 {
		return
	}
	type declaration struct {
		key, value, file string
	}
	exported := make(map[string]declaration) // lower(key) → its first dep declaration
	for _, dep := range depEnvs {
		for name, value := range dep.vars.All() {
			folded := strings.ToLower(name)
			if prev, found := exported[folded]; found {
				c.Require(value == prev.value || refersTo(value, folded),
					"[env] variable '%s' of '%s' overwrites '%s' of '%s'; "+
						"a dep may redefine another dep's variable only with the identical value or by chaining on it "+
						"(a value referencing ${%s})",
					name, dep.file, prev.key, prev.file, prev.key)
			} else {
				exported[folded] = declaration{name, value, dep.file}
			}
		}
	}
}

func interpolateExtDepUrls(deps []BuildConfig.ExtDep, envVars envTable) {
	for i := range deps {
		deps[i].Url = interpolateVars(deps[i].Url, envVars)
	}
}

// Urls are cache-key terms: an entry that itself holds a reference (expanded only at script run) is rejected.
func interpolateVars(s string, envVars envTable) string {
	return expandRefs(s, func(name string) string {
		v, ok := envVars.Get(name)
		c.Require(ok, "String '%s' references undefined [env] variable '%s'", s, name)
		c.Require(!strings.Contains(v.Value, "${"),
			"String '%s' references [env] variable '%s' whose value '%s' holds '${' — urls must be static",
			s, name, v.Value)
		return v.Value
	})
}
