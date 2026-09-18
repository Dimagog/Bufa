package ArtifactCache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/dimagog/bufa/internal/contract"
)

// Plain files stand in for F entries and fetch- leftovers — Nuke checks those by name and
// regular kind; anything symlink-flavored (url links, url- leftovers) is the caller's job.
func makeCacheDir(t *testing.T, entries ...string) *Cache {
	t.Helper()
	ac := New(filepath.Join(t.TempDir(), "cache"))
	c.Check(os.MkdirAll(ac.Dir(), 0o755))
	for _, e := range entries {
		if strings.HasSuffix(e, "/") {
			c.Check(os.MkdirAll(filepath.Join(ac.Dir(), strings.TrimSuffix(e, "/")), 0o755))
		} else {
			c.Check(os.WriteFile(filepath.Join(ac.Dir(), e), []byte("x"), 0o644))
		}
	}
	return ac
}

func TestNuke_RemovesCacheEntries(t *testing.T) {
	pin := pinOf([]byte("art"))
	ac := makeCacheDir(t, pin, "fetch-12345")
	// Entries are chmod'd read-only at insert; the delete path must clear that.
	c.Check(os.Chmod(filepath.Join(ac.Dir(), pin), 0o444))

	ac.Nuke()

	if _, err := os.Lstat(ac.Dir()); !os.IsNotExist(err) {
		t.Fatalf("cache dir must be gone, Lstat err = %v", err)
	}
}

func TestNuke_RemovesUrlLinks(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	pin := pinOf([]byte("art"))
	urlKey := urlLinkName("http://x/art")
	ac := makeCacheDir(t)
	c.Check(os.Symlink(pin, filepath.Join(ac.Dir(), urlKey)))
	// A dangling non-hash target: only the url- prefix admits a staging leftover.
	c.Check(os.Symlink("x", filepath.Join(ac.Dir(), "url-"+urlKey+"-1-1")))

	ac.Nuke()

	if _, err := os.Lstat(ac.Dir()); !os.IsNotExist(err) {
		t.Fatalf("cache dir must be gone, Lstat err = %v", err)
	}
}

func TestNuke_RefusesUrlNamedNonLink(t *testing.T) {
	urlKey := urlLinkName("http://x/art")
	ac := makeCacheDir(t, urlKey)

	err := c.Rescue(func() { ac.Nuke() })

	if err == nil {
		t.Fatal("a regular file squatting on a url-link name must refuse the nuke")
	}
	if !strings.Contains(err.Error(), urlKey) {
		t.Errorf("refusal must name the squatter, got %q", err.Error())
	}
}

func TestNuke_RefusesUrlLinkWithForeignTarget(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	urlKey := urlLinkName("http://x/art")
	ac := makeCacheDir(t)
	c.Check(os.Symlink("not-a-hash", filepath.Join(ac.Dir(), urlKey)))

	err := c.Rescue(func() { ac.Nuke() })

	if err == nil {
		t.Fatal("a url link with a foreign target must refuse the nuke")
	}
	if !strings.Contains(err.Error(), urlKey) {
		t.Errorf("refusal must name the offending link, got %q", err.Error())
	}
}

func TestNuke_MissingDirNoOp(t *testing.T) {
	New(filepath.Join(t.TempDir(), "gone")).Nuke()
}

func TestNuke_RefusesForeignEntries(t *testing.T) {
	pin := pinOf([]byte("art"))
	ac := makeCacheDir(t, pin, "notes.txt", "Fnot-a-hash", "sub/")

	err := c.Rescue(func() { ac.Nuke() })

	if err == nil {
		t.Fatal("foreign entries must refuse the nuke")
	}
	if msg := err.Error(); !strings.Contains(msg, "unexpected entries:\nFnot-a-hash\nnotes.txt\nsub/\n") {
		t.Errorf("refusal must name exactly the foreign entries, got %q", msg)
	}
	if !ac.Contains(pin) {
		t.Error("refused nuke must leave the cache untouched")
	}
}

func TestNuke_RefusesDirSquattingOnEntryName(t *testing.T) {
	pin := pinOf([]byte("art"))
	ac := makeCacheDir(t, pin+"/")

	err := c.Rescue(func() { ac.Nuke() })

	if err == nil {
		t.Fatal("a directory squatting on an entry name must refuse the nuke")
	}
	if msg := err.Error(); !strings.Contains(msg, pin+"/") {
		t.Errorf("refusal must name the squatting dir, got %q", msg)
	}
}
