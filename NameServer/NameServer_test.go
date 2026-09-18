package NameServer

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Short socket name, to stay under the ~108-byte AF_UNIX path limit.
func sockIn(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "ns")
}

func TestTryStart_ClaimsSocket(t *testing.T) {
	sock := sockIn(t)
	stop := tryStartAt(sock)
	defer stop()

	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not created at %s: %v", sock, err)
	}
}

func TestTryStart_LiveLoserReturnsNil(t *testing.T) {
	sock := sockIn(t)
	stop1 := tryStartAt(sock)
	if stop1 == nil {
		t.Fatal("first tryStartAt returned nil; expected real cleanup")
	}
	defer stop1()

	if stop2 := tryStartAt(sock); stop2 != nil {
		t.Fatal("loser tryStartAt returned non-nil cleanup; expected nil")
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket vanished after loser bailed: %v", err)
	}
}

func TestResolve_RoundTrip(t *testing.T) {
	// Pure cache: the server never touches the FS, so no marker/mkdir needed.
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	sub := filepath.Join(root, "a", "b")

	sock := sockIn(t)
	stop := tryStartAt(sock)
	defer stop()

	setSrcRootAt(sock, sub, root)

	got, alive := getSrcRootAt(sock, sub)
	if got != root {
		t.Fatalf("Resolve(%q) = %q, want %q", sub, got, root)
	}
	if !alive {
		t.Fatalf("alive should be true when daemon answered")
	}

	got, alive = getSrcRootAt(sock, sub)
	if got != root {
		t.Fatalf("cached Resolve = %q, want %q", got, root)
	}
	if !alive {
		t.Fatalf("alive should be true on cached call")
	}
}

func TestSetSrcRoot_CachesAllAncestors(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	deep := filepath.Join(root, "a", "b", "c", "d")

	sock := sockIn(t)
	stop := tryStartAt(sock)
	defer stop()

	setSrcRootAt(sock, deep, root)

	for _, dir := range []string{
		deep,
		filepath.Join(root, "a", "b", "c"),
		filepath.Join(root, "a", "b"),
		filepath.Join(root, "a"),
		root,
	} {
		if got, _ := getSrcRootAt(sock, dir); got != root {
			t.Errorf("ancestor Resolve(%q) = %q, want %q (cached by SetSrcRoot)", dir, got, root)
		}
	}
}

func TestResolve_Miss(t *testing.T) {
	cwd := t.TempDir()
	cwd, _ = filepath.EvalSymlinks(cwd)

	sock := sockIn(t)
	stop := tryStartAt(sock)
	defer stop()

	got, alive := getSrcRootAt(sock, cwd)
	if got != "" {
		t.Fatalf("Resolve with empty cache should return empty srcRoot, got %q", got)
	}
	if !alive {
		t.Fatalf("alive should be true: daemon answered, miss is on lookup not dial")
	}
}

func TestResolve_NoDaemonReturnsNotAlive(t *testing.T) {
	sock := sockIn(t) // never started; dial will fail
	got, alive := getSrcRootAt(sock, t.TempDir())
	if got != "" {
		t.Fatalf("Resolve against missing daemon should return empty srcRoot, got %q", got)
	}
	if alive {
		t.Fatalf("alive should be false when daemon never started")
	}
}

func TestStop_StopsServer(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)

	sock := sockIn(t)
	stop := tryStartAt(sock)
	defer stop()

	setSrcRootAt(sock, filepath.Join(root, "a"), root)

	var out bytes.Buffer
	stopAt(sock, &out)
	if got := out.String(); got != "Name server stopped\n" {
		t.Fatalf("stopAt output = %q, want 'Name server stopped'", got)
	}
	stop() // Shutdown is fire-and-forget; the once makes this a join on the in-flight cleanup
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket still present after stop: %v", err)
	}
	if _, alive := getSrcRootAt(sock, root); alive {
		t.Fatal("server still answering after stop")
	}
}

func TestStop_NotRunning(t *testing.T) {
	sock := sockIn(t) // never started; dial will fail
	var out bytes.Buffer
	stopAt(sock, &out)
	if got := out.String(); got != "Name server was not running\n" {
		t.Fatalf("stopAt output = %q, want 'Name server was not running'", got)
	}
}

func TestStop_RemovesStaleSocket(t *testing.T) {
	sock := sockIn(t)
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	stopAt(sock, &out)
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("stale socket not removed: %v", err)
	}
}

// The daemon-reclaim path after an external stop: without the once guard, the spent first
// cleanup would os.Remove the re-claimed server's socket.
func TestStop_SocketReclaimable(t *testing.T) {
	sock := sockIn(t)
	spent := tryStartAt(sock)
	var out bytes.Buffer
	stopAt(sock, &out)
	spent() // join the async cleanup so the socket is free before re-claiming

	stop2 := tryStartAt(sock)
	if stop2 == nil {
		t.Fatal("tryStartAt after stop returned nil; expected re-claim")
	}
	defer stop2()

	spent() // must not disturb the re-claimed server (the once guard)
	if _, alive := getSrcRootAt(sock, t.TempDir()); !alive {
		t.Fatal("re-claimed server not answering (spent cleanup disturbed it)")
	}
}

// The third stopAt outcome: dial succeeds but the RPC fails (e.g. a pre-Shutdown bufa version
// owns NS). The sock must survive — it may be a live server's.
func TestStop_RpcFailureKeepsSocket(t *testing.T) {
	sock := sockIn(t)
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.Close() // not an RPC server: every call fails
		}
	}()

	var out bytes.Buffer
	stopAt(sock, &out)
	if got := out.String(); !strings.HasPrefix(got, "Failed to stop name server: ") {
		t.Fatalf("stopAt output = %q, want 'Failed to stop name server: ...'", got)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket removed on RPC failure: %v", err)
	}
}

func TestTryStart_StaleSocketRecovers(t *testing.T) {
	sock := sockIn(t)
	// Plant a non-socket file at the path; tryListen must Dial-probe, Remove, and re-Listen.
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	stop := tryStartAt(sock)
	defer stop()

	if _, alive := getSrcRootAt(sock, t.TempDir()); !alive {
		t.Fatal("Resolve after stale-socket recovery should reach a live listener")
	}
}
