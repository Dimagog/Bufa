// Package Hashing computes BLAKE3 content hashes of files and directory trees.
// File hash = "F"+base32(digest); dir hash = "D"+base32 of a name-sorted "<hash>\t<name>\n" manifest.
package Hashing

import (
	"context"
	"encoding/base32"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	vfs "github.com/spf13/afero"
	"lukechampine.com/blake3"

	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

const (
	FilePrefix  = "F"
	DirPrefix   = "D"
	BuildPrefix = "B" // prepended by Build to CombineHashes output, which emits no prefix
	UrlPrefix   = "U"
	PathPrefix  = "P"
	digestSize  = 32 // BLAKE3 default 256-bit
)

const base32Alphabet = "abcdefghijklmnopqrstuvwxyz234567"

var enc = base32.NewEncoding(base32Alphabet).WithPadding(base32.NoPadding)

// Hard-coded so isValidHash's length check is over consts; TestEncodedHashLen re-derives it.
const encodedHashLen = 52

// 36 < 64, so alphabet membership packs into one uint64 mask.
func base32CharIndex(c byte) int {
	if '0' <= c && c <= '9' {
		return int(c - '0')
	}
	if 'a' <= c && c <= 'z' {
		return int(c-'a') + 10
	}
	return -1
}

// Hard-coded; TestBase32AlphabetMask re-derives it from the alphabet const.
const base32AlphabetMask uint64 = 0xF_FFFF_FCFC

func isValidHash(s, prefix string) bool {
	if len(s) != len(prefix)+encodedHashLen || !strings.HasPrefix(s, prefix) {
		return false
	}
	for i := len(prefix); i < len(s); i++ {
		idx := base32CharIndex(s[i])
		if idx < 0 || base32AlphabetMask&(1<<idx) == 0 {
			return false
		}
	}
	return true
}

func IsValidFileHash(s string) bool { return isValidHash(s, FilePrefix) }

func IsValidDirHash(s string) bool { return isValidHash(s, DirPrefix) }

func IsValidBuildHash(s string) bool { return isValidHash(s, BuildPrefix) }

func IsValidUrlHash(s string) bool { return isValidHash(s, UrlPrefix) }

func IsValidPathHash(s string) bool { return isValidHash(s, PathPrefix) }

func HashBytes(data []byte) string {
	sum := blake3.Sum256(data)
	return enc.EncodeToString(sum[:])
}

// HashUrl hashes the URL string, not doing any network access
func HashUrl(url string) string { return UrlPrefix + HashBytes([]byte(url)) }

// HashPath hashes the path string, not touching the filesystem
func HashPath(path string) string { return PathPrefix + HashBytes([]byte(path)) }

type FileHasher struct{ h *blake3.Hasher }

func NewFileHasher() *FileHasher { return &FileHasher{h: blake3.New(digestSize, nil)} }

func (f *FileHasher) Write(p []byte) (int, error) { return f.h.Write(p) }

func (f *FileHasher) Sum() string { return FilePrefix + enc.EncodeToString(f.h.Sum(nil)) }

func HashFile(fsys vfs.Fs, file string) string {
	f := c.With("Open '%s'", file).Check2(fsys.Open(file))
	defer f.Close()
	h := NewFileHasher()
	c.With("Hash '%s'", file).Check2(io.Copy(h, f))
	res := h.Sum()
	slog.Debug("hashed file", "path", file, "hash", res)
	return res
}

type HashEntry[T any] interface {
	comparable
	fmt.Stringer
	Compare(T) int
}

type NamedEntry struct{ Name, Hash string }

func (e NamedEntry) String() string { return e.Hash + "\t" + e.Name + "\n" }

func (e NamedEntry) Compare(o NamedEntry) int {
	if c := strings.Compare(e.Name, o.Name); c != 0 {
		return c
	}
	return strings.Compare(e.Hash, o.Hash)
}

// It SORTS items slice in place (no copy)
func CombineHashesInPlace[T HashEntry[T]](items []T) string {
	slices.SortFunc(items, func(a, b T) int { return a.Compare(b) })
	h := blake3.New(digestSize, nil)
	for _, it := range items {
		// hash.Hash.Write never returns an error, so returns are dropped.
		io.WriteString(h, it.String())
	}
	return enc.EncodeToString(h.Sum(nil))
}

func CombineHashes[T HashEntry[T]](items []T) string {
	return CombineHashesInPlace(slices.Clone(items))
}

func HashDir(fsys vfs.Fs, dir string) string {
	return HashDirFiltered(fsys, dir, nil)
}

// A constant rather than init-computed; TestEmptyDirHash pins it to the algorithm.
const EmptyDirHash = DirPrefix + "v4jutopv7gq2nicajxvdnxgjjgn4wjojvxarfn6mtkj4vza7gjra"

func HashDirFiltered(fsys vfs.Fs, dir string, dirSkipFn func(vfs.Fs, string, fs.FileInfo) bool) string {
	return hashDir(context.Background(), fsys, dir, dirSkipFn, nil, Util.NewRoutinePool())
}

type linkRef struct {
	path string
	info fs.FileInfo
}

// One pool per walk, shared down the recursion; deadlock-free only while workers run nothing but HashFile.
// ctx is the parent level's: its failure, or an outside cancel, stops this level at its next entry.
func hashDir(ctx context.Context, fsys vfs.Fs, dir string, dirSkipFn func(vfs.Fs, string, fs.FileInfo) bool, viaLinks []linkRef, pool *Util.RoutinePool) string {
	dirEntries := c.With("Read dir '%s'", dir).Check2(vfs.ReadDir(fsys, dir))

	fj := Util.ForkJoinBuilder[NamedEntry]().
		Context(ctx).
		Pool(pool).
		Capacity(len(dirEntries)).
		Build()
	defer fj.Cleanup()

	for _, e := range dirEntries {
		c.Checkf(fj.Err(), "Hashing: cancelled while hashing dir '%s'", dir)

		path := filepath.Join(dir, e.Name())
		if dirSkipFn != nil && dirSkipFn(fsys, path, e) {
			continue
		}
		var h string
		if e.IsDir() {
			// keeping this call sync (not using fj.Go) makes this a depth-first traversal
			h = hashDir(fj.Context(), fsys, path, dirSkipFn, viaLinks, pool)
		} else if e.Mode().IsRegular() {
			fj.Go(func() NamedEntry {
				return NamedEntry{e.Name(), HashFile(fsys, path)}
			})
			continue
		} else if e.Mode()&fs.ModeSymlink != 0 {
			h = hashLink(fj.Context(), fsys, path, viaLinks, pool)
		} else {
			c.Fail("Hashing: %s is neither regular file nor directory (mode %s)", path, e.Mode())
		}
		// Never prune a symlink entry: the staged link object survives the copy, so pruning it would
		// diverge the hash from the staged tree.
		if dirSkipFn != nil && h == EmptyDirHash && e.IsDir() {
			slog.Debug("Pruning empty dir", "path", path)
		} else {
			fj.AddResult(NamedEntry{e.Name(), h})
		}
	}
	hashItems := fj.Results()

	res := DirPrefix + CombineHashesInPlace(hashItems) // hashItems are discarded
	slog.Debug("hashed dir", "path", dir, "hash", res)
	return res
}

func HashLink(fsys vfs.Fs, path string) string {
	return hashLink(context.Background(), fsys, path, nil, Util.NewRoutinePool())
}

// The trusted-link shortcut runs after the dangling check, so it elides the content read but
// never the existence check. viaLinks guards cycles; a diamond stays legal, hashed twice.
func hashLink(ctx context.Context, fsys vfs.Fs, path string, viaLinks []linkRef, pool *Util.RoutinePool) string {
	target, err := fsys.Stat(path)
	if err != nil {
		// Best-effort raw-target read on the error path — "?" stands in on failure.
		rawTarget := "?"
		if t, e := vfsx.Readlink(fsys, path); e == nil {
			rawTarget = t
		}
		c.Errorf(err, "Hashing: dangling symlink '%s' -> '%s'", path, rawTarget)
	}
	if target.Mode().IsRegular() {
		if hash := getHashFromSymlink(fsys, path, IsValidFileHash); hash != "" {
			return hash
		}
		return HashFile(fsys, path)
	}
	if target.IsDir() {
		if hash := getHashFromSymlink(fsys, path, IsValidDirHash); hash != "" {
			return hash
		}
		for _, via := range viaLinks {
			c.Require(!os.SameFile(via.info, target),
				"Hashing: symlink cycle: '%s' re-enters the tree already entered via '%s'", path, via.path)
		}
		return hashDir(ctx, fsys, path, nil, append(viaLinks, linkRef{path, target}), pool)
	}
	c.Fail("Hashing: symlink %s targets neither a regular file nor a directory (mode %s)", path, target.Mode())
	return "" // unreachable; c.Fail panics
}

// Not synchronized — set once at startup, before hashing starts.
var SafeHashing bool

func getHashFromSymlink(fsys vfs.Fs, path string, isValid func(string) bool) string {
	if SafeHashing {
		return ""
	}
	rawTarget, err := vfsx.Readlink(fsys, path)
	if err != nil {
		return ""
	}
	name := filepath.Base(rawTarget)
	if !isValid(name) {
		return ""
	}
	slog.Info("Trusted link name's", "hash", name, "path", path, "target", rawTarget)
	return name
}

// This one-path entry must discover a root link itself, so `bufa hash <dangling-link>` names the
// link and its target instead of failing as an opaque Stat error.
func Hash(fsys vfs.Fs, path string) string {
	info := vfsx.Lstat(fsys, path)
	if info.Mode()&fs.ModeSymlink != 0 {
		return HashLink(fsys, path)
	}
	if info.IsDir() {
		return HashDir(fsys, path)
	}
	if info.Mode().IsRegular() {
		return HashFile(fsys, path)
	}
	c.Fail("Hashing: %s is neither regular file nor directory (mode %s)", path, info.Mode())
	return "" // unreachable; c.Fail panics
}
