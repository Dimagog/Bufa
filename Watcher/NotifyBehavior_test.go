// Pin down the rjeczalik/notify behavior that justifies newWatcher's parent-dir watchpoint: a
// recursive subtree watch reports events INSIDE srcDir but NOT a rename/delete of srcDir itself,
// while a non-recursive watch on the parent does. Uses notify directly, so a future upgrade that
// changes this fails here. Gated on BUFA_NOTIFY_BEHAVIOR (each case waits a full timeout):
//
//	$env:BUFA_NOTIFY_BEHAVIOR=1; go test ./Watcher/ -run TestNotify_
package Watcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rjeczalik/notify"
)

const notifyBehaviorEnv = "BUFA_NOTIFY_BEHAVIOR"

func skipUnlessNotifyBehavior(t *testing.T) {
	t.Helper()
	if os.Getenv(notifyBehaviorEnv) == "" {
		t.Skipf("set %s=1 to run notify-behavior probes", notifyBehaviorEnv)
	}
}

func srcDirSelfEventArrives(t *testing.T,
	watch func(srcDir string, ev chan notify.EventInfo) error,
	mutate func(srcDir string) error,
	timeout time.Duration,
) bool {
	t.Helper()
	parent := t.TempDir()
	parent, _ = filepath.EvalSymlinks(parent)
	srcDir := filepath.Join(parent, "proj")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	events := make(chan notify.EventInfo, eventChanBufSize)
	if err := watch(srcDir, events); err != nil {
		t.Fatalf("notify.Watch: %v", err)
	}
	defer notify.Stop(events)
	time.Sleep(50 * time.Millisecond) // let the watch settle

	if err := mutate(srcDir); err != nil {
		t.Fatalf("mutate srcDir: %v", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		select {
		case ev := <-events:
			if strings.EqualFold(ev.Path(), srcDir) {
				return true
			}
		case <-time.After(time.Until(deadline)):
			return false
		}
	}
}

func watchSubtree(srcDir string, ev chan notify.EventInfo) error {
	return notify.Watch(srcDir+"/...", ev, notify.All)
}

func watchParent(srcDir string, ev chan notify.EventInfo) error {
	return notify.Watch(filepath.Dir(srcDir), ev, notify.Remove|notify.Rename)
}

func removeSrcDir(srcDir string) error { return os.Remove(srcDir) }

func renameSrcDir(srcDir string) error {
	return os.Rename(srcDir, srcDir+"-renamed")
}

func TestNotify_SubtreeWatch_MissesSrcDirSelfRemove(t *testing.T) {
	skipUnlessNotifyBehavior(t)
	if srcDirSelfEventArrives(t, watchSubtree, removeSrcDir, time.Second) {
		t.Fatal("recursive subtree watch reported srcDir-self remove; " +
			"parent watch in newWatcher may now be redundant")
	}
}

func TestNotify_SubtreeWatch_MissesSrcDirSelfRename(t *testing.T) {
	skipUnlessNotifyBehavior(t)
	if srcDirSelfEventArrives(t, watchSubtree, renameSrcDir, time.Second) {
		t.Fatal("recursive subtree watch reported srcDir-self rename; " +
			"parent watch in newWatcher may now be redundant")
	}
}

func TestNotify_ParentWatch_CatchesSrcDirSelfRemove(t *testing.T) {
	skipUnlessNotifyBehavior(t)
	if !srcDirSelfEventArrives(t, watchParent, removeSrcDir, 2*time.Second) {
		t.Fatal("parent watch did not report srcDir-self remove")
	}
}

func TestNotify_ParentWatch_CatchesSrcDirSelfRename(t *testing.T) {
	skipUnlessNotifyBehavior(t)
	if !srcDirSelfEventArrives(t, watchParent, renameSrcDir, 2*time.Second) {
		t.Fatal("parent watch did not report srcDir-self rename")
	}
}
