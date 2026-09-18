package Build

import (
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

var envRef = regexp.MustCompile(`\$\$\{|\$\{[^}]+\}`)

// The one escape $${ emits a literal ${ and never starts a reference.
func expandRefs(s string, lookup func(name string) string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		if m == "$${" {
			return "${"
		}
		return lookup(m[2 : len(m)-1])
	})
}

// Whether value holds a ${ref} to the case-folded name; the $${ escape never counts.
func refersTo(value, foldedName string) bool {
	found := false
	expandRefs(value, func(name string) string {
		found = found || strings.ToLower(name) == foldedName
		return ""
	})
	return found
}

type envVar struct {
	name  string
	value string
}

// Set mirrors os/exec's dedup — last assignment wins, moved to the end — so Environ() carries no duplicates.
type Env struct {
	vars  map[string]envVar // foldEnvName(name) → original name + value
	order []string          // folded names, insertion order
}

func newEmptyEnv(capacity int) *Env {
	return &Env{
		vars:  make(map[string]envVar, capacity),
		order: make([]string, 0, capacity),
	}
}

func newEnv(environ []string) *Env {
	e := newEmptyEnv(len(environ))
	for _, kv := range environ {
		e.Set(splitEnvEntry(kv))
	}
	return e
}

// keep holds folded names; sized to it — never the whole environ.
func newFilteredEnv(environ []string, keep Util.Set[string]) *Env {
	e := newEmptyEnv(len(keep))
	for _, kv := range environ {
		name, value := splitEnvEntry(kv)
		if keep.Contains(foldEnvName(name)) {
			e.Set(name, value)
		}
	}
	return e
}

// os/exec's rule: a leading "=" (Windows drive cwds, "=C:=C:\…") belongs to the name.
func splitEnvEntry(kv string) (name, value string) {
	c.Assert(kv != "", "Empty environment entry")
	i := strings.Index(kv[1:], "=") + 1 // from 1: index 0 is never the separator
	c.Assert(i > 0, "Environment entry '%s' has no '='", kv)
	return kv[:i], kv[i+1:]
}

func (e *Env) Set(name, value string) {
	folded := foldEnvName(name)
	if _, found := e.vars[folded]; found {
		i := slices.Index(e.order, folded)
		e.order = slices.Delete(e.order, i, i+1)
	}
	e.vars[folded] = envVar{name, value}
	e.order = append(e.order, folded)
}

func (e *Env) Get(name, def string) string {
	value := def
	if kv, found := e.vars[foldEnvName(name)]; found {
		value = kv.value
	}
	return value
}

func (e *Env) Has(name string) bool {
	_, found := e.vars[foldEnvName(name)]
	return found
}

func (e *Env) resolve(name, folded string) string {
	kv, found := e.vars[folded]
	c.Require(found, "references undefined variable '%s' (only variables defined earlier are visible)", name)
	return kv.value
}

func (e *Env) Environ() []string {
	environ := make([]string, 0, len(e.order))
	for _, folded := range e.order {
		kv := e.vars[folded]
		environ = append(environ, kv.name+"="+kv.value)
	}
	return environ
}

// Matches os/exec's dedup: names are case-insensitive on Windows, exact elsewhere.
func foldEnvName(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(name)
	}
	return name
}
