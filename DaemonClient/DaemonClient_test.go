package DaemonClient

import (
	"bytes"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"testing"

	"github.com/dimagog/bufa"
	"github.com/dimagog/bufa/BuildConfig"
	"github.com/dimagog/bufa/Watcher"
	"github.com/dimagog/bufa/internal/Util"
)

type stubWatcher struct {
	version string
}

func (s *stubWatcher) GetRootConfig(_ Util.Nothing, reply *Watcher.GetRootConfigReply) error {
	reply.Found = true
	reply.BufaVersion = s.version
	return nil
}

// A pre-versioning daemon's reply shape: no Version field, so the client decodes "".
type OldGetRootConfigReply struct {
	Found  bool
	Config BuildConfig.RootConfig
}

type oldStubWatcher struct{}

func (s *oldStubWatcher) GetRootConfig(_ Util.Nothing, reply *OldGetRootConfigReply) error {
	reply.Found = true
	return nil
}

func newStubClient(t *testing.T, svc any) *Client {
	t.Helper()
	srv := rpc.NewServer()
	if err := srv.RegisterName("Watcher", svc); err != nil {
		t.Fatal(err)
	}
	cliConn, srvConn := net.Pipe()
	go srv.ServeConn(srvConn)
	rpcClient := rpc.NewClient(cliConn)
	t.Cleanup(func() { rpcClient.Close() })
	return &Client{daemonClient: rpcClient}
}

func withRestart(c *Client) func() bool {
	restarted := false
	c.restartDaemon = func(string) { restarted = true }
	return func() bool { return restarted }
}

// A stale sock (daemon killed, unlink defer skipped) used to make Restart poll its full timeout
// and never print "Daemon restarted".
func TestRestart_StaleSockRemoved(t *testing.T) {
	bldRoot := t.TempDir()
	sock := filepath.Join(bldRoot, SockName)
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	Restart(bldRoot, false /*daemonDisabled*/, &out)

	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("stale sock not removed")
	}
	if !bytes.Contains(out.Bytes(), []byte("Daemon restarted")) {
		t.Fatalf("expected 'Daemon restarted', got: %q", out.String())
	}
}

func TestGetRootConfig_VersionMatch(t *testing.T) {
	c := newStubClient(t, &stubWatcher{version: Bufa.Version})
	restarted := withRestart(c)
	if _, found := c.GetRootConfig(); !found {
		t.Fatal("expected found")
	}
	if restarted() {
		t.Fatal("matching version triggered restart")
	}
}

func TestGetRootConfig_VersionMismatchRestartsDaemon(t *testing.T) {
	c := newStubClient(t, &stubWatcher{version: "0.0.0"})
	restarted := withRestart(c)
	if _, found := c.GetRootConfig(); found {
		t.Fatal("mismatched daemon's config must not be trusted")
	}
	if !restarted() {
		t.Fatal("version mismatch did not trigger restart")
	}
}

func TestGetRootConfig_PreVersioningDaemonRestartsDaemon(t *testing.T) {
	c := newStubClient(t, &oldStubWatcher{})
	restarted := withRestart(c)
	if _, found := c.GetRootConfig(); found {
		t.Fatal("pre-versioning daemon's config must not be trusted")
	}
	if !restarted() {
		t.Fatal("pre-versioning daemon did not trigger restart")
	}
}
