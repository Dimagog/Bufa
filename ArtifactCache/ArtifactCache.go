// Package ArtifactCache is the global content-addressed cache of pinned [[deps.ext]] artifacts.
package ArtifactCache

import (
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	vfs "github.com/spf13/afero"

	"github.com/dimagog/bufa/Hashing"
	"github.com/dimagog/bufa/Store"
	c "github.com/dimagog/bufa/internal/contract"
	"github.com/dimagog/bufa/internal/vfsx"
)

const QueryHashSentinel = "?"

// Staging-name prefixes at the cache root; Nuke's verification must recognize both.
const (
	fetchPrefix   = "fetch-"
	urlLinkPrefix = "url-"
)

// Windows-forbidden filename chars → same-looking Unicode, so a url link's name IS the url.
// Other OSes reserve a subset of these.
var urlNameEncoder = strings.NewReplacer(
	"/", "∕", // U+2215 DIVISION SLASH
	"\\", "⧵", // U+29F5 REVERSE SOLIDUS OPERATOR
	":", "꞉", // U+A789 MODIFIER LETTER COLON
	"*", "∗", // U+2217 ASTERISK OPERATOR
	"?", "？", // U+FF1F FULLWIDTH QUESTION MARK
	"\"", "＂", // U+FF02 FULLWIDTH QUOTATION MARK
	"<", "＜", // U+FF1C FULLWIDTH LESS-THAN SIGN
	">", "＞", // U+FF1E FULLWIDTH GREATER-THAN SIGN
	"|", "∣", // U+2223 DIVIDES
)

// Fits name + the url-…-<pid>-<seq> staging wrapper under every FS's 255-byte name cap.
const maxUrlNameLen = 150

func urlLinkName(url string) string {
	name := urlNameEncoder.Replace(url)
	if len(name) > maxUrlNameLen {
		hashSuffix := "_" + Hashing.HashUrl(url)
		keep := maxUrlNameLen - len(hashSuffix)
		for !utf8.RuneStart(name[keep]) { // don't cut a multi-byte look-alike char
			keep--
		}
		name = name[:keep] + hashSuffix
	}
	return name
}

type Cache struct{ fs *vfs.BasePathFs }

func GetCacheDir() string {
	if env := os.Getenv("BUFA_GLOBAL_CACHE_DIR"); env != "" {
		return env
	}
	base := c.With("Resolve user cache dir; set BUFA_GLOBAL_CACHE_DIR to place the artifact cache").Check2(os.UserCacheDir())
	return filepath.Join(base, "bufa")
}

func New(dir string) *Cache {
	dir = c.With("Resolve artifact cache dir '%s'", dir).Check2(filepath.Abs(dir))
	return &Cache{fs: vfs.NewBasePathFs(vfs.NewOsFs(), dir).(*vfs.BasePathFs)}
}

func (ac *Cache) Dir() string { return ac.EntryPath(".") }

func (ac *Cache) Owns(target string) bool {
	return strings.HasPrefix(target, ac.Dir()+string(filepath.Separator))
}

func (ac *Cache) Fs() vfs.Fs { return ac.fs }

func (ac *Cache) EntryPath(hash string) string {
	return filepath.Clean(c.With("RealPath '%s'", hash).Check2(ac.fs.RealPath(hash)))
}

func (ac *Cache) Contains(hash string) bool {
	return vfsx.RegularFileExists(ac.fs, hash)
}

func (ac *Cache) isUrlPinned(url, hash string) bool {
	target, err := vfsx.Readlink(ac.fs, urlLinkName(url))
	return err == nil && target == hash
}

func (ac *Cache) Ensure(url, expectedHash string, out io.Writer) string {
	return ac.ensure(url, expectedHash, out, true /*checkEntry*/)
}

// For a caller that already proved the F entry present — skips the redundant Contains Stat.
func (ac *Cache) EnsureURLBinding(url, expectedHash string, out io.Writer) {
	ac.ensure(url, expectedHash, out, false /*checkEntry*/)
}

func (ac *Cache) ensure(url, expectedHash string, out io.Writer, checkEntry bool) string {
	// A hit needs proof this url produced the entry — an edited url under a stale pin must re-verify.
	if expectedHash == QueryHashSentinel ||
		(checkEntry && !ac.Contains(expectedHash)) ||
		!ac.isUrlPinned(url, expectedHash) {
		ac.fetch(url, expectedHash, out)
	} else {
		slog.Info("Artifact cache hit", "hash", expectedHash, "url", url)
	}
	return ac.EntryPath(expectedHash)
}

func (ac *Cache) fetch(url, expectedHash string, out io.Writer) {
	defer c.Context("Download '%s'", url)
	slog.Info("Downloading Artifact", "url", url, "expectedHash", expectedHash)

	// Printed regardless of log level: a huge fetch must not look hung.
	fmt.Fprintln(out, "Downloading", url)

	resp := c.Check2(http.Get(url))
	defer resp.Body.Close()
	c.Require(resp.StatusCode == http.StatusOK, "server answered '%s'", resp.Status)

	// "." is the cache root itself — created here on first fetch.
	c.Check(ac.fs.MkdirAll(".", 0o755))
	f := c.Check2(vfs.TempFile(ac.fs, ".", fetchPrefix+"*"))
	tmpName := f.Name()
	hasher := Hashing.NewFileHasher()
	_, copyErr := io.Copy(io.MultiWriter(f, hasher), resp.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		c.Check(ac.fs.Remove(tmpName))
		c.Check(copyErr)
		c.Check(closeErr)
	}
	computedHash := hasher.Sum()

	slog.Info("Downloaded Artifact", "url", url, "computedHash", computedHash, "hashMatch", computedHash == expectedHash)

	// Always admitted under its own verified hash — even on a mismatch — so re-pinning to the
	// printed hash costs no second download.
	ac.publish(tmpName, computedHash)
	prevHash := ac.publishUrlLink(url, computedHash)
	c.Require(expectedHash != QueryHashSentinel,
		"Computed hash of '%s' is \"%s\" — pin it in deps.ext (\"%s\" bootstrap)", url, computedHash, QueryHashSentinel)
	hint := ""
	if prevHash == computedHash {
		hint = " (same as the earlier download already cached — url content unchanged, fix the pin)"
	}
	c.Require(computedHash == expectedHash, "Computed hash of '%s' is \"%s\", but expected '%s'%s", url, computedHash, expectedHash, hint)
}

// The only delete path (bufa nuke --global): user-requested, whole-cache — never eviction.
func (ac *Cache) Nuke() {
	Store.NukeDir("Artifact Cache", ac.Dir(), func(e fs.FileInfo) bool {
		name := e.Name()
		if e.Mode()&fs.ModeSymlink != 0 {
			return strings.HasPrefix(name, urlLinkPrefix) || ac.isUrlLink(name)
		}
		if e.Mode().IsRegular() {
			return strings.HasPrefix(name, fetchPrefix) || Hashing.IsValidFileHash(name)
		}
		return false
	})
}

// Url links are recognized by target alone — the link's own name is never consulted.
func (ac *Cache) isUrlLink(name string) bool {
	target, err := vfsx.Readlink(ac.fs, name)
	return err == nil && Hashing.IsValidFileHash(target)
}

func (ac *Cache) publish(tmpName, hash string) {
	c.Check(ac.fs.Chmod(tmpName, 0o444))
	slog.Info("Publishing Artifact", "hash", hash)
	if err := ac.fs.Rename(tmpName, hash); err != nil {
		// Discard before the verdict, so a real failure doesn't strand the tmp file.
		c.Check(ac.fs.Remove(tmpName))
		c.Require(ac.Contains(hash), "publish '%s' failed: %v", hash, err)
		slog.Info("Artifact publish collision, discarded duplicate download", "hash", hash)
	}
}

// Goroutine discriminator: pid alone collides if fetches ever run concurrently in-process
// (goroutine ids are hidden and goroutines migrate threads — a counter is the reliable unique).
var urlLinkSeq atomic.Uint64

// <encoded-url> -> F<hash> sibling link: url is a verified source of the entry.
func (ac *Cache) publishUrlLink(url, fileHash string) string {
	linkName := urlLinkName(url)
	slog.Info("Publishing Artifact URL link", "url", url, "linkName", linkName, "fileHash", fileHash)
	prevTarget, _ := vfsx.Readlink(ac.fs, linkName)
	tmpName := urlLinkPrefix + linkName + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(urlLinkSeq.Add(1), 10)
	if vfsx.DirEntryExists(ac.fs, tmpName) { // leftover of a crashed run under a recycled pid
		c.Check(ac.fs.Remove(tmpName))
	}
	vfsx.Symlink(ac.fs, tmpName, fileHash)
	// os.Symlink won't overwrite, but Rename replaces an existing link — a re-pinned url re-points.
	if err := ac.fs.Rename(tmpName, linkName); err != nil {
		// Discard before the verdict, so a real failure doesn't strand the tmp link.
		c.Check(ac.fs.Remove(tmpName))
		c.Require(ac.isUrlPinned(url, fileHash), "Publish artifact URL cache link '%s' failed: %v", linkName, err)
		slog.Info("Artifact link publish collision, verified it points to correct hash", "linkName", linkName, "fileHash", fileHash)
	}
	return prevTarget
}
