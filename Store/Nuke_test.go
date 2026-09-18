package Store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/dimagog/bufa/internal/contract"
)

const testSockName = "bufa-d.sock"

func makeBldRoot(t *testing.T, entries ...string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "proj.BUFA")
	c.Check(os.MkdirAll(root, 0o755))
	for _, e := range entries {
		if strings.HasSuffix(e, "/") {
			c.Check(os.MkdirAll(filepath.Join(root, strings.TrimSuffix(e, "/"), "nested"), 0o755))
		} else {
			c.Check(os.WriteFile(filepath.Join(root, e), []byte("x"), 0o644))
		}
	}
	return root
}

func TestNuke_RemovesCleanLayout(t *testing.T) {
	root := makeBldRoot(t, "in/", "out/", "bld/", "dirty/", "tmp/", "user/", testSockName)

	Nuke(root, testSockName)

	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("build root must be gone, Lstat err = %v", err)
	}
}

func TestNuke_CaseInsensitiveAndSubset(t *testing.T) {
	root := makeBldRoot(t, "In/", "OUT/")

	Nuke(root, testSockName)

	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("build root must be gone, Lstat err = %v", err)
	}
}

func TestNuke_AllowedFilesMatchCaseInsensitively(t *testing.T) {
	root := makeBldRoot(t, "in/", "out/", "extra.TXT", testSockName)

	Nuke(root, testSockName, "Extra.txt", "other.txt")

	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("build root must be gone, Lstat err = %v", err)
	}
}

func TestNuke_MissingRootNoOp(t *testing.T) {
	Nuke(filepath.Join(t.TempDir(), "gone.BUFA"), testSockName)
}

func TestNuke_RefusesForeignEntries(t *testing.T) {
	root := makeBldRoot(t, "in/", "src/", "README.md", testSockName)

	err := c.Rescue(func() { Nuke(root, testSockName) })

	if err == nil {
		t.Fatal("foreign entries must refuse the nuke")
	}
	// Exactly the foreign entries in ReadDir order, dirs marked with a trailing slash.
	if msg := err.Error(); !strings.Contains(msg, "unexpected entries:\nREADME.md\nsrc/\n") {
		t.Errorf("refusal must name exactly the foreign entries, got %q", msg)
	}
	if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
		t.Errorf("refused nuke must leave the dir untouched: %v", err)
	}
}

func TestNuke_RefusesWrongKind(t *testing.T) {
	// A file squatting on a layout-root name and a dir squatting on the sock name.
	root := makeBldRoot(t, "in", testSockName+"/")

	err := c.Rescue(func() { Nuke(root, testSockName) })

	if err == nil {
		t.Fatal("wrong-kind entries must refuse the nuke")
	}
	if msg := err.Error(); !strings.Contains(msg, "in") || !strings.Contains(msg, testSockName+"/") {
		t.Errorf("refusal must name the wrong-kind entries, got %q", msg)
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("refused nuke must leave the dir untouched: %v", err)
	}
}
