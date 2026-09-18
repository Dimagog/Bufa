package Build

import (
	"log/slog"
	"path/filepath"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Cache"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/vfsx"
)

// A bld dep's published BUFA.env: vars exported into every direct dependent's script env.
type depEnv struct {
	file string // <depDir>/BUFA.env, the error label
	vars BuildConfig.EnvTable[string]
}

func (b *BuilderBase) newDepEnvLoader(fs vfs.Fs, fsRoot string) func(string) depEnv {
	return Cache.WrapInt(
		func(dep string) depEnv { return loadDepEnv(dep, fs, fsRoot) },
		b.localDepEnvCache,
		"DepEnv", "no-value",
	)
}

func loadDepEnv(dep string, fs vfs.Fs, fsRoot string) depEnv {
	env := depEnv{file: filepath.Join(dep, Store.EnvFileName)}
	if data := vfsx.TryReadSmallFileFast(fs, filepath.Join(fsRoot, env.file)); data != nil {
		slog.Info("Loading dep env exports:", "dep", dep, "path", env.file)
		env.vars = BuildConfig.DecodeEnvFile(data, env.file)
	}
	return env
}

func (b *BuilderBase) depEnvsFor(bldDeps []string) []depEnv {
	var depEnvs []depEnv
	for _, dep := range bldDeps {
		if env := b.loadDepEnv(dep); env.vars.Len() > 0 {
			depEnvs = append(depEnvs, env)
		}
	}
	return depEnvs
}
