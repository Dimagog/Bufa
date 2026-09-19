package Store

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/Hashing"
	c "github.com/dimagog/bufa/internal/contract"
)

func runCheck(s *Store, fix bool, allowedFiles ...string) (CheckStats, string) {
	// Check asserts the caller set safe hashing; restore it for the rest of the package.
	prev := Hashing.SafeHashing
	Hashing.SafeHashing = true
	defer func() { Hashing.SafeHashing = prev }()
	var buf bytes.Buffer
	st := s.Check(&buf, fix, allowedFiles...)
	return st, buf.String()
}

func TestCheck_CleanStore(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	inKey := s.Store(InRoot, src, "u", false, nil, true)
	s.LinkPath(InRoot, "u", inKey)

	bld := vfs.NewMemMapFs()
	writeFile(t, bld, filepath.Join("u", "out.txt"), []byte("OUT"))
	outKey := s.Store(OutRoot, bld, "u", false, nil, true)
	s.Link(OutRoot, buildKey("combined"), outKey)
	s.LinkPath(OutRoot, "u", buildKey("combined"))

	st, report := runCheck(s, false /*fix*/)
	if want := (CheckStats{CheckedContent: 2, CheckedLinks: 3}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if report != "" {
		t.Errorf("clean store must report no problems, got:\n%s", report)
	}
}

func TestCheck_CorruptInContentNamesSourceDir(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	key := s.Store(InRoot, src, "u", false, nil, true)
	s.LinkPath(InRoot, "a/b", key)

	writeFile(t, s.fs, filepath.Join(InRoot, key, "a.txt"), []byte("TAMPERED"))

	st, report := runCheck(s, false /*fix*/)
	if st.CorruptContent != 1 || !st.HasProblems() {
		t.Errorf("CheckStats = %+v, want 1 corrupt content dir", st)
	}
	if !strings.Contains(report, "CORRUPT: 'in/"+key+"'") {
		t.Errorf("report must name the corrupt entry, got:\n%s", report)
	}
	if !strings.Contains(report, "(source dirs: 'a/b')") {
		t.Errorf("report must correlate to source dir a/b, got:\n%s", report)
	}
}

func TestCheck_CorruptOutCorrelatesThroughBuildLink(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	bld := vfs.NewMemMapFs()
	writeFile(t, bld, filepath.Join("u", "out.txt"), []byte("OUT"))
	key := s.Store(OutRoot, bld, "u", false, nil, true)
	s.Link(OutRoot, buildKey("combined"), key)
	s.LinkPath(OutRoot, "pkg/app", buildKey("combined"))

	writeFile(t, s.fs, filepath.Join(OutRoot, key, "out.txt"), []byte("TAMPERED"))

	st, report := runCheck(s, false /*fix*/)
	if st.CorruptContent != 1 {
		t.Errorf("CheckStats = %+v, want 1 corrupt content dir", st)
	}
	if !strings.Contains(report, "(source dirs: 'pkg/app')") {
		t.Errorf("report must correlate through the B link to pkg/app, got:\n%s", report)
	}
}

func TestCheck_CorruptDanglingContent(t *testing.T) {
	s := NewStore(vfs.NewMemMapFs())

	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	key := s.Store(InRoot, src, "u", false, nil, true)

	writeFile(t, s.fs, filepath.Join(InRoot, key, "a.txt"), []byte("TAMPERED"))

	st, report := runCheck(s, false /*fix*/)
	if st.CorruptContent != 1 {
		t.Errorf("CheckStats = %+v, want 1 corrupt content dir", st)
	}
	if !strings.Contains(report, "(dangling: no path link references it)") {
		t.Errorf("unreferenced corrupt entry must report as dangling, got:\n%s", report)
	}
}

func TestCheck_DanglingBuildLink(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	target := dirKey("target")
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, target), 0o755))
	s.Link(OutRoot, buildKey("combined"), target)
	c.Check(s.fs.RemoveAll(filepath.Join(OutRoot, target)))

	st, report := runCheck(s, false /*fix*/)
	if want := (CheckStats{CheckedLinks: 1, BadLinks: 1}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "target '"+target+"' does not exist") {
		t.Errorf("dangling B link must be reported, got:\n%s", report)
	}
}

func TestCheck_PathEntryNotSymlink(t *testing.T) {
	s, _ := osStore(t) // real fs, but no symlink creation needed

	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, "∕u"), 0o755))

	st, report := runCheck(s, false /*fix*/)
	if want := (CheckStats{CheckedLinks: 1, UnexpectedEntries: 1}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "UNEXPECTED ENTRY: 'in/∕u' is not a symlink") {
		t.Errorf("non-symlink ∕ entry must be reported as unexpected, got:\n%s", report)
	}
}

func TestCheck_WrongKindTarget(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	// The empty-dir key keeps the D itself valid, so only the link misfiles.
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, Hashing.EmptyDirHash), 0o755))
	s.LinkPath(OutRoot, "u", Hashing.EmptyDirHash)

	st, report := runCheck(s, false /*fix*/)
	if want := (CheckStats{CheckedContent: 1, CheckedLinks: 1, BadLinks: 1}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "is not a 'B' entry") {
		t.Errorf("out/∕ targeting content directly must be reported, got:\n%s", report)
	}
}

func TestCheck_FileSquattingOnContentKey(t *testing.T) {
	s := NewStore(vfs.NewMemMapFs())

	key := dirKey("squat")
	writeFile(t, s.fs, filepath.Join(InRoot, key), []byte("file"))

	st, report := runCheck(s, false /*fix*/)
	if want := (CheckStats{CheckedContent: 1, CorruptContent: 1}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "'in/"+key+"' is not a directory") {
		t.Errorf("file squatting on a D key must be reported, got:\n%s", report)
	}
}

// Content is recognized by the full key shape, as in gc: a D-prefixed non-key is foreign, not corrupt.
func TestCheck_UnexpectedEntries(t *testing.T) {
	s := NewStore(vfs.NewMemMapFs())

	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, "foo"), 0o755))
	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, "Bnope"), 0o755))
	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, "Dnotakey"), 0o755))

	st, report := runCheck(s, false /*fix*/)
	if want := (CheckStats{UnexpectedEntries: 3}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "UNEXPECTED ENTRY: 'in/foo'") ||
		!strings.Contains(report, "UNEXPECTED ENTRY: 'in/Bnope'") ||
		!strings.Contains(report, "UNEXPECTED ENTRY: 'in/Dnotakey'") {
		t.Errorf("unexpected entries must be reported, got:\n%s", report)
	}
}

func TestCheckFix_RemovesCorruptInContentAndLink(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	key := s.Store(InRoot, src, "u", false, nil, true)
	s.LinkPath(InRoot, "u", key)

	writeFile(t, s.fs, filepath.Join(InRoot, key, "a.txt"), []byte("TAMPERED"))

	st, report := runCheck(s, true /*fix*/)
	if st.CorruptContent != 1 || st.RemovedEntries != 2 {
		t.Errorf("CheckStats = %+v, want 1 corrupt, 2 removed (content + ∕ link)", st)
	}
	if !strings.Contains(report, "FIXED: removed link 'in/∕u'") ||
		!strings.Contains(report, "FIXED: removed 'in/"+key+"'") {
		t.Errorf("fix must report each removal, got:\n%s", report)
	}
	if exists(t, s.fs, filepath.Join(InRoot, key)) {
		t.Error("corrupt in/ content must be removed")
	}
	if s.GetLinkTarget(InRoot, "∕u") != "" {
		t.Error("in/∕u link to corrupt content must be removed")
	}
	if st, _ := runCheck(s, false /*fix*/); st != (CheckStats{}) {
		t.Errorf("re-check after fix = %+v, want a clean empty store", st)
	}
}

func TestCheckFix_RemovesCorruptOutChain(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	bld := vfs.NewMemMapFs()
	writeFile(t, bld, filepath.Join("u", "out.txt"), []byte("OUT"))
	key := s.Store(OutRoot, bld, "u", false, nil, true)
	bKey := buildKey("combined")
	s.Link(OutRoot, bKey, key)
	s.LinkPath(OutRoot, "u", bKey)

	writeFile(t, s.fs, filepath.Join(OutRoot, key, "out.txt"), []byte("TAMPERED"))

	st, report := runCheck(s, true /*fix*/)
	if st.CorruptContent != 1 || st.RemovedEntries != 3 {
		t.Errorf("CheckStats = %+v, want 1 corrupt, 3 removed (content + B + ∕)", st)
	}
	if !strings.Contains(report, "FIXED: removed link 'out/∕u'") ||
		!strings.Contains(report, "FIXED: removed link 'out/"+bKey+"'") ||
		!strings.Contains(report, "FIXED: removed 'out/"+key+"'") {
		t.Errorf("fix must remove the whole ∕ -> B -> D chain, got:\n%s", report)
	}
	if exists(t, s.fs, filepath.Join(OutRoot, key)) ||
		s.GetLinkTarget(OutRoot, bKey) != "" || s.GetLinkTarget(OutRoot, "∕u") != "" {
		t.Error("corrupt out/ content and both referencing links must be removed")
	}
	if st, _ := runCheck(s, false /*fix*/); st != (CheckStats{}) {
		t.Errorf("re-check after fix = %+v, want a clean empty store", st)
	}
}

func TestCheckFix_RemovesSquattingFile(t *testing.T) {
	s := NewStore(vfs.NewMemMapFs())

	key := dirKey("squat")
	writeFile(t, s.fs, filepath.Join(InRoot, key), []byte("file"))

	st, report := runCheck(s, true /*fix*/)
	if st.CorruptContent != 1 || st.RemovedEntries != 1 {
		t.Errorf("CheckStats = %+v, want 1 corrupt, 1 removed", st)
	}
	if !strings.Contains(report, "FIXED: removed 'in/"+key+"'") {
		t.Errorf("fix must remove the squatting file, got:\n%s", report)
	}
	if exists(t, s.fs, filepath.Join(InRoot, key)) {
		t.Error("squatting file must be removed")
	}
}

// A dangling B would otherwise serve a phantom cache hit at missing content.
func TestCheckFix_RemovesBadLinkLeavesValidContent(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	key := s.Store(InRoot, src, "u", false, nil, true)
	s.LinkPath(InRoot, "u", key)

	gone := dirKey("gone")
	bDangling := buildKey("dangling")
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, gone), 0o755))
	s.Link(OutRoot, bDangling, gone)
	c.Check(s.fs.RemoveAll(filepath.Join(OutRoot, gone)))

	st, report := runCheck(s, true /*fix*/)
	if want := (CheckStats{CheckedContent: 1, CheckedLinks: 2, BadLinks: 1, RemovedEntries: 1}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "FIXED: removed bad link 'out/"+bDangling+"'") {
		t.Errorf("fix must remove the dangling B link, got:\n%s", report)
	}
	if !s.Contains(InRoot, key) || s.GetLinkTarget(InRoot, "∕u") != key {
		t.Error("valid in/ content and its link must survive fix")
	}
	if s.GetLinkTarget(OutRoot, bDangling) != "" {
		t.Error("dangling B link must be removed")
	}
	if st, _ := runCheck(s, false /*fix*/); st != (CheckStats{CheckedContent: 1, CheckedLinks: 1}) {
		t.Errorf("re-check after fix = %+v, want a clean store", st)
	}
}

// Deleting the bad B makes the ∕ rooted at it dangling; the ∕ tier sees that in the same run.
func TestCheckFix_RemovesDanglingBuildLinkChain(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	target := dirKey("target")
	bKey := buildKey("combined")
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, target), 0o755))
	s.Link(OutRoot, bKey, target)
	s.LinkPath(OutRoot, "u", bKey)
	c.Check(s.fs.RemoveAll(filepath.Join(OutRoot, target)))

	st, report := runCheck(s, true /*fix*/)
	if want := (CheckStats{CheckedLinks: 2, BadLinks: 2, RemovedEntries: 2}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "FIXED: removed bad link 'out/"+bKey+"'") ||
		!strings.Contains(report, "FIXED: removed bad link 'out/∕u'") {
		t.Errorf("fix must remove the whole dangling chain, got:\n%s", report)
	}
	if st, _ := runCheck(s, false /*fix*/); st != (CheckStats{}) {
		t.Errorf("re-check after fix = %+v, want a clean empty store", st)
	}
}

// One policy for every root: fix removes whatever check reports, foreign entries included.
func TestCheckFix_RemovesUnexpectedEntries(t *testing.T) {
	s, _ := osStore(t)

	c.Check(s.fs.MkdirAll(filepath.Join(InRoot, "∕u", "nested"), 0o755))
	writeFile(t, s.fs, filepath.Join(OutRoot, "README"), []byte("foreign"))

	st, report := runCheck(s, true /*fix*/)
	if want := (CheckStats{CheckedLinks: 1, UnexpectedEntries: 2, RemovedEntries: 2}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "UNEXPECTED ENTRY: 'in/∕u' is not a symlink") ||
		!strings.Contains(report, "FIXED: removed unexpected entry 'in/∕u'") ||
		!strings.Contains(report, "UNEXPECTED ENTRY: 'out/README'") ||
		!strings.Contains(report, "FIXED: removed unexpected entry 'out/README'") {
		t.Errorf("fix must report and remove each unexpected entry, got:\n%s", report)
	}
	if exists(t, s.fs, filepath.Join(InRoot, "∕u")) || exists(t, s.fs, filepath.Join(OutRoot, "README")) {
		t.Error("fix must delete unexpected entries, dirs whole")
	}
	if st, _ := runCheck(s, false /*fix*/); st != (CheckStats{}) {
		t.Errorf("re-check after fix = %+v, want a clean empty store", st)
	}
}

// A ∕ rooted at a squatting non-symlink B is dangling once fix removes the squatter.
func TestCheckFix_RemovesPathLinkAtRemovedSquatter(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	bSquat := buildKey("squat")
	c.Check(s.fs.MkdirAll(filepath.Join(OutRoot, bSquat), 0o755))
	s.LinkPath(OutRoot, "u", bSquat)

	st, report := runCheck(s, true /*fix*/)
	if want := (CheckStats{CheckedLinks: 2, BadLinks: 1, UnexpectedEntries: 1, RemovedEntries: 2}); st != want {
		t.Errorf("CheckStats = %+v, want %+v", st, want)
	}
	if !strings.Contains(report, "FIXED: removed unexpected entry 'out/"+bSquat+"'") ||
		!strings.Contains(report, "FIXED: removed bad link 'out/∕u'") {
		t.Errorf("fix must remove the squatter and the ∕ rooted at it, got:\n%s", report)
	}
	if st, _ := runCheck(s, false /*fix*/); st != (CheckStats{}) {
		t.Errorf("re-check after fix = %+v, want a clean empty store", st)
	}
}

// A lying hash-named link folds to the very key its tree was stored under, so Check must force
// SafeHashing and stream the referent.
func TestCheck_LyingHashNamedLinkIsCorrupt(t *testing.T) {
	s, root := osStoreAt(t)
	skipIfNoSymlinks(t, s)

	fake := "F" + strings.Repeat("a", 52)
	writeFile(t, s.fs, filepath.Join("blob", fake), []byte("not-those-bytes"))
	writeFile(t, s.fs, filepath.Join("stage", "keep.txt"), []byte("K"))
	osSymlink(filepath.Join(root, "blob", fake), filepath.Join(root, "stage", "lying"))

	key := s.MoveStore(OutRoot, "stage", PublishPlan{AllowLinks: true})
	if Hashing.HashDir(s.fs, filepath.Join(OutRoot, key)) != key {
		t.Fatal("precondition: the default (trusting) walk must reproduce the key")
	}

	st, report := runCheck(s, false /*fix*/)
	if st.CorruptContent != 1 {
		t.Errorf("CheckStats = %+v, want the lying link surfaced as 1 corrupt entry", st)
	}
	if !strings.Contains(report, "CORRUPT: 'out/"+key+"'") {
		t.Errorf("report must name the corrupt entry, got:\n%s", report)
	}
}

func TestCheck_EmptyStoreNoop(t *testing.T) {
	s := NewStore(vfs.NewMemMapFs())

	st, report := runCheck(s, false /*fix*/)
	if st != (CheckStats{}) || st.HasProblems() {
		t.Errorf("Check on empty store = %+v, want zero stats", st)
	}
	if report != "" {
		t.Errorf("empty store must report no problems, got:\n%s", report)
	}
}

// The build root itself is bufa's namespace: the same ownership rule Nuke refuses on, check reports and fix removes.
func TestCheck_TopLevelForeignEntries(t *testing.T) {
	s, _ := osStore(t)
	skipIfNoSymlinks(t, s)

	src := vfs.NewMemMapFs()
	writeFile(t, src, filepath.Join("u", "a.txt"), []byte("A"))
	s.LinkPath(InRoot, "u", s.Store(InRoot, src, "u", false, nil, true))
	c.Check(s.fs.MkdirAll("BLD", 0o755))               // layout root, any casing
	writeFile(t, s.fs, "extra.txt", []byte("allowed")) // allowed file, any casing
	writeFile(t, s.fs, "bufa-d.sock", []byte("sock"))

	st, report := runCheck(s, false /*fix*/, "Extra.TXT", "bufa-d.sock")
	if want := (CheckStats{CheckedContent: 1, CheckedLinks: 1}); st != want {
		t.Errorf("owned top level: CheckStats = %+v, want %+v:\n%s", st, want, report)
	}

	c.Check(s.fs.MkdirAll(filepath.Join("target", "nested"), 0o755)) // a stray tool's output dir
	writeFile(t, s.fs, "README", []byte("foreign"))
	writeFile(t, s.fs, "in.txt", []byte("not a layout root"))
	c.Check(s.fs.MkdirAll("extra.txt.d", 0o755)) // an allowed *file* name is not an allowed dir name

	st, report = runCheck(s, false /*fix*/, "Extra.TXT", "bufa-d.sock")
	if want := (CheckStats{CheckedContent: 1, CheckedLinks: 1, UnexpectedEntries: 4}); st != want {
		t.Errorf("CheckStats = %+v, want %+v:\n%s", st, want, report)
	}
	for _, line := range []string{
		"UNEXPECTED ENTRY: 'target'\n", "UNEXPECTED ENTRY: 'README'\n",
		"UNEXPECTED ENTRY: 'in.txt'\n", "UNEXPECTED ENTRY: 'extra.txt.d'\n",
	} {
		if !strings.Contains(report, line) {
			t.Errorf("report must contain %q, got:\n%s", line, report)
		}
	}
	if st, _ := runCheck(s, false /*fix*/); st.UnexpectedEntries != 6 {
		t.Errorf("without an allowlist the extra file and sock are foreign too, got %+v", st)
	}

	st, report = runCheck(s, true /*fix*/, "Extra.TXT", "bufa-d.sock")
	if st.UnexpectedEntries != 4 || st.RemovedEntries != 4 || !strings.Contains(report, "FIXED: removed unexpected entry 'target'") {
		t.Errorf("fix must remove all 4 foreign entries, CheckStats = %+v:\n%s", st, report)
	}
	for _, name := range []string{"target", "README", "in.txt", "extra.txt.d"} {
		if exists(t, s.fs, name) {
			t.Errorf("fix must delete top-level %q (dirs whole)", name)
		}
	}
	for _, name := range []string{"BLD", "extra.txt", "bufa-d.sock", InRoot} {
		if !exists(t, s.fs, name) {
			t.Errorf("fix must keep owned entry %q", name)
		}
	}
	if st, _ := runCheck(s, false /*fix*/, "Extra.TXT", "bufa-d.sock"); st.HasProblems() {
		t.Errorf("re-check after fix = %+v, want clean", st)
	}
}
