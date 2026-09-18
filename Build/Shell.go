package Build

import (
	"log/slog"
	"maps"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/UnsafeIO"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

const shellPresetPrefix = ":"

// cmd is Windows-only; bash is offered everywhere (Git Bash / WSL on a Windows PATH).
var shellPresets = sync.OnceValue(func() map[string]BuildConfig.ShellDef {
	presets := map[string]BuildConfig.ShellDef{
		"bash": {
			Exe: "bash", Ext: ".bash",
			Run: []string{BuildConfig.ScriptPlaceholder},
			// --norc: the script's non-interactive bash reads no rc file either;
			// -i: an rcfile is read by an interactive shell only, tty or not. It must follow
			// --rcfile: bash recognizes long options only ahead of single-character ones.
			Shell:     []string{"--norc"},
			PostShell: []string{"--rcfile", BuildConfig.ScriptPlaceholder, "-i"},
			Prompt:    BuildConfig.ShellPrompt{Env: "PS1", Default: `\s-\v\$ `},
			Move:      "mv", Copy: "cp",
		},
	}
	if runtime.GOOS == "windows" {
		presets["cmd"] = BuildConfig.ShellDef{
			Exe: cleanComSpec(), Ext: ".cmd",
			// /D: no AutoRun registry commands — an ambient machine setting must not reach a build.
			Run:       []string{"/D", "/C", BuildConfig.ScriptPlaceholder},
			Shell:     []string{"/D", "/K"},
			PostShell: []string{"/D", "/K", BuildConfig.ScriptPlaceholder},
			Prompt:    BuildConfig.ShellPrompt{Env: "PROMPT", Default: "$P$G"},
			Move:      "move", Copy: "copy",
		}
	}
	return presets
})

var nativeShellName = func() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return "bash"
}()

func (b *BuilderBase) shellFor(srcDir string, cfg BuildConfig.BufaConfig) string {
	if cfg.IsScriptOptional() && !b.ShellSessionFor(srcDir) {
		slog.Info("No shell needed", "dir", srcDir)
		return ""
	}
	source := "dir"
	shellName, baseDir := cfg.Shell, srcDir
	if shellName == "" {
		source = "root"
		shellName, baseDir = b.RootConfig.Shell, "."
	}
	if shellName == "" {
		source = "native"
		shellName = nativeShellName
	}
	shell := b.resolveShell(shellName, baseDir)
	if _, preset := decodeShellPreset(shell); !preset && shell == srcDir {
		// A project mapping `bash` to a versioned Bash provider still has to build that provider
		// under the preset, and the shadowed name is its only spelling.
		_, shadowed := shellPresets()[shellName]
		c.Require(shadowed,
			"'%s' depends on itself via shell '%s'; a shell provider must name its own shell explicitly", srcDir, shellName)
		slog.Info("Shell provider uses shadowed preset", "dir", srcDir, "shell", shellName)
		shell = encodeShellPreset(shellName)
	}
	slog.Info("Shell:", "dir", srcDir, "shell", shellName, "source", source, "resolved", shell)
	return shell
}

func (b *BuilderBase) resolveShell(shellName, baseDir string) string {
	if BuildConfig.IsShellPath(shellName) {
		return requireShellProviderDir(resolveRelDir(baseDir, shellName))
	}
	if dir, ok := b.RootConfig.Shells[shellName]; ok {
		return requireShellProviderDir(resolveRelDir(".", dir))
	}
	_, ok := shellPresets()[shellName]
	c.Require(ok, "unknown shell '%s': neither a %s [shells] alias nor a preset (%s)",
		shellName, Store.SrcRootFileName, strings.Join(slices.Sorted(maps.Keys(shellPresets())), ", "))
	return encodeShellPreset(shellName)
}

func encodeShellPreset(name string) string {
	return shellPresetPrefix + name
}

func decodeShellPreset(shell string) (string, bool) {
	return strings.CutPrefix(shell, shellPresetPrefix)
}

func requireShellProviderDir(dir string) string {
	c.Require(!strings.HasPrefix(dir, shellPresetPrefix),
		"shell provider dir '%s' must not start with reserved '%s'", dir, shellPresetPrefix)
	return dir
}

func effectiveBldDeps(srcDir string, cfg BuildConfig.BufaConfig, shell string) []string {
	if shell == "" {
		return cfg.Deps.Bld
	}
	if _, preset := decodeShellPreset(shell); preset {
		return cfg.Deps.Bld
	}
	c.Require(!slices.Contains(cfg.Deps.Bld, shell),
		"'%s' lists its shell provider '%s' in deps.bld; bufa adds that dependency itself", srcDir, shell)
	slog.Info("Adding shell provider dependency", "dir", srcDir, "provider", shell)
	return slices.Concat(cfg.Deps.Bld, []string{shell})
}

func loadShellDef(shell string, fs vfs.Fs, fsRoot string) BuildConfig.ShellDef {
	if presetName, isPreset := decodeShellPreset(shell); isPreset {
		preset, ok := shellPresets()[presetName]
		c.Assert(ok, "unknown resolved shell preset '%s'", presetName)
		preset.Name = presetName
		preset.Exe = resolveShellExe(preset, nil)
		slog.Info("Using shell preset", "shell", presetName, "exe", preset.Exe, "ext", preset.Ext)
		return preset
	}
	providerDir := filepath.Join(fsRoot, shell)
	defPath := filepath.Join(providerDir, Store.ShellDefFileName)
	slog.Info("Loading shell definition:", "provider", shell, "path", defPath)
	data := vfsx.TryReadSmallFileFast(fs, defPath)
	c.Require(data != nil, "shell provider '%s' output has no %s", shell, Store.ShellDefFileName)
	def := BuildConfig.DecodeShellDef(data, defPath)
	def.Name = shell
	def.Exe = resolveShellExe(def, func() string { return UnsafeIO.RealPath(fs, providerDir) })
	slog.Info("Shell definition loaded", "provider", shell, "exe", def.Exe, "ext", def.Ext)
	return def
}

func resolveShellExe(def BuildConfig.ShellDef, providerOSDir func() string) string {
	var resolved string
	var via string
	if filepath.IsAbs(def.Exe) {
		resolved = def.Exe
		via = "absolute"
	} else if def.IsBareExe() {
		resolved = c.With("Shell '%s': find executable '%s'", def.Name, def.Exe).Check2(exec.LookPath(def.Exe))
		via = "PATH"
	} else {
		c.Assert(providerOSDir != nil, "shell '%s': preset exe '%s' is provider-relative", def.Name, def.Exe)
		resolved = filepath.Join(providerOSDir(), def.Exe)
		via = "provider"
	}
	slog.Info("Shell executable resolved", "shell", def.Name, "configured", def.Exe, "resolved", resolved, "via", via)
	return resolved
}

func shellCommand(def BuildConfig.ShellDef, mode string, argv []string, script string) *exec.Cmd {
	args := make([]string, len(argv))
	for i, arg := range argv {
		args[i] = strings.ReplaceAll(arg, BuildConfig.ScriptPlaceholder, script)
	}
	slog.Debug("Shell command", "shell", def.Name, "mode", mode, "exe", def.Exe, "args", args)
	return exec.Command(def.Exe, args...)
}

func scriptCommand(def BuildConfig.ShellDef, script string) *exec.Cmd {
	return shellCommand(def, "run", def.Run, script)
}

// A non-empty script is run by the shell process itself, so the session keeps the script's
// state — and a bare `exit` in it ends the session. The prompt is the shell's own, prefixed.
func sessionCommand(def BuildConfig.ShellDef, env *Env, script string) *exec.Cmd {
	argv := def.Shell
	mode := "shell"
	if script != "" {
		argv = def.PostShell
		mode = "postShell"
	}
	c.Require(argv != nil, "shell '%s' defines no '%s' session mode in its %s", def.Name, mode, Store.ShellDefFileName)
	if def.Prompt.Env != "" {
		env.Set(def.Prompt.Env, "(bufa shell) "+env.Get(def.Prompt.Env, def.Prompt.Default))
	}
	cmd := shellCommand(def, mode, argv, script)
	cmd.Env = env.Environ()
	return cmd
}
