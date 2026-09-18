// Package Daemon is an on-demand, single-instance UDS RPC daemon; client and daemon share one
// binary. Singleton-ness is enforced by bind() on the socket file itself.
package Daemon

import (
	"log/slog"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	c "github.com/dimagog/bufa/internal/contract"
)

const connectTimeout = 5 * time.Second

const pollInterval = 20 * time.Millisecond

func PollUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(pollInterval)
	}
	return false
}

func Connect(sockPath string, daemonArgs ...string) (net.Conn, bool) {
	if conn, err := net.Dial("unix", sockPath); err == nil {
		slog.Info("Connected to daemon", "sockPath", sockPath)
		return conn, true
	}

	exe := c.Check2(os.Executable())
	spawnArgs := append([]string{"--daemon", sockPath}, daemonArgs...)
	cmd := exec.Command(exe, spawnArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	slog.Info("Spawning daemon", "sockPath", sockPath)
	c.Checkf(cmd.Start(), "spawn daemon")
	// Detach from Go's child-tracking finalizers; daemon outlives us.
	_ = cmd.Process.Release()

	var conn net.Conn
	if PollUntil(connectTimeout, func() bool {
		c, err := net.Dial("unix", sockPath)
		if err != nil {
			return false
		}
		conn = c
		return true
	}) {
		slog.Info("Connected to spawned daemon", "sockPath", sockPath)
		return conn, false
	}
	slog.Error("Spawned daemon did not start listening", "sockPath", sockPath)
	return nil, false
}

// Serve creates the socket's parent dir first — a missing dir would otherwise make the Listen
// failure look like a stale socket.
func Serve(sockPath string, idleTimeout time.Duration, register func(srv *rpc.Server, stop func())) {
	c.Check(os.MkdirAll(filepath.Dir(sockPath), 0o755))
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		slog.Warn("Socket path exists; probing for live daemon", "sockPath", sockPath)
		if c, derr := net.Dial("unix", sockPath); derr == nil {
			c.Close()
			slog.Info("Live daemon found, exiting", "sockPath", sockPath)
			return
		}
		// Residual race: between the winner's bind() and listen() the path resolves but refuses
		// connections, so a peer may Remove it and re-Listen. The orphan exits at its idle deadline.
		_ = os.Remove(sockPath)
		slog.Info("Stale socket removed", "sockPath", sockPath)
		l = c.Check2(net.Listen("unix", sockPath))
	}
	slog.Info("Daemon listening on", "sockPath", sockPath)
	defer os.Remove(sockPath)

	srv := rpc.NewServer()
	ul := l.(*net.UnixListener)
	register(srv, func() { _ = ul.Close() })

	for {
		c.Check(ul.SetDeadline(time.Now().Add(idleTimeout)))
		c, err := ul.Accept()
		if err != nil {
			slog.Info("Idle timeout or listener closed, exiting", "sockPath", sockPath, "error", err)
			return
		}
		go srv.ServeConn(c)
	}
}
