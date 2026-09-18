// Package vfsx extends afero with generic fs helpers plus the os-level symlink, lstat, and copy
// ops vfs cannot express; path translation and raw OS-path probes live in internal/UnsafeIO —
// all other prod file I/O goes through vfs.Fs methods or these helpers.
package vfsx

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/internal/UnsafeIO"
	c "github.com/dimagog/bufa/internal/contract"
)

// Returns os.Readlink's error so callers pick panic vs best-effort.
func Readlink(fsys vfs.Fs, path string) (string, error) {
	return os.Readlink(UnsafeIO.OSPath(fsys, path))
}

// A fs with neither Lstater nor GetRealPath falls back to plain Stat: such a fs (MemMapFs)
// cannot hold symlinks, so the two agree.
func TryLstat(fsys vfs.Fs, path string) (fs.FileInfo, error) {
	if lstater, ok := fsys.(vfs.Lstater); ok {
		if info, lstatUsed, err := lstater.LstatIfPossible(path); lstatUsed {
			return info, err
		}
	}
	if _, ok := fsys.(UnsafeIO.GetRealPath); ok {
		return os.Lstat(UnsafeIO.OSPath(fsys, path))
	}
	return fsys.Stat(path)
}

func Lstat(fsys vfs.Fs, path string) fs.FileInfo {
	return c.With("Lstat '%s'", path).Check2(TryLstat(fsys, path))
}

// Lstat-based any-occupant probe — a symlink counts even dangling.
func DirEntryExists(fsys vfs.Fs, path string) bool {
	if _, err := TryLstat(fsys, path); !os.IsNotExist(err) {
		c.Checkf(err, "Lstat '%s'", path)
		return true
	}
	return false
}

func Exists(fsys vfs.Fs, path string) bool {
	_, err := fsys.Stat(path)
	return err == nil
}

func FileExists(fsys vfs.Fs, path string) bool {
	info, err := fsys.Stat(path)
	return err == nil && !info.IsDir()
}

func RegularFileExists(fsys vfs.Fs, path string) bool {
	info, err := fsys.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func DirExistsFailOnFile(fsys vfs.Fs, path string) bool {
	info, err := fsys.Stat(path)
	if err != nil {
		return false
	}
	c.Require(info.IsDir(), "Unexpected file '%s', if exists this path could only be a directory", path)
	return true
}

func ReadSmallFileFast(fsys vfs.Fs, name string) []byte {
	data := TryReadSmallFileFast(fsys, name)
	c.Require(data != nil, "ReadSmallFileFast: '%s' does not exist", name)
	return data
}

// One pass — open, one Read into a generous buffer, close — so a small config costs exactly
// 3 OS calls. nil ⇒ absent, ENOTDIR (an ancestor is a regular file) included.
func TryReadSmallFileFast(fsys vfs.Fs, name string) []byte {
	f, err := fsys.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil
		}
		c.Checkf(err, "Open '%s'", name)
	}
	defer func() { c.Check(f.Close()) }()

	buf := make([]byte, 0, 16*1024) // 16KB initial buffer, grows on demand
	for {
		if len(buf) == cap(buf) {
			buf = slices.Grow(buf, 1)
		}
		n, err := f.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err == io.EOF {
			return buf
		}
		if err != nil {
			if info, statErr := f.Stat(); statErr == nil {
				c.Require(!info.IsDir(), "Expected a file at '%s', but found a directory", name)
			}
		}
		c.Checkf(err, "Read '%s'", name)
		if len(buf) < cap(buf) {
			return buf // short read on a regular file ⇒ EOF; skip the probe read
		}
	}
}

// One Readdirnames(1) probes emptiness without reading the whole listing. vfs deliberately: the
// deletion stays jailed to the caller's rooted fs.
func RemoveDirIfEmpty(fsys vfs.Fs, dir string) bool {
	f, err := fsys.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close() // panic backstop; a second Close is a harmless ErrClosed
	names, readErr := f.Readdirnames(1)
	f.Close() // f.Close() before Remove — Windows refuses an open dir removal
	if len(names) > 0 || (readErr != nil && readErr != io.EOF) {
		return false
	}
	c.Check(fsys.Remove(dir))
	return true
}

// RemoveAll deletes a symlink child as a link object, never following it.
func RemoveAllUnder(fsys vfs.Fs, dir string) {
	f := c.With("Open '%s'", dir).Check2(fsys.Open(dir))
	names, err := f.Readdirnames(-1)
	c.Check(f.Close())
	c.Checkf(err, "Readdirnames '%s'", dir)
	for _, name := range names {
		c.Check(fsys.RemoveAll(filepath.Join(dir, name)))
	}
}

// Creates the link object at linkPath with target written verbatim; os.Symlink picks file vs
// dir flavor from the referent and refuses an occupied linkPath.
func Symlink(fsys vfs.Fs, linkPath, target string) {
	c.Check(os.Symlink(target, UnsafeIO.OSPath(fsys, linkPath)))
}

// Resolved via the strict UnsafeIO.RealPath, not OSPath: a link op on a fs without real paths
// must fail loud, never Abs-guess.
func XSymlink(targetFs vfs.Fs, targetPath string, linkFs vfs.Fs, linkPath string) {
	osSymlink(UnsafeIO.RealPath(targetFs, targetPath), UnsafeIO.RealPath(linkFs, linkPath))
}

// The CopyFileW engine (UseCopyFileOS) requires RealPath on both stores; the stream fallback
// opens through the fs values, so it works on any fs but refuses a read-only dst.
func XCopyFile(srcFs vfs.Fs, srcPath string, dstFs vfs.Fs, dstPath string, mode os.FileMode) {
	if UseCopyFileOS {
		osCopyFile(UnsafeIO.RealPath(srcFs, srcPath), UnsafeIO.RealPath(dstFs, dstPath), true)
		return
	}
	slog.Debug("StdLib io.Copy", "src", srcPath, "dst", dstPath)
	in := c.With("Open src '%s'", srcPath).Check2(srcFs.Open(srcPath))
	defer in.Close()
	out := c.With("Create dst '%s'", dstPath).Check2(dstFs.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode))
	defer out.Close()
	c.With("Copy '%s' -> '%s'", srcPath, dstPath).Check2(io.Copy(out, in))
}

func osSymlink(targetPath, linkPath string) {
	slog.Debug("osSymlink", "target", targetPath, "link", linkPath)
	c.Check(os.MkdirAll(filepath.Dir(linkPath), 0o755))
	c.Check(os.Symlink(targetPath, linkPath))
}
