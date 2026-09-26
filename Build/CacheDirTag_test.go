package Build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

const specSignatureLine = "Signature: 8a477f597d28d172789f06886806bc55\n"

// Daemon disabled: no spawn creates the build root, so the build's first store write must.
func TestBuild_FreshBuildRootIsTagged(t *testing.T) {
	f := newFixture(t)
	c.Check(os.Remove(f.bld))
	f.unit(t, "u", "x")

	f.builder().Build("u")

	tag := string(c.Check2(os.ReadFile(filepath.Join(f.bld, Store.CacheDirTagName))))
	if !strings.HasPrefix(tag, specSignatureLine) {
		t.Errorf("build root tag = %q, want the spec signature first", tag)
	}
}

func TestDirtyBuild_FreshBuildRootIsTagged(t *testing.T) {
	f := newFixture(t)
	c.Check(os.Remove(f.bld))
	f.unit(t, "u", "x")

	f.dirtyBuilder().Build("u")

	tag := string(c.Check2(os.ReadFile(filepath.Join(f.bld, Store.CacheDirTagName))))
	if !strings.HasPrefix(tag, specSignatureLine) {
		t.Errorf("build root tag = %q, want the spec signature first", tag)
	}
}

func TestBuild_ExistingBuildRootNotTagged(t *testing.T) {
	f := newFixture(t) // pre-creates the build root
	f.unit(t, "u", "x")

	f.builder().Build("u")

	if _, err := os.Lstat(filepath.Join(f.bld, Store.CacheDirTagName)); !os.IsNotExist(err) {
		t.Errorf("a pre-existing build root must not be tagged, Lstat err = %v", err)
	}
}
