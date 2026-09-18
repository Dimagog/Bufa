// Package Watcher hosts a recursive watch over a source tree, exposed as an on-demand UDS RPC
// daemon via Daemon.Serve.
package Watcher

import (
	"log/slog"
	"net/rpc"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rjeczalik/notify"

	"github.com/dimagog/bufa"
	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Daemon"
	"github.com/dimagog/bufa/NameServer"
	"github.com/dimagog/bufa/Store"
	"github.com/dimagog/bufa/internal/UnsafeIO"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

// notify drops events when this fills; sized generously so build bursts don't lose changes.
const eventChanBufSize = 1024

type watcher struct {
	srcDir string
	// recursive subtree watch; every event on it is inside srcDir by construction
	events chan notify.EventInfo
	// non-recursive parent watch, only to detect srcDir itself being renamed/deleted
	parentEvents chan notify.EventInfo
	done         chan Util.Nothing

	mu sync.RWMutex
	// build-dir-rel → staged-source hash
	srcHashCache map[string]string
	// build-dir-rel → dirty tree hash
	dirtyHashCache map[string]string
	// build-dir-rel → dirty/∕<dir> skip hash; a shadow of on-disk state, never event-invalidated
	dirtySkipHashCache map[string]string
	// build-dir-rel → BuildConfig.Config
	buildConfigCache map[string]BuildConfig.BufaConfig
	// combined-input-hash → build hash
	buildHashCache map[string]string
	// build-dir-rel → the combined hash out/∕<dir> was last pointed at; a shadow of the on-disk GC
	// root, default-absent ⇒ "not current"
	pathBuildHashCache map[string]string

	// changed-dir (absolute) → owning build-dir-rel, or "." when the walk reached srcDir
	owningDirCache map[string]string

	// A pointer, not value+bool: the zero RootConfig is a legitimate value.
	rootConfig *BuildConfig.RootConfig

	// non-nil iff this daemon currently owns the global NameServer socket; guarded by nsMu
	nsMu      sync.Mutex
	nsCleanup func()

	// closes the Daemon.Serve listener; set once in register, before any RPC method can fire
	stopMu sync.Mutex
	stop   func()
}

func newWatcher(srcDir string) *watcher {
	srcDir = c.Check2(filepath.Abs(srcDir))
	slog.Info("Watching", "dir", srcDir)
	events := make(chan notify.EventInfo, eventChanBufSize)
	c.Checkf(notify.Watch(srcDir+"/...", events, notify.All),
		"notify.Watch %s/...", srcDir)
	// The subtree watch sees events INSIDE srcDir, not events about srcDir, so watch the parent too.
	parentEvents := make(chan notify.EventInfo, eventChanBufSize)
	if parent := filepath.Dir(srcDir); parent != srcDir {
		c.Checkf(notify.Watch(parent, parentEvents, notify.Remove|notify.Rename),
			"notify.Watch %s (parent)", parent)
	}
	w := &watcher{
		srcDir:             srcDir,
		events:             events,
		parentEvents:       parentEvents,
		done:               make(chan Util.Nothing),
		srcHashCache:       make(map[string]string),
		dirtyHashCache:     make(map[string]string),
		dirtySkipHashCache: make(map[string]string),
		buildConfigCache:   make(map[string]BuildConfig.BufaConfig),
		buildHashCache:     make(map[string]string),
		pathBuildHashCache: make(map[string]string),
		owningDirCache:     make(map[string]string),
	}
	go w.run()
	return w
}

// A no-op when no parent watch was registered (fs root).
func (w *watcher) close() {
	notify.Stop(w.events)
	notify.Stop(w.parentEvents)
	close(w.done)
}

func (w *watcher) run() {
	for {
		select {
		case ev := <-w.events:
			slog.Debug("Watcher", "event", ev)
			w.invalidatePathCaches(ev)
		case ev := <-w.parentEvents:
			slog.Debug("Watcher parent event", "event", ev)
			w.checkBaseGone(ev)
		case <-w.done:
			return
		}
	}
}

func (w *watcher) register(srv *rpc.Server, stop func()) {
	c.Check(srv.RegisterName("Watcher", w))
	w.stopMu.Lock()
	w.stop = stop
	w.stopMu.Unlock()
}

// The existence probe catches what the path comparison can miss: a rename delivered only under
// the new name, or an 8.3 short name.
func (w *watcher) checkBaseGone(ev notify.EventInfo) {
	const baseDirGoneEvent = notify.Remove | notify.Rename
	if ev.Event()&baseDirGoneEvent == 0 {
		return
	}
	if strings.EqualFold(ev.Path(), w.srcDir) || !UnsafeIO.OSDirEntryExists(w.srcDir) {
		slog.Info("srcDir gone, shutting down daemon", "event", ev, "srcDir", w.srcDir)
		go w.triggerShutdown()
	}
}

func (w *watcher) invalidatePathCaches(ev notify.EventInfo) {
	p := ev.Path()
	const graphChangeEvent = notify.Create | notify.Remove | notify.Rename
	const movedDirEvent = notify.Remove | notify.Rename

	// notify consolidates both watchpoints into one recursive OS watch at the parent, so srcDir's own
	// event lands here too. The owner walk never terminates for a path outside srcDir.
	if strings.EqualFold(p, w.srcDir) {
		w.checkBaseGone(ev)
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	base := filepath.Base(p)
	if strings.EqualFold(base, Store.SrcRootFileName) {
		if ev.Event()&graphChangeEvent != 0 {
			slog.Info(Store.SrcRootFileName+" created/removed/renamed, shutting down daemon", "event", ev)
			go w.triggerShutdown()
		} else {
			slog.Info(Store.SrcRootFileName+" written, invalidating RootConfig cache", "event", ev)
			w.rootConfig = nil
		}
		return
	}
	// Only the .git entry itself: events inside .git/** have other basenames. .git is a source-root
	// marker, so exit and let the next CLI re-resolve.
	if strings.EqualFold(base, Store.GitRootMarker) && ev.Event()&graphChangeEvent != 0 {
		slog.Info(Store.GitRootMarker+" created/removed/renamed, shutting down daemon", "event", ev)
		go w.triggerShutdown()
		return
	}
	if strings.EqualFold(base, Store.BuildConfigName) && ev.Event()&graphChangeEvent != 0 {
		// Essential for an unanchored root (its identity dissolves); harmless for anchored roots.
		if ev.Event()&movedDirEvent != 0 && strings.EqualFold(filepath.Dir(p), w.srcDir) {
			slog.Info("root's own BUFA removed/renamed, shutting down daemon", "event", ev)
			go w.triggerShutdown()
			return
		}
		slog.Info("BUFA created/removed/renamed, clearing entire Watcher cache", "event", ev)
		clear(w.owningDirCache)
		clear(w.srcHashCache)
		clear(w.dirtyHashCache)
		clear(w.buildConfigCache)
		return
	}

	// The fall-through owner invalidation below is REQUIRED, not merely conservative: a create/remove
	// flips Store.skipNested on the dir it declares, so the owner's staged source gains or loses that
	// whole subtree.
	if suffix := Store.VirtualSuffix(base); suffix != "" {
		rel := c.Check2(filepath.Rel(w.srcDir, filepath.Dir(p)))
		virtualPath := Util.NormalizePath(filepath.Join(rel, suffix))
		w.invalidateBuildDirEntries(virtualPath)
		slog.Info("<suffix>.BUFA event, invalidating virtual build dir", "event", ev, "virtual", virtualPath)
		if ev.Event()&graphChangeEvent != 0 {
			deleteSubtree("owningDirCache", w.owningDirCache, Util.NormalizePath(filepath.Join(filepath.Dir(p), suffix)))
		}
	}

	// A Create can materialize a real dir at a cached VIRTUAL path, whose config and constant src
	// hash would otherwise keep building with empty own source.
	if ev.Event()&notify.Create != 0 {
		rel := Util.NormalizePath(c.Check2(filepath.Rel(w.srcDir, p)))
		w.invalidateBuildDirEntries(rel)
	}

	// The owner-of-parent walk never reaches entries keyed at or below the moved dir itself.
	if ev.Event()&movedDirEvent != 0 {
		w.pruneMovedDirCaches(p)
	}

	rawDir := filepath.Dir(p)
	dir := Util.NormalizePath(rawDir)
	owningDir, ok := w.owningDirCache[dir]
	if !ok {
		owningDir = w.resolveOwningDir(rawDir)
		slog.Info("Resolved owning build", "dir", dir, "owningDir", owningDir)
		w.owningDirCache[dir] = owningDir
	}
	w.invalidateBuildDirEntries(owningDir)
	slog.Debug("Invalidated caches for", "owningDir", owningDir)
}

// The three caches share key space and lifetime. Caller holds w.mu.
func (w *watcher) invalidateBuildDirEntries(key string) {
	delete(w.srcHashCache, key)
	delete(w.dirtyHashCache, key)
	delete(w.buildConfigCache, key)
}

// buildHashCache is left alone — content-addressed, it self-corrects. Caller holds w.mu.
func (w *watcher) pruneMovedDirCaches(p string) {
	rel, err := filepath.Rel(w.srcDir, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return // outside the tree, or srcDir itself (never a cache key)
	}
	relKey := Util.NormalizePath(rel)
	deleteSubtree("srcHashCache", w.srcHashCache, relKey)
	deleteSubtree("dirtyHashCache", w.dirtyHashCache, relKey)
	deleteSubtree("dirtySkipHashCache", w.dirtySkipHashCache, relKey)
	deleteSubtree("buildConfigCache", w.buildConfigCache, relKey)
	deleteSubtree("pathBuildHashCache", w.pathBuildHashCache, relKey)
	deleteSubtree("owningDirCache", w.owningDirCache, Util.NormalizePath(p))
}

func deleteSubtree[V any](name string, m map[string]V, root string) {
	prefix := root + "/"
	for dir := range m {
		if dir == root || strings.HasPrefix(dir, prefix) {
			delete(m, dir)
			slog.Debug("Pruned moved-dir cache entry", "cache", name, "root", root, "dir", dir)
		}
	}
}

// A dirty-materialized virtual dir has no BUFA of its own, so the sibling declaration counts too.
// Both probes are silent file-only checks: the daemon must not die on a squatting directory.
func (w *watcher) resolveOwningDir(dir string) string {
	for {
		rel := c.Check2(filepath.Rel(w.srcDir, dir))
		if rel == "." {
			return rel
		}
		if !Util.RelStrictlyBelow(rel) {
			// Defensive: a path outside srcDir would walk up forever — filepath.Dir is a fixed point at the
			// drive root, so rel never becomes ".".
			slog.Warn("resolveOwningDir: path outside srcDir", "dir", dir, "srcDir", w.srcDir)
			return "."
		}
		if UnsafeIO.OSFileExists(filepath.Join(dir, Store.BuildConfigName)) {
			return Util.NormalizePath(rel)
		}
		// File-only and silent, matching virtualConfigDeclares.
		if virtualConfig := Store.VirtualConfigPathForDir(dir); virtualConfig != "" && UnsafeIO.OSFileExists(virtualConfig) {
			return Util.NormalizePath(rel)
		}
		dir = filepath.Dir(dir)
	}
}

func (w *watcher) GetSrcHash(path string, hash *string) error {
	path = Util.NormalizePath(path)
	w.mu.RLock()
	*hash = w.srcHashCache[path]
	w.mu.RUnlock()
	slog.Info("Get SrcHash", "path", path, "hash", *hash)
	return nil
}

type SetSrcHashArgs struct{ Path, Hash string }

func (w *watcher) SetSrcHash(args SetSrcHashArgs, _ *Util.Nothing) error {
	path := Util.NormalizePath(args.Path)
	w.mu.Lock()
	w.srcHashCache[path] = args.Hash
	w.mu.Unlock()
	slog.Info("Set SrcHash", "path", path, "hash", args.Hash)
	return nil
}

func (w *watcher) GetDirtyHash(path string, hash *string) error {
	path = Util.NormalizePath(path)
	w.mu.RLock()
	*hash = w.dirtyHashCache[path]
	w.mu.RUnlock()
	slog.Info("Get DirtyHash", "path", path, "hash", *hash)
	return nil
}

type SetDirtyHashArgs struct{ Path, Hash string }

func (w *watcher) SetDirtyHash(args SetDirtyHashArgs, _ *Util.Nothing) error {
	path := Util.NormalizePath(args.Path)
	w.mu.Lock()
	w.dirtyHashCache[path] = args.Hash
	w.mu.Unlock()
	slog.Info("Set DirtyHash", "path", path, "hash", args.Hash)
	return nil
}

func (w *watcher) GetDirtySkipHash(path string, skipHash *string) error {
	path = Util.NormalizePath(path)
	w.mu.RLock()
	*skipHash = w.dirtySkipHashCache[path]
	w.mu.RUnlock()
	slog.Info("Get DirtySkipHash", "path", path, "skipHash", *skipHash)
	return nil
}

type SetDirtySkipHashArgs struct{ Path, SkipHash string }

func (w *watcher) SetDirtySkipHash(args SetDirtySkipHashArgs, _ *Util.Nothing) error {
	path := Util.NormalizePath(args.Path)
	w.mu.Lock()
	w.dirtySkipHashCache[path] = args.SkipHash
	w.mu.Unlock()
	slog.Info("Set DirtySkipHash", "path", path, "skipHash", args.SkipHash)
	return nil
}

type SetBuildConfigArgs struct {
	Path   string
	Config BuildConfig.BufaConfig
}

type GetBuildConfigReply struct {
	Found  bool
	Config BuildConfig.BufaConfig
	Hashes map[string]string
}

func (w *watcher) GetBuildConfig(path string, reply *GetBuildConfigReply) error {
	np := Util.NormalizePath(path)
	w.mu.RLock()
	defer w.mu.RUnlock()
	reply.Config, reply.Found = w.buildConfigCache[np]
	if !reply.Found {
		slog.Info("Get BuildConfig", "path", path, "hit", false)
		return nil
	}
	hashes := make(map[string]string, 1+len(reply.Config.Deps.Src))
	if h, ok := w.srcHashCache[np]; ok {
		hashes[path] = h
	}
	for _, dep := range reply.Config.Deps.Src {
		if h, ok := w.srcHashCache[Util.NormalizePath(dep)]; ok {
			hashes[dep] = h
		}
	}
	reply.Hashes = hashes
	slog.Info("Get BuildConfig", "path", path, "hit", true, "knownHashes", len(hashes), "outOf", 1+len(reply.Config.Deps.Src))
	return nil
}

func (w *watcher) SetBuildConfig(args SetBuildConfigArgs, _ *Util.Nothing) error {
	path := Util.NormalizePath(args.Path)
	w.mu.Lock()
	w.buildConfigCache[path] = args.Config
	w.mu.Unlock()
	slog.Info("Set BuildConfig", "path", path)
	return nil
}

type SetBuildHashArgs struct{ Combined, BuildHash string }

type GetBuildHashArgs struct{ Combined, SrcDir string }

type GetBuildHashReply struct {
	BuildHash            string
	PathBuildHashCurrent bool
}

func (w *watcher) GetBuildHash(args GetBuildHashArgs, reply *GetBuildHashReply) error {
	w.mu.RLock()
	reply.BuildHash = w.buildHashCache[args.Combined]
	reply.PathBuildHashCurrent = w.pathBuildHashCache[Util.NormalizePath(args.SrcDir)] == args.Combined
	w.mu.RUnlock()
	slog.Info("Get BuildHash", "combined", args.Combined, "hash", reply.BuildHash, "dir", args.SrcDir, "pathBuildHashCurrent", reply.PathBuildHashCurrent)
	return nil
}

func (w *watcher) SetBuildHash(args SetBuildHashArgs, _ *Util.Nothing) error {
	w.mu.Lock()
	w.buildHashCache[args.Combined] = args.BuildHash
	w.mu.Unlock()
	slog.Info("Set BuildHash", "combined", args.Combined, "hash", args.BuildHash)
	return nil
}

type SetPathBuildHashArgs struct{ SrcDir, Combined string }

// Not invalidated on file-change events: a content change shifts Combined, so a stale record
// simply mismatches.
func (w *watcher) SetPathBuildHash(args SetPathBuildHashArgs, _ *Util.Nothing) error {
	dir := Util.NormalizePath(args.SrcDir)
	w.mu.Lock()
	w.pathBuildHashCache[dir] = args.Combined
	w.mu.Unlock()
	slog.Info("Set PathBuildHash", "dir", dir, "combined", args.Combined)
	return nil
}

type GetRootConfigReply struct {
	Found       bool
	Config      BuildConfig.RootConfig
	BufaVersion string
}

func (w *watcher) GetRootConfig(_ Util.Nothing, reply *GetRootConfigReply) error {
	reply.BufaVersion = Bufa.Version
	w.mu.RLock()
	if w.rootConfig != nil {
		reply.Config, reply.Found = *w.rootConfig, true
	}
	w.mu.RUnlock()
	slog.Info("Get RootConfig", "found", reply.Found)
	return nil
}

// Dropped only when the .BUFA marker is written, not on ordinary file-change events.
func (w *watcher) SetRootConfig(cfg BuildConfig.RootConfig, _ *Util.Nothing) error {
	w.mu.Lock()
	w.rootConfig = &cfg
	w.mu.Unlock()
	slog.Info("Set RootConfig")
	return nil
}

func Serve(sockPath, srcDir string, idleTimeout time.Duration) {
	w := newWatcher(srcDir)
	defer w.close()
	w.tryStartNS()
	defer w.closeNS()
	Daemon.Serve(sockPath, idleTimeout, w.register)
}

// Not a direct call only for Unit Test's sake: the real TryStart binds the global socket.
var nsTryStart = NameServer.TryStart

// No own-NS short-circuit: after an external NameServer.Stop the held cleanup is spent, and
// tryListen's dial-probe already makes claiming twice impossible.
func (w *watcher) tryStartNS() {
	w.nsMu.Lock()
	defer w.nsMu.Unlock()
	if c := nsTryStart(); c != nil {
		w.nsCleanup = c
	}
}

func (w *watcher) closeNS() {
	w.nsMu.Lock()
	defer w.nsMu.Unlock()
	if w.nsCleanup != nil {
		w.nsCleanup()
		w.nsCleanup = nil
	}
}

func (w *watcher) TryStartNameServer(_ Util.Nothing, _ *Util.Nothing) error {
	w.tryStartNS()
	return nil
}

// Closes the listener after a short delay so the RPC reply lands first.
func (w *watcher) Shutdown(_ Util.Nothing, _ *Util.Nothing) error {
	slog.Info("Shutdown RPC received")
	go func() {
		time.Sleep(50 * time.Millisecond)
		w.triggerShutdown()
	}()
	return nil
}

// Idempotent; a nil w.stop — an event firing between newWatcher and register — is a no-op.
func (w *watcher) triggerShutdown() {
	w.stopMu.Lock()
	stop := w.stop
	w.stop = nil
	w.stopMu.Unlock()
	if stop != nil {
		stop()
	}
}
