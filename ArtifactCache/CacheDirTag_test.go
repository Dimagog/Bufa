package ArtifactCache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
)

func TestFetch_FirstFetchTagsCacheDir(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	data := []byte("artifact-bytes")
	srv, _ := serve(t, data)
	ac := New(filepath.Join(t.TempDir(), "cache"))

	var out bytes.Buffer
	ac.Ensure(srv.URL+"/art.bin", pinOf(data), &out)

	got := string(c.Check2(os.ReadFile(filepath.Join(ac.Dir(), Store.CacheDirTagName))))
	if want := "Signature: 8a477f597d28d172789f06886806bc55\n" + cacheDirTagComment + "\n"; got != want {
		t.Errorf("cache tag = %q, want %q", got, want)
	}
}

func TestFetch_ExistingCacheDirNotTagged(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	data := []byte("artifact-bytes")
	srv, _ := serve(t, data)
	ac := makeCacheDir(t)

	var out bytes.Buffer
	ac.Ensure(srv.URL+"/art.bin", pinOf(data), &out)

	if _, err := os.Lstat(filepath.Join(ac.Dir(), Store.CacheDirTagName)); !os.IsNotExist(err) {
		t.Errorf("a pre-existing cache dir must not be tagged, Lstat err = %v", err)
	}
}

// Admitted by name alone: the content is never read.
func TestCheck_AcceptsCacheDirTag(t *testing.T) {
	ac := makeCacheDir(t, "cachedir.tag")

	st, report := runCheck(ac, true /*fix*/)
	if st != (CheckStats{}) || report != "" {
		t.Errorf("tag must be owned: CheckStats = %+v, report:\n%s", st, report)
	}
	if _, err := os.Lstat(filepath.Join(ac.Dir(), "cachedir.tag")); err != nil {
		t.Errorf("fix must keep the tag: %v", err)
	}
}

func TestNuke_AcceptsCacheDirTag(t *testing.T) {
	ac := makeCacheDir(t, pinOf([]byte("x")), Store.CacheDirTagName)

	ac.Nuke()

	if _, err := os.Lstat(ac.Dir()); !os.IsNotExist(err) {
		t.Fatalf("cache dir must be gone, Lstat err = %v", err)
	}
}
