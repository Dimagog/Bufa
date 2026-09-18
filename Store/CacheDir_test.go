package Store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dimagog/bufa/Hashing"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

func TestCacheDirName_MatchesLinkNameEncoding(t *testing.T) {
	// OS-separator and slash spellings of one path must agree, and so must the ∕ link's decode.
	for _, path := range []string{filepath.Join("A", "b"), "A/b", "."} {
		name := cacheDirName(path)
		if !Hashing.IsValidPathHash(name) {
			t.Errorf("cacheDirName(%q) = %q, want a P path hash", path, name)
		}
		if got := cacheDirName(pathFromLinkName(pathToLinkName(path))); got != name {
			t.Errorf("cacheDirName through the ∕ round trip = %q, want %q", got, name)
		}
	}
	if cacheDirName("A/b") != cacheDirName(filepath.Join("A", "b")) {
		t.Error("cacheDirName must be separator-agnostic")
	}
	if cacheDirName("A/b") == cacheDirName("a/b") {
		t.Error("cacheDirName must preserve casing like the ∕ link name")
	}
	if strings.HasPrefix(cacheDirName("x"), PathLinkPrefix) {
		t.Error("a P name must never look like a ∕ link name")
	}
}

func TestEnsureCacheDir_CreatesPairAndIsIdempotent(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	name := s.EnsureCacheDir(filepath.Join("Build", "go"))
	if name != cacheDirName("Build/go") {
		t.Fatalf("EnsureCacheDir = %q, want %q", name, cacheDirName("Build/go"))
	}
	dir := filepath.Join(UserRoot, name)
	if !vfsx.DirExistsFailOnFile(s.fs, dir) {
		t.Fatal("cache dir must exist as a directory")
	}
	if got := s.GetLinkTarget(UserRoot, "∕Build∕go"); got != name {
		t.Errorf("user/∕Build∕go -> %q, want %q", got, name)
	}

	// Content survives a repeat ensure: bufa never wipes an entry.
	writeFile(t, s.fs, filepath.Join(dir, "go", "cache.bin"), []byte("history"))
	if again := s.EnsureCacheDir("Build/go"); again != name {
		t.Errorf("repeat EnsureCacheDir = %q, want the same %q", again, name)
	}
	if !exists(t, s.fs, filepath.Join(dir, "go", "cache.bin")) {
		t.Error("EnsureCacheDir must never touch entry content")
	}

	rootName := s.EnsureCacheDir(".")
	if got := s.GetLinkTarget(UserRoot, PathLinkPrefix); got != rootName {
		t.Errorf("root build dir's link must be the bare ∕, got target %q", got)
	}
}

func TestEnsureCacheDir_RepointsLinkRefusesSquatters(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	// A ∕ link pointing elsewhere is re-pointed: the target is a pure function of the path.
	c.Check(s.fs.MkdirAll(filepath.Join(UserRoot, "Pother"), 0o755))
	s.LinkPath(UserRoot, "u", "Pother")
	name := s.EnsureCacheDir("u")
	if got := s.GetLinkTarget(UserRoot, "∕u"); got != name {
		t.Errorf("mispointed ∕ link must be re-pointed, got %q", got)
	}

	// A file on the P… name is a squatter, never replaced.
	writeFile(t, s.fs, filepath.Join(UserRoot, cacheDirName("f")), []byte("squat"))
	err := c.Rescue(func() { s.EnsureCacheDir("f") })
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("file squatting on the P… name must be a hard error, got %v", err)
	}
	if !vfsx.RegularFileExists(s.fs, filepath.Join(UserRoot, cacheDirName("f"))) {
		t.Error("the squatter must be left in place")
	}

	// A symlink on the P… name is a squatter too, even one resolving to a directory.
	s.Link(UserRoot, cacheDirName("l"), "Pother")
	err = c.Rescue(func() { s.EnsureCacheDir("l") })
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("symlink squatting on the P… name must be a hard error, got %v", err)
	}
}

func TestEmptyCacheDirs_KeepsEntriesAndLinks(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	var report strings.Builder
	if n := s.EmptyCacheDirs(&report); n != 0 || report.Len() != 0 {
		t.Errorf("missing user/ must be a no-op, got emptied %d, report:\n%s", n, report.String())
	}

	a := filepath.Join(UserRoot, s.EnsureCacheDir("a"))
	b := filepath.Join(UserRoot, s.EnsureCacheDir("b/c"))
	writeFile(t, s.fs, filepath.Join(a, "deep", "x.bin"), []byte("x"))
	writeFile(t, s.fs, filepath.Join(a, "y.bin"), []byte("y"))
	c.Check(s.fs.Chmod(filepath.Join(a, "y.bin"), 0o444)) // Go's cache marks its files read-only
	writeFile(t, s.fs, filepath.Join("outside", "k.txt"), []byte("k"))
	s.symlinkAt("outside", filepath.Join(a, "lnk")) // a dir symlink inside an entry: removed as a link, never followed

	if n := s.EmptyCacheDirs(&report); n != 2 || report.Len() != 0 {
		t.Errorf("emptied %d entries, want 2; report must be empty, got:\n%s", n, report.String())
	}
	for _, dir := range []string{a, b} {
		if !vfsx.DirExistsFailOnFile(s.fs, dir) || !isDirEmpty(s.fs, dir) {
			t.Errorf("entry %q must survive empty", dir)
		}
	}
	if !exists(t, s.fs, filepath.Join("outside", "k.txt")) {
		t.Error("a symlink's referent outside the entry must survive")
	}
	if s.GetLinkTarget(UserRoot, "∕a") != filepath.Base(a) || s.GetLinkTarget(UserRoot, "∕b∕c") != filepath.Base(b) {
		t.Error("∕ links must survive")
	}
}

func TestEmptyCacheDirs_ReportsForeignEntriesAndProceeds(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	a := filepath.Join(UserRoot, s.EnsureCacheDir("a"))
	writeFile(t, s.fs, filepath.Join(a, "x.bin"), []byte("x"))
	writeFile(t, s.fs, filepath.Join(UserRoot, "README"), []byte("foreign"))
	c.Check(s.fs.MkdirAll(filepath.Join(UserRoot, "∕squat", "inner"), 0o755))
	writeFile(t, s.fs, filepath.Join(UserRoot, "Pfile"), []byte("squat"))

	var report strings.Builder
	n := s.EmptyCacheDirs(&report)
	if n != 1 {
		t.Errorf("emptied %d entries, want the one well-formed entry", n)
	}
	for _, name := range []string{"Pfile", "README", "∕squat"} {
		if !strings.Contains(report.String(), "UNEXPECTED ENTRY: 'user/"+name+"'") {
			t.Errorf("report must name %q, got:\n%s", name, report.String())
		}
	}
	if !strings.Contains(report.String(), "run 'bufa check'") {
		t.Errorf("report must hint at bufa check, got:\n%s", report.String())
	}
	if exists(t, s.fs, filepath.Join(a, "x.bin")) {
		t.Error("foreign entries must not stop the well-formed ones from being emptied")
	}
	for _, name := range []string{"README", filepath.Join("∕squat", "inner"), "Pfile"} {
		if !exists(t, s.fs, filepath.Join(UserRoot, name)) {
			t.Errorf("foreign entry %q must be left untouched", name)
		}
	}
}

func TestGC_UserRoot(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	kept := filepath.Join(UserRoot, s.EnsureCacheDir("kept"))
	gone := filepath.Join(UserRoot, s.EnsureCacheDir("gone"))
	writeFile(t, s.fs, filepath.Join(kept, "k.bin"), []byte("k"))
	writeFile(t, s.fs, filepath.Join(gone, "g.bin"), []byte("g"))
	orphan := filepath.Join(UserRoot, cacheDirName("orphan")) // a real entry name, but no link
	c.Check(s.fs.MkdirAll(orphan, 0o755))

	st := s.GC(func(path string) bool { return path == "kept" }, false)

	if st != (GCStats{StalePathLinks: 1, OrphanCacheDirs: 2}) {
		t.Errorf("GCStats = %+v, want 1 stale link, 2 orphan cache dirs", st)
	}
	if !exists(t, s.fs, filepath.Join(kept, "k.bin")) || s.GetLinkTarget(UserRoot, "∕kept") != filepath.Base(kept) {
		t.Error("live entry and its link must survive untouched")
	}
	if exists(t, s.fs, gone) || exists(t, s.fs, filepath.Join(UserRoot, "∕gone")) {
		t.Error("stale link and its entry must be removed")
	}
	if exists(t, s.fs, orphan) {
		t.Error("unreferenced P… dir must be removed")
	}
}

func TestCheck_UserRootClean(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	dir := filepath.Join(UserRoot, s.EnsureCacheDir("Build/go"))
	writeFile(t, s.fs, filepath.Join(dir, "anything", "at-all"), []byte("never inspected"))
	c.Check(s.fs.MkdirAll(filepath.Join(UserRoot, cacheDirName("unlinked")), 0o755)) // gc's concern, not check's

	st, report := runCheck(s, false /*fix*/)
	if want := (CheckStats{CheckedLinks: 1}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if report != "" {
		t.Errorf("well-formed user/ must report nothing, got:\n%s", report)
	}
}

func TestCheck_UserRootShape(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	writeFile(t, s.fs, filepath.Join(UserRoot, cacheDirName("file")), []byte("squat")) // P… not a dir
	writeFile(t, s.fs, filepath.Join(UserRoot, "Pjunk"), []byte("not a hash"))         // P-prefixed non-key
	c.Check(s.fs.MkdirAll(filepath.Join(UserRoot, "∕squat"), 0o755))                   // ∕ not a symlink
	writeFile(t, s.fs, filepath.Join(UserRoot, "README"), []byte("foreign"))           // other name
	c.Check(s.fs.MkdirAll(filepath.Join(UserRoot, cacheDirName("wrong")), 0o755))      // present but…
	s.LinkPath(UserRoot, "mis", cacheDirName("wrong"))                                 // …not this link's own P
	dangling := filepath.Join(UserRoot, s.EnsureCacheDir("dangling"))                  // own P…
	c.Check(s.fs.RemoveAll(dangling))                                                  // …then gone

	st, report := runCheck(s, false /*fix*/)
	if want := (CheckStats{CheckedLinks: 3, BadLinks: 2, UnexpectedEntries: 4}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	for _, line := range []string{
		"UNEXPECTED ENTRY: 'user/" + cacheDirName("file") + "' is not a directory",
		"UNEXPECTED ENTRY: 'user/Pjunk'\n",
		"UNEXPECTED ENTRY: 'user/∕squat' is not a symlink",
		"UNEXPECTED ENTRY: 'user/README'",
		"BAD LINK: 'user/∕mis' target '" + cacheDirName("wrong") + "' is not the cache dir of 'mis'",
		"BAD LINK: 'user/∕dangling' target '" + cacheDirName("dangling") + "' does not exist",
	} {
		if !strings.Contains(report, line) {
			t.Errorf("report must contain %q, got:\n%s", line, report)
		}
	}

	st, report = runCheck(s, true /*fix*/)
	if st.RemovedEntries != 6 {
		t.Errorf("fix must remove all 6 offenders, CheckStats = %+v:\n%s", st, report)
	}
	if !exists(t, s.fs, filepath.Join(UserRoot, cacheDirName("wrong"))) {
		t.Error("an unlinked P… dir is not check's concern and must survive fix")
	}
	if st, _ := runCheck(s, false /*fix*/); st != (CheckStats{}) {
		t.Errorf("re-check after fix = %+v, want clean", st)
	}
}
