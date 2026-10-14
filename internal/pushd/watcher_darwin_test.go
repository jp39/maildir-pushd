//go:build darwin && cgo

package pushd

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestNativeWatcherLargeMaildir(t *testing.T) {
	root := t.TempDir()
	mkdirMaildir(t, filepath.Join(root, "INBOX"))
	cur := filepath.Join(root, "INBOX", "cur")
	for i := 0; i < 2000; i++ {
		if err := os.WriteFile(filepath.Join(cur, fmt.Sprintf("old-%d:2,", i)), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := newWatcher(root)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err := watcher.Add(cur); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after)-len(before) > 32 {
		t.Fatalf("opened %d descriptors for a directory watch", len(after)-len(before))
	}

	arrival := filepath.Join(root, "INBOX", "new", "arrival")
	if err := os.WriteFile(arrival, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(cur, "arrival:2,")
	if err := os.Rename(arrival, target); err != nil {
		t.Fatal(err)
	}
	waitNativeEvent(t, watcher, target, fsnotify.Create)
	renamed := filepath.Join(cur, "arrival,U=10:2,S")
	if err := os.Rename(target, renamed); err != nil {
		t.Fatal(err)
	}
	waitNativeEvent(t, watcher, renamed, fsnotify.Create)
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	waitNativeEvent(t, watcher, renamed, fsnotify.Remove)
}

func waitNativeEvent(t *testing.T, watcher *mailWatcher, path string, op fsnotify.Op) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case event := <-watcher.Events:
			if event.Name == path && event.Op&op != 0 {
				return
			}
		case err := <-watcher.Errors:
			t.Fatal(err)
		case <-timeout.C:
			t.Fatalf("did not receive %v for %s", op, path)
		}
	}
}
