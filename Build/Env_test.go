package Build

import (
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

func TestEnv_LastWinsMovesToEnd(t *testing.T) {
	env := newEnv([]string{"A=1", "B=2", "A=3"})
	if got := env.Environ(); !slices.Equal(got, []string{"B=2", "A=3"}) {
		t.Errorf("Environ = %q, want [B=2 A=3]", got)
	}
	if got := env.Get("A", "def"); got != "3" {
		t.Errorf("Get(A) = %q, want 3", got)
	}
	if got := env.Get("C", "def"); got != "def" {
		t.Errorf("Get(C) = %q, want the default", got)
	}
}

func TestEnv_FoldsLikeOsExec(t *testing.T) {
	env := newEnv([]string{"Path=old", "X=1"})
	env.Set("PATH", "new")
	want := []string{"X=1", "PATH=new"}
	wantOld := "new"
	if runtime.GOOS != "windows" {
		want = []string{"Path=old", "X=1", "PATH=new"}
		wantOld = "old"
	}
	if got := env.Environ(); !slices.Equal(got, want) {
		t.Errorf("Environ = %q, want %q", got, want)
	}
	if got := env.Get("Path", ""); got != wantOld {
		t.Errorf("Get(Path) = %q, want %q", got, wantOld)
	}
	if got := env.Has("path"); got != (runtime.GOOS == "windows") {
		t.Errorf("Has(path) = %v on %s", got, runtime.GOOS)
	}
}

func TestEnv_LeadingEqualsIsPartOfName(t *testing.T) {
	entries := []string{`=C:=C:\work`, `=D:=D:\`, "A=1"}
	env := newEnv(entries)
	if got := env.Environ(); !slices.Equal(got, entries) {
		t.Errorf("Environ = %q, want the entries round-tripped %q", got, entries)
	}
	if !env.Has("=C:") {
		t.Error("Has(=C:) = false")
	}
	if got := env.Get("=D:", ""); got != `D:\` {
		t.Errorf("Get(=D:) = %q, want D:\\", got)
	}
}

func TestEnv_NewFilteredEnvKeepsOnlyTheKeepList(t *testing.T) {
	keep := Util.NewSet[string]()
	keep.Add(foldEnvName("C"))
	keep.Add(foldEnvName("A"))
	env := newFilteredEnv([]string{"A=1", "B=2", "C=3"}, keep)
	if got := env.Environ(); !slices.Equal(got, []string{"A=1", "C=3"}) {
		t.Errorf("Environ = %q, want [A=1 C=3]", got)
	}
	if env.Has("B") {
		t.Error("Has(B) = true after filtering")
	}
	if got := newFilteredEnv([]string{"A=1"}, Util.NewSet[string]()).Environ(); len(got) != 0 {
		t.Errorf("Environ = %q, want empty", got)
	}
}

func TestEnv_EntryWithoutEqualsFails(t *testing.T) {
	err := c.Rescue(func() { newEnv([]string{"A=1", "BOGUS"}) })
	if err == nil || !strings.Contains(err.Error(), "has no '='") {
		t.Errorf("err = %v, want the no-'=' assert", err)
	}
}

func TestEnv_ExpandGrammar(t *testing.T) {
	env := newEnv([]string{"A=1", "B=x y"})
	cases := map[string]string{
		"plain":       "plain",
		"${A}":        "1",
		"<${A}|${B}>": "<1|x y>",
		"$${A}":       "${A}",
		"$${A}${A}":   "${A}1",
		"${}":         "${}",
		"${A":         "${A",
		"$A $$A":      "$A $$A",
	}
	for in, want := range cases {
		env.setExpanded("X", in, "d", nil)
		if got := env.Get("X", ""); got != want {
			t.Errorf("setExpanded(%q) = %q, want %q", in, got, want)
		}
	}
	err := c.Rescue(func() { env.setExpanded("X", "${NOPE}", "d", nil) })
	if err == nil || !strings.Contains(err.Error(), "undefined variable 'NOPE'") {
		t.Errorf("err = %v, want the undefined-variable error naming NOPE", err)
	}
}

func TestEnv_ExpandFoldsLikeOsExec(t *testing.T) {
	env := newEnv([]string{"Path=p"})
	err := c.Rescue(func() {
		env.setExpanded("X", "${PATH}", "d", nil)
		if got := env.Get("X", ""); got != "p" {
			t.Errorf("setExpanded(${PATH}) = %q, want p", got)
		}
	})
	if (err == nil) != (runtime.GOOS == "windows") {
		t.Errorf("err = %v on %s", err, runtime.GOOS)
	}
}

func TestEnv_SetExpanded(t *testing.T) {
	env := newEnv([]string{"A=1"})
	env.setExpanded("B", "${A}2", "d", nil)
	env.setExpanded("A", "${A}${B}", "d", nil) // sees its own earlier value
	if got := env.Environ(); !slices.Equal(got, []string{"B=12", "A=112"}) {
		t.Errorf("Environ = %q, want [B=12 A=112]", got)
	}
	err := c.Rescue(func() { env.setExpanded("C", "${Z}", "d", nil) })
	if err == nil || !strings.Contains(err.Error(), "[env] variable 'C' of 'd'") ||
		!strings.Contains(err.Error(), "undefined variable 'Z'") {
		t.Errorf("err = %v, want the error to name the variable, the file, and the missing name", err)
	}
	if env.Has("C") {
		t.Error("a rejected set left C behind")
	}
}
