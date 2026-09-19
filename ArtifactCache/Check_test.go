package ArtifactCache

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/dimagog/bufa/internal/contract"
)

func runCheck(ac *Cache, fix bool) (CheckStats, string) {
	var buf bytes.Buffer
	st := ac.Check(&buf, fix)
	return st, buf.String()
}

func addEntry(t *testing.T, ac *Cache, data []byte) string {
	t.Helper()
	pin := pinOf(data)
	c.Check(os.WriteFile(filepath.Join(ac.Dir(), pin), data, 0o444))
	return pin
}

func addUrlLink(t *testing.T, ac *Cache, url, target string) string {
	t.Helper()
	name := urlLinkName(url)
	c.Check(os.Symlink(target, filepath.Join(ac.Dir(), name)))
	return name
}

func assertGone(t *testing.T, ac *Cache, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(ac.Dir(), name)); !os.IsNotExist(err) {
			t.Errorf("'%s' must be removed, Lstat err = %v", name, err)
		}
	}
}

func TestCheck_CleanCache(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	ac := makeCacheDir(t)
	pin := addEntry(t, ac, []byte("art"))
	addUrlLink(t, ac, "http://x/art", pin)
	addUrlLink(t, ac, "http://mirror/art", pin)
	addEntry(t, ac, []byte("unlinked")) // a crashed run's publish window: legal, just never a hit

	st, report := runCheck(ac, false /*fix*/)
	if want := (CheckStats{CheckedEntries: 2, CheckedLinks: 2}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if report != "" {
		t.Errorf("clean cache must report no problems, got:\n%s", report)
	}
}

func TestCheck_MissingDirNoOp(t *testing.T) {
	st, report := runCheck(New(filepath.Join(t.TempDir(), "gone")), true /*fix*/)
	if st != (CheckStats{}) || report != "" {
		t.Errorf("missing cache dir must be a silent no-op, got %+v:\n%s", st, report)
	}
}

func TestCheck_CorruptEntryNamesUrlLinks(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	ac := makeCacheDir(t)
	pin := pinOf([]byte("art"))
	c.Check(os.WriteFile(filepath.Join(ac.Dir(), pin), []byte("TAMPERED"), 0o444))
	link := addUrlLink(t, ac, "http://x/art", pin)

	st, report := runCheck(ac, false /*fix*/)
	if st.CorruptEntries != 1 || !st.HasProblems() || st.RemovedEntries != 0 {
		t.Errorf("CheckStats = %+v, want 1 corrupt entry, nothing removed", st)
	}
	if !strings.Contains(report, "CORRUPT: '"+ac.EntryPath(pin)+"' content hashes to '"+pinOf([]byte("TAMPERED"))+"'") {
		t.Errorf("report must name the corrupt entry and its real hash, got:\n%s", report)
	}
	if !strings.Contains(report, "(url links: '"+link+"')") {
		t.Errorf("report must correlate to the url link, got:\n%s", report)
	}
	if !ac.Contains(pin) {
		t.Error("a plain check must not delete anything")
	}
}

func TestCheck_FixRemovesCorruptEntryAndItsLinks(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	ac := makeCacheDir(t)
	good := addEntry(t, ac, []byte("good"))
	addUrlLink(t, ac, "http://x/good", good)
	pin := pinOf([]byte("art"))
	// Read-only like a real entry: the delete path must clear that.
	c.Check(os.WriteFile(filepath.Join(ac.Dir(), pin), []byte("TAMPERED"), 0o444))
	link1 := addUrlLink(t, ac, "http://x/art", pin)
	link2 := addUrlLink(t, ac, "http://mirror/art", pin)

	st, _ := runCheck(ac, true /*fix*/)
	if st.CorruptEntries != 1 || st.RemovedEntries != 3 {
		t.Errorf("CheckStats = %+v, want 1 corrupt entry and 3 removals (entry + 2 url links)", st)
	}
	assertGone(t, ac, pin, link1, link2)
	if !ac.Contains(good) || !ac.isUrlPinned("http://x/good", good) {
		t.Error("fix must leave the healthy entry and its url link alone")
	}
	if st, report := runCheck(ac, false /*fix*/); st.HasProblems() {
		t.Errorf("cache must be clean after fix, got %+v:\n%s", st, report)
	}
}

// Contains follows links, so an F-named symlink would serve another entry's bytes under its own pin.
func TestCheck_EntryNamedSymlinkIsCorrupt(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	ac := makeCacheDir(t)
	real := addEntry(t, ac, []byte("real"))
	alias := pinOf([]byte("alias"))
	c.Check(os.Symlink(real, filepath.Join(ac.Dir(), alias)))
	viaAlias := addUrlLink(t, ac, "http://x/alias", alias)

	st, report := runCheck(ac, true /*fix*/)
	if st.CorruptEntries != 1 || st.BadLinks != 1 || st.CheckedEntries != 2 || st.RemovedEntries != 2 {
		t.Errorf("CheckStats = %+v, want the alias corrupt and the url link through it bad:\n%s", st, report)
	}
	if !strings.Contains(report, "CORRUPT: '"+ac.EntryPath(alias)+"' is a symlink, not a regular file") {
		t.Errorf("report must flag the alias, got:\n%s", report)
	}
	assertGone(t, ac, alias, viaAlias)
	if !ac.Contains(real) {
		t.Error("removing the F-named symlink must not touch its referent")
	}
}

func TestCheck_DanglingUrlLink(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	ac := makeCacheDir(t)
	dangling := addUrlLink(t, ac, "http://x/gone", pinOf([]byte("gone")))

	st, report := runCheck(ac, false /*fix*/)
	if st.BadLinks != 1 || st.CheckedLinks != 1 || !st.HasProblems() {
		t.Errorf("CheckStats = %+v, want 1 bad link", st)
	}
	if !strings.Contains(report, "BAD LINK: '"+ac.EntryPath(dangling)+"' target '"+pinOf([]byte("gone"))+"' does not exist") {
		t.Errorf("report must name the dangling url link, got:\n%s", report)
	}

	st, _ = runCheck(ac, true /*fix*/)
	if st.RemovedEntries != 1 {
		t.Errorf("CheckStats = %+v, want the bad link removed", st)
	}
	assertGone(t, ac, dangling)
}

func TestCheck_UnexpectedEntries(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	outside := filepath.Join(t.TempDir(), "outside.txt")
	c.Check(os.WriteFile(outside, []byte("x"), 0o644))
	foreign := []string{"notes.txt", "Fnot-a-hash", "sub", pinOf([]byte("dir")), "foreign-link"}
	ac := makeCacheDir(t, "notes.txt", "Fnot-a-hash", "sub/", pinOf([]byte("dir"))+"/")
	c.Check(os.WriteFile(filepath.Join(ac.Dir(), "sub", "inner.txt"), []byte("x"), 0o644))
	c.Check(os.Symlink(outside, filepath.Join(ac.Dir(), "foreign-link")))

	st, report := runCheck(ac, false /*fix*/)
	if want := (CheckStats{UnexpectedEntries: len(foreign)}); st != want || !st.HasProblems() {
		t.Errorf("CheckStats = %+v, want %+v:\n%s", st, want, report)
	}
	for _, name := range foreign {
		if !strings.Contains(report, "UNEXPECTED ENTRY: '"+ac.EntryPath(name)+"'") {
			t.Errorf("report must name '%s', got:\n%s", name, report)
		}
	}

	st, _ = runCheck(ac, true /*fix*/)
	if st.RemovedEntries != len(foreign) {
		t.Errorf("CheckStats = %+v, want all %d removed", st, len(foreign))
	}
	assertGone(t, ac, foreign...)
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("removing a foreign symlink must not touch its referent: %v", err)
	}
}

func stagingFiles(t *testing.T, ac *Cache) []string {
	t.Helper()
	stagingFile := fetchPrefix + "12345"
	stagingLink := urlLinkPrefix + urlLinkName("http://x/art") + "-1-1"
	c.Check(os.WriteFile(filepath.Join(ac.Dir(), stagingFile), []byte("partial"), 0o644))
	c.Check(os.Symlink(pinOf([]byte("art")), filepath.Join(ac.Dir(), stagingLink)))
	return []string{stagingFile, stagingLink}
}

// A young staging file may be another process's download in flight.
func TestCheck_YoungStagingFilesAreIgnored(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	ac := makeCacheDir(t)
	staging := stagingFiles(t, ac)

	st, report := runCheck(ac, true /*fix*/)
	if st != (CheckStats{}) || report != "" {
		t.Errorf("young staging files must be invisible to check, got %+v:\n%s", st, report)
	}
	for _, name := range staging {
		if _, err := os.Lstat(filepath.Join(ac.Dir(), name)); err != nil {
			t.Errorf("fix must leave young '%s' alone, Lstat err = %v", name, err)
		}
	}
}

func TestCheck_OldStagingLeftovers(t *testing.T) {
	skipIfNoOSSymlinks(t, t.TempDir())
	// os.Chtimes follows links, so a url- staging symlink can't be aged on disk.
	prev := stagingGracePeriod
	stagingGracePeriod = -time.Hour
	defer func() { stagingGracePeriod = prev }()
	ac := makeCacheDir(t)
	staging := stagingFiles(t, ac)

	st, report := runCheck(ac, false /*fix*/)
	if st.StagingLeftovers != 2 || st.RemovedEntries != 0 || !st.HasProblems() {
		t.Errorf("CheckStats = %+v, want 2 staging leftovers, nothing removed:\n%s", st, report)
	}
	for _, name := range staging {
		if !strings.Contains(report, "STAGING LEFTOVER: '"+ac.EntryPath(name)+"'") {
			t.Errorf("report must name '%s', got:\n%s", name, report)
		}
	}

	st, _ = runCheck(ac, true /*fix*/)
	if st.RemovedEntries != 2 {
		t.Errorf("CheckStats = %+v, want both leftovers removed", st)
	}
	assertGone(t, ac, staging...)
}

