package Daemon

import (
	"bytes"
	"fmt"
	"net"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	c "github.com/dimagog/bufa/internal/contract"
)

var daemonBinPath string

func TestMain(m *testing.M) { os.Exit(runMain(m)) }

func runMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "daemon-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		return 1
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "daemon")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, "github.com/dimagog/bufa/cmd/daemon")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build cmd/daemon: %v\n%s\n", err, out)
		return 1
	}
	daemonBinPath = bin
	return m.Run()
}

// Short socket name, to stay under the ~108-byte AF_UNIX path limit.
func sockIn(t *testing.T) (dir, sock string) {
	t.Helper()
	dir = t.TempDir()
	return dir, filepath.Join(dir, "s")
}

func runClient(sock, idle string) ([]byte, error) {
	args := []string{sock}
	if idle != "" {
		args = append(args, idle)
	}
	cmd := exec.Command(daemonBinPath, args...)
	return cmd.CombinedOutput()
}

// Each pid file is one live daemon (removed when the daemon exits cleanly).
func pidFiles(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "daemon-*.pid"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return files
}

func TestColdStart(t *testing.T) {
	_, sock := sockIn(t)
	out, err := runClient(sock, "")
	if err != nil {
		t.Fatalf("client: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("sum: 5")) {
		t.Fatalf("expected 'sum: 5', got: %q", out)
	}
}

func TestWarmReuse(t *testing.T) {
	dir, sock := sockIn(t)

	if out, err := runClient(sock, "10s"); err != nil {
		t.Fatalf("client 1: %v\n%s", err, out)
	}
	files1 := pidFiles(t, dir)
	if len(files1) != 1 {
		t.Fatalf("after run 1, want 1 daemon PID file, got %d: %v", len(files1), files1)
	}

	if out, err := runClient(sock, "10s"); err != nil {
		t.Fatalf("client 2: %v\n%s", err, out)
	}
	files2 := pidFiles(t, dir)
	if len(files2) != 1 {
		t.Fatalf("after run 2, want 1 daemon PID file, got %d: %v", len(files2), files2)
	}
	if files1[0] != files2[0] {
		t.Fatalf("daemon PID changed: %s → %s", files1[0], files2[0])
	}
}

func TestConcurrent(t *testing.T) {
	dir, sock := sockIn(t)

	const N = 10
	const idleStr = "1s"
	const idle = 1 * time.Second
	var wg sync.WaitGroup
	errs := make([]error, N)
	outs := make([][]byte, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outs[i], errs[i] = runClient(sock, idleStr)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("client %d: %v\n%s", i, err, outs[i])
		}
	}
	if t.Failed() {
		return
	}

	// idle + slack — orphans from the bind/listen race must have hit their idle deadline.
	time.Sleep(idle + 2*time.Second)
	left := pidFiles(t, dir)
	if len(left) > 1 {
		t.Fatalf("want ≤1 daemon after settle, got %d: %v", len(left), left)
	}
}

func TestStaleSocket(t *testing.T) {
	_, sock := sockIn(t)
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runClient(sock, "")
	if err != nil {
		t.Fatalf("client: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("sum: 5")) {
		t.Fatalf("expected 'sum: 5', got: %q", out)
	}
}

func TestMissingSockDir(t *testing.T) {
	dir, _ := sockIn(t)
	sock := filepath.Join(dir, "d", "s")

	const idle = 200 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		done <- c.Rescue(func() {
			Serve(sock, idle, func(*rpc.Server, func()) {})
		})
	}()

	if !PollUntil(2*time.Second, func() bool {
		c, err := net.Dial("unix", sock)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}) {
		t.Fatal("Serve did not start listening")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve panicked: %v", err)
		}
	case <-time.After(idle + 2*time.Second):
		t.Fatal("Serve did not exit within idle + slack")
	}
}

func TestIdleShutdown(t *testing.T) {
	_, sock := sockIn(t)

	const idle = 200 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		done <- c.Rescue(func() {
			Serve(sock, idle, func(*rpc.Server, func()) {})
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	conn, _ := Connect(sock)
	conn.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve panicked: %v", err)
		}
	case <-time.After(idle + 2*time.Second):
		t.Fatal("Serve did not exit within idle + slack")
	}
}
