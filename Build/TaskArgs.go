package Build

import (
	"os"
	"path/filepath"
	"strings"

	c "github.com/dimagog/bufa/internal/contract"
)

func (b *BuilderBase) IsTask(srcDir string) bool {
	return b.getBuildConfig(filepath.Clean(srcDir)).Task.Enabled
}

// A name without a token falls back to the caller's variable of that name (safeInheritedEnvFor lets it
// through), which a fixed name must have. Bound here, never on the shared BufaConfig.
func (b *BuilderBase) BindArgs(srcDir string, tokens []string) {
	srcDir = filepath.Clean(srcDir)
	defer c.Context("Task '%s' arguments", srcDir)
	task := b.getBuildConfig(srcDir).Task
	c.Assert(task.Enabled, "'%s' is not a task", srcDir)
	fixed := task.Fixed()
	b.argsDir = srcDir
	b.args = make([]envVar, 0, len(fixed)+1)
	for i, name := range fixed {
		if i < len(tokens) {
			b.bindArg(name, tokens[i])
		} else {
			_, inherited := os.LookupEnv(name)
			c.Require(inherited, "argument '%s' is missing: pass it on the command line or set the env variable", name)
		}
	}
	rest := tokens[min(len(fixed), len(tokens)):]
	if task.Tail {
		if len(rest) > 0 {
			for _, token := range rest {
				checkArgToken(task.TailName(), token)
			}
			b.args = append(b.args, envVar{task.TailName(), strings.Join(rest, " ")})
		}
	} else {
		c.Require(len(rest) == 0, "takes %d argument(s), unexpected: '%s'", len(fixed), strings.Join(rest, "' '"))
	}
}

func (b *BuilderBase) bindArg(name, token string) {
	checkArgToken(name, token)
	b.args = append(b.args, envVar{name, token})
}

// NormalizeEnvVarValue's rules minus the trim: a token is literal.
func checkArgToken(name, token string) {
	c.Require(token != "", "argument '%s' must not be empty", name)
	c.Require(!strings.ContainsAny(token, "\r\n"), "argument '%s' must hold a single line", name)
}

func (b *BuilderBase) setArgs(env *Env, srcDir string) {
	if srcDir == b.argsDir {
		for _, arg := range b.args {
			env.Set(arg.name, arg.value)
		}
	}
}
