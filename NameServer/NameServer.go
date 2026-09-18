// Package NameServer is a single-host registry resolving a working directory to its project's
// source root, so clients skip the .BUFA marker walk.
package NameServer

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"sync"

	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

const sockName = "bufa-ns.sock"

func SockPath() string { return filepath.Join(os.TempDir(), sockName) }

type nameServer struct {
	mu       sync.RWMutex
	cwdCache map[string]string // normalized cwd → resolved srcRoot
	stop     func()            // once-guarded cleanup, set before the accept loop starts
}

// The server never touches the filesystem: a miss is the client's job to resolve and back-fill.
func (ns *nameServer) GetSrcRoot(cwd string, srcRoot *string) error {
	ns.mu.RLock()
	cached := ns.cwdCache[Util.NormalizePath(cwd)]
	ns.mu.RUnlock()
	if cached != "" {
		slog.Info("NameServer cache hit", "cwd", cwd, "srcRoot", cached)
	} else {
		slog.Info("NameServer cache miss", "cwd", cwd)
	}
	*srcRoot = cached
	return nil
}

type SetSrcRootArgs struct{ Dir, SrcRoot string }

// Path arithmetic on the two endpoints, no filesystem walk.
func (ns *nameServer) SetSrcRoot(args SetSrcRootArgs, _ *Util.Nothing) error {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	normSrcRoot := Util.NormalizePath(args.SrcRoot)
	for d := args.Dir; ; {
		normD := Util.NormalizePath(d)
		ns.cwdCache[normD] = args.SrcRoot
		if normD == normSrcRoot {
			break
		}
		parent := filepath.Dir(d)
		if parent == d { // fs root reached without matching SrcRoot — malformed call
			break
		}
		d = parent
	}
	slog.Info("NameServer cache set", "dir", args.Dir, "srcRoot", args.SrcRoot)
	return nil
}

// Fire-and-forget: the sock disappears shortly after the reply lands.
func (ns *nameServer) Shutdown(_ Util.Nothing, _ *Util.Nothing) error {
	slog.Info("NameServer Shutdown RPC received")
	go ns.stop()
	return nil
}

// Listen failures are logged and ignored — Watcher keeps serving its own socket regardless.
func TryStart() func() { return tryStartAt(SockPath()) }

func tryStartAt(sockPath string) func() {
	l, ok := tryListen(sockPath)
	if !ok {
		return nil
	}

	slog.Info("NameServer listening", "sockPath", sockPath)
	ns := &nameServer{cwdCache: make(map[string]string)}

	done := make(chan Util.Nothing, 1)
	// Once: reachable from both the Shutdown RPC and the owning daemon's exit; a second run could
	// os.Remove a socket another daemon has since re-bound.
	var once sync.Once
	ns.stop = func() {
		once.Do(func() {
			_ = l.Close()
			_ = os.Remove(sockPath)
			<-done
			slog.Info("NameServer stopped", "sockPath", sockPath)
		})
	}

	srv := rpc.NewServer()
	c.Check(srv.RegisterName("NameServer", ns))

	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go srv.ServeConn(c)
		}
	}()

	return ns.stop
}

// Stops the global NameServer wherever it runs; its owning Watcher daemon keeps serving its own
// socket. The sock disappears shortly after return (Shutdown is fire-and-forget).
func Stop(out io.Writer) { stopAt(SockPath(), out) }

func stopAt(sockPath string, out io.Writer) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		slog.Info("Stop: no NameServer to stop", "sockPath", sockPath)
		fmt.Fprintln(out, "Name server was not running")
		// Plain Remove, no live-probe: the dial above just failed, so the sock is stale.
		if os.Remove(sockPath) == nil {
			slog.Info("Stale NameServer socket removed", "sockPath", sockPath)
		}
		return
	}
	defer conn.Close()
	client := rpc.NewClient(conn)
	defer client.Close()

	if err := client.Call("NameServer.Shutdown", Util.Nothing{}, &Util.Nothing{}); err != nil {
		slog.Error("NameServer.Shutdown RPC failed", "err", err)
		fmt.Fprintf(out, "Failed to stop name server: %v\n", err)
		return
	}
	slog.Info("NameServer stopped via RPC", "sockPath", sockPath)
	fmt.Fprintln(out, "Name server stopped")
}

func tryListen(sockPath string) (net.Listener, bool) {
	if l, err := net.Listen("unix", sockPath); err == nil {
		return l, true
	}
	if c, err := net.Dial("unix", sockPath); err == nil {
		c.Close()
		// Possibly our own listener: tryStartNS re-probes unconditionally on takeover requests.
		slog.Info("NameServer already running", "sockPath", sockPath)
		return nil, false
	}
	_ = os.Remove(sockPath)
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		slog.Warn("NameServer bind failed; continuing without", "sockPath", sockPath, "err", err)
		return nil, false
	}
	return l, true
}

// alive false ⇒ the dial itself failed; alive with srcRoot=="" ⇒ answered, no entry.
func GetSrcRoot(cwd string) (srcRoot string, nsAlive bool) { return getSrcRootAt(SockPath(), cwd) }

func getSrcRootAt(sockPath, cwd string) (string, bool) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	client := rpc.NewClient(conn)
	defer client.Close()

	var srcRoot string
	if err := client.Call("NameServer.GetSrcRoot", cwd, &srcRoot); err != nil {
		slog.Warn("NameServer.GetSrcRoot call failed", "err", err)
		return "", true // dial worked, RPC errored — NS is up
	}
	return srcRoot, true
}

// Best-effort — a failed dial only costs a future walk, never correctness.
func SetSrcRoot(dir, srcRoot string) { go setSrcRootAt(SockPath(), dir, srcRoot) }

func setSrcRootAt(sockPath, dir, srcRoot string) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return
	}
	defer conn.Close()
	client := rpc.NewClient(conn)
	defer client.Close()

	args := SetSrcRootArgs{Dir: dir, SrcRoot: srcRoot}
	if err := client.Call("NameServer.SetSrcRoot", args, &Util.Nothing{}); err != nil {
		slog.Warn("NameServer.SetSrcRoot call failed", "err", err)
	}
}
