package Hashing

import (
	"context"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	vfs "github.com/spf13/afero"
	"lukechampine.com/blake3"

	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

var testEnc = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func assertEq(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %q, want %q", what, got, want)
	}
}

func writeFile(t *testing.T, fsys vfs.Fs, path string, data []byte) {
	t.Helper()
	c.Check(fsys.MkdirAll(filepath.Dir(path), 0o755))
	c.Check(vfs.WriteFile(fsys, path, data, 0o644))
}

// blake3 of empty input — canonical vector from the BLAKE3 spec test suite.
const emptyBlake3Hex = "af1349b9f5f9a1a6a0404dea36dcc9499bcb25c9adc112b7cc9a93cae41f3262"

func TestHashFile_EmptyFileKnownVector(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, "empty", nil)

	digest, err := hex.DecodeString(emptyBlake3Hex)
	if err != nil {
		t.Fatalf("decode vector: %v", err)
	}
	want := FilePrefix + testEnc.EncodeToString(digest)
	assertEq(t, "HashFile(empty)", HashFile(fsys, "empty"), want)
}

// EmptyDirHash must stay in lockstep with the algorithm; pin it three independent ways.
func TestEmptyDirHash_Constant(t *testing.T) {
	// 1. Matches the value the algorithm produces for a manifest with no entries.
	assertEq(t, "EmptyDirHash recompute", DirPrefix+CombineHashes([]NamedEntry(nil)), EmptyDirHash)

	// 2. Independent vector: the canonical BLAKE3 empty digest with the "D" prefix.
	digest, err := hex.DecodeString(emptyBlake3Hex)
	if err != nil {
		t.Fatalf("decode vector: %v", err)
	}
	assertEq(t, "EmptyDirHash vector", EmptyDirHash, DirPrefix+testEnc.EncodeToString(digest))

	// 3. Equals HashDir of an actually-empty directory.
	fsys := vfs.NewMemMapFs()
	c.Check(fsys.MkdirAll("empty", 0o755))
	assertEq(t, "HashDir(empty dir)", HashDir(fsys, "empty"), EmptyDirHash)
}

func TestHashFile_MatchesDirectBlake3(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	data := []byte("hello blake3\n")
	writeFile(t, fsys, "f.txt", data)

	sum := blake3.Sum256(data)
	want := FilePrefix + testEnc.EncodeToString(sum[:])
	assertEq(t, "HashFile(f.txt)", HashFile(fsys, "f.txt"), want)
}

func TestHashDir_ManifestComposition(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("d", "b.txt"), []byte("BBB"))
	writeFile(t, fsys, filepath.Join("d", "a.txt"), []byte("A"))

	hA := HashFile(fsys, filepath.Join("d", "a.txt"))
	hB := HashFile(fsys, filepath.Join("d", "b.txt"))
	manifest := hA + "\t" + "a.txt" + "\n" + hB + "\t" + "b.txt" + "\n"
	sum := blake3.Sum256([]byte(manifest))
	want := DirPrefix + testEnc.EncodeToString(sum[:])
	assertEq(t, "HashDir(d)", HashDir(fsys, "d"), want)
}

func TestHashDir_NestedRecursion(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	nested := filepath.Join("root", "sub", "f.txt")
	writeFile(t, fsys, nested, []byte("v1"))
	h1 := Hash(fsys, "root")

	writeFile(t, fsys, nested, []byte("v2"))
	h2 := Hash(fsys, "root")

	if h1[:1] != DirPrefix || h2[:1] != DirPrefix {
		t.Fatalf("dir hashes must start with %q: %q %q", DirPrefix, h1, h2)
	}
	if h1 == h2 {
		t.Errorf("Hash(root) unchanged after nested file edit: %q", h1)
	}
}

func TestHashDirFiltered_NilEqualsHashDir(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("d", "a.txt"), []byte("A"))
	writeFile(t, fsys, filepath.Join("d", "sub", "b.txt"), []byte("B"))

	assertEq(t, "HashDirFiltered(nil)", HashDirFiltered(fsys, "d", nil), HashDir(fsys, "d"))
}

func TestHashDirFiltered_PrunesSubtreeAndIsRootBlind(t *testing.T) {
	full := vfs.NewMemMapFs()
	writeFile(t, full, filepath.Join("d", "a.txt"), []byte("A"))
	writeFile(t, full, filepath.Join("d", "keep", "x.txt"), []byte("X"))
	writeFile(t, full, filepath.Join("d", "skipme", "y.txt"), []byte("Y"))

	want := vfs.NewMemMapFs()
	writeFile(t, want, filepath.Join("d", "a.txt"), []byte("A"))
	writeFile(t, want, filepath.Join("d", "keep", "x.txt"), []byte("X"))

	skip := func(_ vfs.Fs, path string, info fs.FileInfo) bool {
		return info.IsDir() && filepath.Base(path) == "skipme"
	}
	got := HashDirFiltered(full, "d", skip)
	assertEq(t, "filtered == tree-without-skipme", got, HashDir(want, "d"))

	// Root is never offered to skip: a skip-everything predicate still hashes "d" as empty.
	skipAllDirs := func(_ vfs.Fs, _ string, info fs.FileInfo) bool { return info.IsDir() }
	if got2 := HashDirFiltered(full, "d", skipAllDirs); got2[:1] != DirPrefix {
		t.Errorf("root must still be hashed as a dir: %q", got2)
	}
}

func TestHash_DispatchesFileVsDir(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("d", "x.txt"), []byte("x"))

	assertEq(t, "Hash(file)", Hash(fsys, filepath.Join("d", "x.txt")), HashFile(fsys, filepath.Join("d", "x.txt")))
	assertEq(t, "Hash(dir)", Hash(fsys, "d"), HashDir(fsys, "d"))
}

func TestHash_MissingPathPanics(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	err := c.Rescue(func() { Hash(fsys, "nope") })
	if err == nil {
		t.Error("Hash on missing path: want error, got nil")
	}
}

type failOpenFs struct {
	vfs.Fs
	failName string
}

func (f failOpenFs) Open(name string) (vfs.File, error) {
	if filepath.Base(name) == f.failName {
		return nil, fs.ErrPermission
	}
	return f.Fs.Open(name)
}

// A file-hash failure inside the parallel walk must surface as a rescuable panic on the caller's
// stack; an unrescued goroutine panic would crash the process past every Catch/Rescue.
func TestHashDir_FileFailureIsRescuable(t *testing.T) {
	mem := vfs.NewMemMapFs()
	writeFile(t, mem, "root/a.txt", []byte("a"))
	writeFile(t, mem, "root/bad.txt", []byte("b"))
	writeFile(t, mem, "root/sub/c.txt", []byte("c"))
	fsys := failOpenFs{mem, "bad.txt"}
	err := c.Rescue(func() { HashDir(fsys, "root") })
	if err == nil || !strings.Contains(err.Error(), "bad.txt") {
		t.Errorf("HashDir with an unreadable file: want error naming bad.txt, got %v", err)
	}
}

// A walk-loop panic (here from dirSkipFn, after the files' goroutines are launched) must still
// reap them: unreaped, the collector goroutine leaks.
func TestHashDirFiltered_LoopPanicReapsInFlightHashes(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, "root/a.txt", []byte("a"))
	writeFile(t, fsys, "root/b.txt", []byte("b"))
	writeFile(t, fsys, "root/zdir/c.txt", []byte("c"))
	boom := func(_ vfs.Fs, path string, info fs.FileInfo) bool {
		c.Require(!info.IsDir(), "boom at %s", path)
		return false
	}
	before := runtime.NumGoroutine()
	err := c.Rescue(func() { HashDirFiltered(fsys, "root", boom) })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want the skip-fn panic as the error, got %v", err)
	}
	for deadline := time.Now().Add(time.Second); runtime.NumGoroutine() > before && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("goroutines after the failed walk = %d, want <= %d: collector or hashers leaked", n, before)
	}
}

type kv struct{ k, v string }

func (a kv) String() string   { return a.k + "=" + a.v + "\n" }
func (a kv) Compare(b kv) int { return strings.Compare(a.k, b.k) }

func combineWant(items []kv) string {
	s := slices.Clone(items)
	slices.SortFunc(s, func(a, b kv) int { return a.Compare(b) })
	var sb strings.Builder
	for _, it := range s {
		sb.WriteString(it.String())
	}
	sum := blake3.Sum256([]byte(sb.String()))
	return testEnc.EncodeToString(sum[:])
}

func TestCombineHashes_MatchesManifestAndIsDeterministic(t *testing.T) {
	items := []kv{{"b", "2"}, {"a", "1"}, {"c", "3"}}
	want := combineWant(items)
	assertEq(t, "CombineHashes", CombineHashes(items), want)
	assertEq(t, "CombineHashes (repeat)", CombineHashes(items), want)
}

func TestCombineHashes_OrderIndependent(t *testing.T) {
	a := []kv{{"x", "1"}, {"y", "2"}, {"z", "3"}}
	b := []kv{{"z", "3"}, {"x", "1"}, {"y", "2"}}
	if CombineHashes(a) != CombineHashes(b) {
		t.Errorf("CombineHashes order-dependent: %q vs %q", CombineHashes(a), CombineHashes(b))
	}
	assertEq(t, "CombineHashes(shuffled)", CombineHashes(b), combineWant(a))
}

func TestCombineHashes_SensitiveToAnyMember(t *testing.T) {
	base := []kv{{"a", "1"}, {"b", "2"}}
	changed := []kv{{"a", "1"}, {"b", "2x"}}
	if CombineHashes(base) == CombineHashes(changed) {
		t.Errorf("CombineHashes unchanged after member edit: %q", CombineHashes(base))
	}
}

func TestCombineHashes_NoKindPrefix(t *testing.T) {
	got := CombineHashes([]kv{{"a", "1"}, {"b", "2"}})
	if strings.HasPrefix(got, FilePrefix) || strings.HasPrefix(got, DirPrefix) {
		t.Errorf("CombineHashes must carry no kind prefix: %q", got)
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	for _, r := range got {
		if !strings.ContainsRune(alphabet, r) {
			t.Errorf("CombineHashes = %q has non-base32 char %q", got, r)
			break
		}
	}
}

func TestCombineHashes_DoesNotMutateButInPlaceSorts(t *testing.T) {
	orig := []kv{{"c", "3"}, {"a", "1"}, {"b", "2"}}

	keep := slices.Clone(orig)
	cloned := CombineHashes(orig)
	if !slices.Equal(orig, keep) {
		t.Errorf("CombineHashes reordered caller slice: got %v, want %v", orig, keep)
	}

	inPlace := CombineHashesInPlace(orig)
	if !slices.IsSortedFunc(orig, func(a, b kv) int { return a.Compare(b) }) {
		t.Errorf("CombineHashesInPlace did not sort caller slice: %v", orig)
	}
	if cloned != inPlace {
		t.Errorf("CombineHashes (%q) and CombineHashesInPlace (%q) disagree", cloned, inPlace)
	}
}

func TestNamedEntry_CompareTieBreaksOnHash(t *testing.T) {
	a := NamedEntry{Name: "same", Hash: "Aaa"}
	b := NamedEntry{Name: "same", Hash: "Bbb"}
	if a.Compare(b) >= 0 || b.Compare(a) <= 0 {
		t.Errorf("equal Name must tie-break on Hash: a.Compare(b)=%d b.Compare(a)=%d",
			a.Compare(b), b.Compare(a))
	}
	if a.Compare(NamedEntry{Name: "same", Hash: "Aaa"}) != 0 {
		t.Error("identical entries must compare equal")
	}
	lo := NamedEntry{Name: "x", Hash: "zzz"}
	hi := NamedEntry{Name: "y", Hash: "aaa"}
	if lo.Compare(hi) >= 0 {
		t.Errorf("differing Name must order by Name: got %d, want <0", lo.Compare(hi))
	}
}

func TestValidFileHash(t *testing.T) {
	valid := "F" + HashBytes([]byte("x")) // HashBytes is the bare 52-char digest
	cases := []struct {
		s    string
		want bool
	}{
		{valid, true},
		{"D" + valid[1:], false}, // dir prefix is not a file key
		{valid[1:], false},       // missing prefix
		{valid[:len(valid)-1], false},
		{valid + "a", false},
		{"F" + strings.ToUpper(valid[1:]), false}, // outside the lowercase alphabet
		{"F" + strings.Repeat("1", 52), false},    // '1' is not in the alphabet
		{"", false},
	}
	for _, c := range cases {
		if got := IsValidFileHash(c.s); got != c.want {
			t.Errorf("ValidFileHash(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestHashUrl(t *testing.T) {
	url := "https://example.invalid/artifact.bin"
	if got, want := HashUrl(url), "U"+HashBytes([]byte(url)); got != want {
		t.Errorf("HashUrl() = %q, want %q", got, want)
	}
}

func TestHashPath(t *testing.T) {
	path := "Build/go"
	if got, want := HashPath(path), "P"+HashBytes([]byte(path)); got != want {
		t.Errorf("HashPath() = %q, want %q", got, want)
	}
	if !IsValidPathHash(HashPath(path)) || IsValidPathHash(HashUrl(path)) {
		t.Error("IsValidPathHash must accept exactly the P-prefixed key shape")
	}
}

// Re-derive the hard-coded mask from the alphabet const so an edit can't leave it stale.
func TestBase32AlphabetMask(t *testing.T) {
	var mask uint64
	for i := range len(base32Alphabet) {
		idx := base32CharIndex(base32Alphabet[i])
		if idx < 0 {
			t.Fatalf("alphabet char %q is outside the digit/lowercase-letter space", base32Alphabet[i])
		}
		if mask&(1<<idx) != 0 {
			t.Fatalf("alphabet char %q repeats", base32Alphabet[i])
		}
		mask |= 1 << idx
	}
	if base32AlphabetMask != mask {
		t.Errorf("base32AlphabetMask = %#x, want %#x (derived from the alphabet)", base32AlphabetMask, mask)
	}
}

// Re-derive the hard-coded length from the encoding so a digest-size edit can't leave it stale.
func TestEncodedHashLen(t *testing.T) {
	if want := enc.EncodedLen(digestSize); encodedHashLen != want {
		t.Errorf("encodedHashLen = %d, want %d (enc.EncodedLen(digestSize))", encodedHashLen, want)
	}
}

func TestValidDirHash(t *testing.T) {
	valid := "D" + HashBytes([]byte("x"))
	if !IsValidDirHash(valid) {
		t.Errorf("ValidDirHash(%q) = false, want true", valid)
	}
	if IsValidDirHash("F" + valid[1:]) {
		t.Error("a file key must not pass ValidDirHash")
	}
	if IsValidFileHash(valid) {
		t.Error("a dir key must not pass ValidFileHash")
	}
}

func TestValidBuildHash(t *testing.T) {
	valid := "B" + HashBytes([]byte("x")) // CombineHashes output is a bare digest; Build prepends the B
	if !IsValidBuildHash(valid) {
		t.Errorf("ValidBuildHash(%q) = false, want true", valid)
	}
	if IsValidBuildHash("D"+valid[1:]) || IsValidDirHash(valid) {
		t.Error("B and D keys must not cross-validate")
	}
	if IsValidBuildHash("Bcombined") {
		t.Error("a B-prefixed non-key must not pass ValidBuildHash")
	}
}

// Counts overlapping file opens: Open enters, the file's Close leaves. Directory opens (ReadDir) are ignored.
type trackingFs struct {
	vfs.Fs
	inflight, peak atomic.Int32
}

type trackedFile struct {
	vfs.File
	owner *trackingFs
}

func (f trackedFile) Close() error {
	f.owner.inflight.Add(-1)
	return f.File.Close()
}

func (f *trackingFs) Open(name string) (vfs.File, error) {
	file, err := f.Fs.Open(name)
	if err != nil {
		return nil, err
	}
	if info, statErr := file.Stat(); statErr == nil && info.IsDir() {
		return file, nil
	}
	cur := f.inflight.Add(1)
	for {
		old := f.peak.Load()
		if cur <= old || f.peak.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(time.Millisecond) // widen the overlap window
	return trackedFile{file, f}, nil
}

// One pool per walk, threaded down the recursion: every nesting level's files share the cap, not NumCPU per level.
func TestHashDir_PoolCapsAcrossLevels(t *testing.T) {
	const P = 2
	fsys := &trackingFs{Fs: vfs.NewMemMapFs()}
	dir := "root"
	for range 4 {
		for i := range 8 {
			writeFile(t, fsys, filepath.Join(dir, "f"+strconv.Itoa(i)+".txt"), []byte{byte(i)})
		}
		dir = filepath.Join(dir, "sub")
	}
	hashDir(t.Context(), fsys, "root", nil, nil, Util.NewRoutinePoolWithParallelism(P))
	if got := fsys.peak.Load(); got > P {
		t.Errorf("peak concurrent file opens = %d, want <= %d", got, P)
	}
}

// The ReadDir open of dir signals reached, then blocks on gate; file opens under dir are counted.
type gatedDirFs struct {
	vfs.Fs
	dir     string
	reached chan Util.Nothing
	gate    <-chan Util.Nothing
	first   atomic.Bool
	opens   atomic.Int32
}

func (f *gatedDirFs) Open(name string) (vfs.File, error) {
	if name == f.dir {
		if f.first.CompareAndSwap(false, true) {
			close(f.reached)
		}
		<-f.gate
	} else if strings.HasPrefix(name, f.dir+string(filepath.Separator)) {
		f.opens.Add(1)
	}
	return f.Fs.Open(name)
}

// A cancel above a child level must reach it through the ctx thread: cancelled while its dir open is
// gated, the child stops at its first entry with the child dir's context and hashes nothing.
func TestHashDir_CancelReachesChildWalk(t *testing.T) {
	mem := vfs.NewMemMapFs()
	writeFile(t, mem, filepath.Join("root", "a.txt"), []byte("a"))
	sub := filepath.Join("root", "sub")
	for i := range 20 {
		writeFile(t, mem, filepath.Join(sub, "f"+strconv.Itoa(i)+".txt"), []byte{byte(i)})
	}
	gate := make(chan Util.Nothing)
	fsys := &gatedDirFs{Fs: mem, dir: sub, reached: make(chan Util.Nothing), gate: gate}
	ctx, cancel := context.WithCancelCause(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- c.Rescue(func() { hashDir(ctx, fsys, "root", nil, nil, Util.NewRoutinePool()) })
	}()
	<-fsys.reached
	cancel(errors.New("outside"))
	close(gate)
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "outside") || !strings.Contains(err.Error(), sub) {
		t.Errorf("want the outside cause with the child dir's context, got %v", err)
	}
	if n := fsys.opens.Load(); n != 0 {
		t.Errorf("child hashed %d files after the cancel, want 0", n)
	}
}

func TestHashDir_OutsideCancel(t *testing.T) {
	fsys := vfs.NewMemMapFs()
	writeFile(t, fsys, filepath.Join("root", "a.txt"), []byte("a"))
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("outside"))
	err := c.Rescue(func() { hashDir(ctx, fsys, "root", nil, nil, Util.NewRoutinePool()) })
	if err == nil || !strings.Contains(err.Error(), "outside") || !strings.Contains(err.Error(), "root") {
		t.Errorf("want the outside cause with dir context, got %v", err)
	}
}
