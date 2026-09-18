// Package DaemonClient is the build-side wrapper over the Watcher RPC daemon.
package DaemonClient

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"time"

	"github.com/dimagog/bufa"
	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Daemon"
	"github.com/dimagog/bufa/Watcher"
	"github.com/dimagog/bufa/internal/Util"
	c "github.com/dimagog/bufa/internal/contract"
)

// Exported for bufa nuke's build-root verification: the sock is the one legal file there.
const SockName = "bufa-d.sock"

// A nil rpc handle means "no daemon": every method returns a miss/no-op, so callers never nil-check.
type Client struct {
	daemonClient  *rpc.Client
	restartDaemon func(msg string) // set by Connect; GetRootConfig fires it on daemon version mismatch
}

func sockPath(bldRoot string, daemonDisabled bool) string {
	if daemonDisabled {
		return ""
	}
	return filepath.Join(bldRoot, SockName)
}

func Connect(bldRoot, srcRoot string, daemonDisabled, nsAlive bool, out io.Writer) *Client {
	sockPath := sockPath(bldRoot, daemonDisabled)
	if sockPath == "" {
		slog.Info("Daemon disabled, not connecting")
		return &Client{}
	}

	conn, alreadyRunning := Daemon.Connect(sockPath, srcRoot)
	if conn == nil {
		slog.Warn("Watcher daemon did not start; hashing uncached", "sockPath", sockPath)
		return &Client{}
	}
	client := rpc.NewClient(conn)
	// nsAlive comes from the single NameServer dial in PrepareConfig.
	if alreadyRunning && !nsAlive {
		var reply Util.Nothing
		call := client.Go("Watcher.TryStartNameServer", Util.Nothing{}, &reply, make(chan *rpc.Call, 1))
		go logRpcError(call, "Watcher.TryStartNameServer", "sockPath", sockPath)
		slog.Info("Requested NameServer takeover on already-running project daemon", "sockPath", sockPath)
	}
	cl := &Client{daemonClient: client}
	cl.restartDaemon = func(msg string) {
		fmt.Fprintln(out, msg)
		client.Close()
		Restart(bldRoot, false /*daemonDisabled*/, out)
		*cl = *Connect(bldRoot, srcRoot, false /*daemonDisabled*/, nsAlive, out)
	}
	return cl
}

func Restart(bldRoot string, daemonDisabled bool, out io.Writer) {
	sockPath, _ := stop(bldRoot, daemonDisabled, out)
	if sockPath == "" {
		return
	}
	if waitSockGone(sockPath) {
		fmt.Fprintln(out, "Daemon restarted")
	}
}

func Stop(bldRoot string, daemonDisabled bool, out io.Writer) {
	if _, stopped := stop(bldRoot, daemonDisabled, out); stopped {
		fmt.Fprintln(out, "Daemon stopped")
	}
}

// Stop that waits for the sock cleanup like Restart, so the caller can delete the build root
// without racing the dying daemon.
func StopWait(bldRoot string, daemonDisabled bool, out io.Writer) {
	sockPath, stopped := stop(bldRoot, daemonDisabled, out)
	if sockPath == "" || !stopped {
		return
	}
	if waitSockGone(sockPath) {
		fmt.Fprintln(out, "Daemon stopped")
	} else {
		fmt.Fprintln(out, "Daemon stop timed out; sock may remain")
	}
}

func waitSockGone(sockPath string) bool {
	slog.Info("Waiting for sock cleanup", "sockPath", sockPath)
	if Daemon.PollUntil(2*time.Second, func() bool {
		_, err := os.Stat(sockPath)
		return os.IsNotExist(err)
	}) {
		return true
	}
	slog.Error("Daemon did not exit within timeout", "sockPath", sockPath)
	return false
}

// Returns the sock path it targeted ("" when disabled) so Restart can poll for cleanup, and whether
// Shutdown was sent — the success message is the caller's (Stop "stopped", Restart "restarted").
func stop(bldRoot string, daemonDisabled bool, out io.Writer) (string, bool) {
	sockPath := sockPath(bldRoot, daemonDisabled)
	if sockPath == "" {
		fmt.Fprintln(out, "Daemon disabled, nothing to stop")
		return "", false
	}
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		slog.Info("Stop: no daemon to stop", "sockPath", sockPath)
		fmt.Fprintln(out, "Daemon was not running")
		// Not redundant with Serve's stale probe: a leftover sock makes Restart poll to timeout.
		// Plain Remove, no live-probe: the dial above just failed, so the sock is stale.
		if os.Remove(sockPath) == nil {
			slog.Info("Stale socket removed", "sockPath", sockPath)
		}
		return sockPath, false
	}
	client := rpc.NewClient(conn)
	defer client.Close()

	var reply Util.Nothing
	if err := client.Call("Watcher.Shutdown", Util.Nothing{}, &reply); err != nil {
		slog.Error("Watcher.Shutdown RPC failed", "err", err)
		fmt.Fprintf(out, "Failed to stop daemon: %v\n", err)
		return sockPath, false
	}
	slog.Info("Sent Watcher.Shutdown", "sockPath", sockPath)
	return sockPath, true
}

func (cl *Client) GetSrcHash(srcDir string) string {
	if cl.daemonClient == nil {
		return ""
	}
	var hash string
	if err := cl.daemonClient.Call("Watcher.GetSrcHash", srcDir, &hash); err != nil {
		slog.Warn("Watcher.GetSrcHash failed; falling back", "dir", srcDir, "err", err)
		return ""
	}
	return hash
}

func (cl *Client) SetSrcHash(srcDir, hash string) {
	if cl.daemonClient == nil {
		return
	}
	var reply Util.Nothing
	call := cl.daemonClient.Go("Watcher.SetSrcHash",
		Watcher.SetSrcHashArgs{Path: srcDir, Hash: hash}, &reply, make(chan *rpc.Call, 1))
	go logRpcError(call, "Watcher.SetSrcHash", "dir", srcDir, "hash", hash)
}

func (cl *Client) GetDirtyHash(srcDir string) string {
	if cl.daemonClient == nil {
		return ""
	}
	var hash string
	if err := cl.daemonClient.Call("Watcher.GetDirtyHash", srcDir, &hash); err != nil {
		slog.Warn("Watcher.GetDirtyHash failed; falling back", "dir", srcDir, "err", err)
		return ""
	}
	return hash
}

func (cl *Client) SetDirtyHash(srcDir, hash string) {
	if cl.daemonClient == nil {
		return
	}
	var reply Util.Nothing
	call := cl.daemonClient.Go("Watcher.SetDirtyHash",
		Watcher.SetDirtyHashArgs{Path: srcDir, Hash: hash}, &reply, make(chan *rpc.Call, 1))
	go logRpcError(call, "Watcher.SetDirtyHash", "dir", srcDir, "hash", hash)
}

func (cl *Client) GetDirtySkipHash(srcDir string) string {
	if cl.daemonClient == nil {
		return ""
	}
	var skipHash string
	if err := cl.daemonClient.Call("Watcher.GetDirtySkipHash", srcDir, &skipHash); err != nil {
		slog.Warn("Watcher.GetDirtySkipHash failed; falling back", "dir", srcDir, "err", err)
		return ""
	}
	return skipHash
}

func (cl *Client) SetDirtySkipHash(srcDir, skipHash string) {
	if cl.daemonClient == nil {
		return
	}
	var reply Util.Nothing
	call := cl.daemonClient.Go("Watcher.SetDirtySkipHash",
		Watcher.SetDirtySkipHashArgs{Path: srcDir, SkipHash: skipHash}, &reply, make(chan *rpc.Call, 1))
	go logRpcError(call, "Watcher.SetDirtySkipHash", "dir", srcDir, "skipHash", skipHash)
}

func (cl *Client) GetBuildConfig(srcDir string) (BuildConfig.BufaConfig, map[string]string, bool) {
	if cl.daemonClient == nil {
		return BuildConfig.BufaConfig{}, make(map[string]string), false
	}
	var reply Watcher.GetBuildConfigReply
	if err := cl.daemonClient.Call("Watcher.GetBuildConfig", srcDir, &reply); err != nil {
		slog.Warn("Watcher.GetBuildConfig failed; falling back", "dir", srcDir, "err", err)
		return BuildConfig.BufaConfig{}, make(map[string]string), false
	}
	return reply.Config, reply.Hashes, reply.Found
}

func (cl *Client) SetBuildConfig(srcDir string, buildConfig BuildConfig.BufaConfig) {
	if cl.daemonClient == nil {
		return
	}
	var reply Util.Nothing
	call := cl.daemonClient.Go("Watcher.SetBuildConfig",
		Watcher.SetBuildConfigArgs{Path: srcDir, Config: buildConfig}, &reply, make(chan *rpc.Call, 1))
	go logRpcError(call, "Watcher.SetBuildConfig", "dir", srcDir)
}

func (cl *Client) GetBuildHash(combined, srcDir string) (string, bool) {
	if cl.daemonClient == nil {
		return "", false
	}
	var reply Watcher.GetBuildHashReply
	if err := cl.daemonClient.Call("Watcher.GetBuildHash",
		Watcher.GetBuildHashArgs{Combined: combined, SrcDir: srcDir}, &reply); err != nil {
		slog.Warn("Watcher.GetBuildHash failed; falling back", "combined", combined, "dir", srcDir, "err", err)
		return "", false
	}
	return reply.BuildHash, reply.PathBuildHashCurrent
}

func (cl *Client) SetBuildHash(combined, buildHash string) {
	if cl.daemonClient == nil {
		return
	}
	var reply Util.Nothing
	call := cl.daemonClient.Go("Watcher.SetBuildHash",
		Watcher.SetBuildHashArgs{Combined: combined, BuildHash: buildHash}, &reply, make(chan *rpc.Call, 1))
	go logRpcError(call, "Watcher.SetBuildHash", "combined", combined, "hash", buildHash)
}

func (cl *Client) SetPathBuildHash(srcDir, combined string) {
	if cl.daemonClient == nil {
		return
	}
	var reply Util.Nothing
	call := cl.daemonClient.Go("Watcher.SetPathBuildHash",
		Watcher.SetPathBuildHashArgs{SrcDir: srcDir, Combined: combined}, &reply, make(chan *rpc.Call, 1))
	go logRpcError(call, "Watcher.SetPathBuildHash", "dir", srcDir, "combined", combined)
}

func (cl *Client) GetRootConfig() (BuildConfig.RootConfig, bool) {
	if cl.daemonClient == nil {
		return BuildConfig.RootConfig{}, false
	}
	var reply Watcher.GetRootConfigReply
	if err := cl.daemonClient.Call("Watcher.GetRootConfig", Util.Nothing{}, &reply); err != nil {
		slog.Warn("Watcher.GetRootConfig failed; falling back", "err", err)
		return BuildConfig.RootConfig{}, false
	}
	if reply.BufaVersion != Bufa.Version {
		slog.Warn("Daemon version mismatch", "clientVersion", Bufa.Version, "daemonVersion", reply.BufaVersion)
		c.Assert(cl.restartDaemon != nil, "restartDaemon must be set on a connected Client")
		cl.restartDaemon(fmt.Sprintf("Restarting Daemon version '%s', as ours is '%s'", reply.BufaVersion, Bufa.Version))
		return BuildConfig.RootConfig{}, false
	}
	return reply.Config, reply.Found
}

func (cl *Client) SetRootConfig(cfg BuildConfig.RootConfig) {
	if cl.daemonClient == nil {
		return
	}
	var reply Util.Nothing
	call := cl.daemonClient.Go("Watcher.SetRootConfig", cfg, &reply, make(chan *rpc.Call, 1))
	go logRpcError(call, "Watcher.SetRootConfig")
}

// Drains call.Done so async fire-and-forget errors stay visible without blocking.
func logRpcError(call *rpc.Call, method string, attrs ...any) {
	if rc := <-call.Done; rc.Error != nil {
		slog.Error(method+" failed", append(attrs, "err", rc.Error)...)
	}
}
