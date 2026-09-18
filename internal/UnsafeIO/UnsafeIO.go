// Package UnsafeIO holds the raw OS-path escape hatches: vfs→OS path translation and probes on
// OS paths outside any vfs jail. An UnsafeIO call site is deliberately loud — it marks code that
// bypasses the injected fs.
package UnsafeIO

import (
	"io/fs"
	"os"
	"path/filepath"

	vfs "github.com/spf13/afero"

	c "github.com/dimagog/bufa/internal/contract"
)

type GetRealPath interface {
	RealPath(string) (string, error)
}

// Must never take the filepath.Abs fallback — a virtual fs path is not an OS path, and the
// hard failure is the point.
func RealPath(fsys vfs.Fs, path string) string {
	rp, ok := fsys.(GetRealPath)
	c.Require(ok, "UnsafeIO: fs %T does not expose RealPath", fsys)
	return realPathOf(rp, path)
}

func realPathOf(rp GetRealPath, path string) string {
	return c.With("RealPath '%s'", path).Check2(rp.RealPath(path))
}

// Link ops must call os package APIs even under an injected afero fs — symlinks are an OS
// feature, so any fs holding one has a real path.
func OSPath(fsys vfs.Fs, path string) string {
	if realPathFs, ok := fsys.(GetRealPath); ok {
		return realPathOf(realPathFs, path)
	}
	return c.With("Abs '%s'", path).Check2(filepath.Abs(path))
}

// Readlink through the strict RealPath, returning the link's raw OS-path target — the loud
// flavor for store link reads, where OSPath's Abs fallback would turn a mis-wrapped fs into
// silent absence (e.g. GC collecting everything).
func OSReadlink(fsys vfs.Fs, path string) (string, error) {
	return os.Readlink(RealPath(fsys, path))
}

func OSFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func OSDirEntryExists(path string) bool {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		c.Checkf(err, "Lstat '%s'", path)
		return true
	}
	return false
}

// Stat/Lstat that report any error as absence — for probes on OS paths outside any vfs (the
// srcRoot discovery walk).
func OSTryStat(path string) fs.FileInfo {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return info
}

func OSTryLstat(path string) fs.FileInfo {
	info, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	return info
}
