package pushd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func TestMailboxForCurFile(t *testing.T) {
	root := t.TempDir()
	mkdirMaildir(t, filepath.Join(root, "INBOX"))
	mkdirMaildir(t, filepath.Join(root, "INBOX", "Lists"))

	d := &daemon{
		opts: Options{Root: root, Source: "INBOX"},
		curDirs: map[string]struct{}{
			filepath.Join(root, "INBOX", "cur"):          {},
			filepath.Join(root, "INBOX", "Lists", "cur"): {},
		},
	}

	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{filepath.Join(root, "INBOX", "cur", "m:2,"), "INBOX", true},
		{filepath.Join(root, "INBOX", "Lists", "cur", "m:2,S"), "INBOX/Lists", true},
		{filepath.Join(root, "INBOX", "new", "m"), "", false},
		{filepath.Join(root, "INBOX", "tmp", "m"), "", false},
	}

	for _, tt := range tests {
		got, ok := d.mailboxForCurFile(tt.path)
		if ok != tt.ok || got != tt.want {
			t.Fatalf("mailboxForCurFile(%q) = %q, %v; want %q, %v",
				tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRefreshWatchesFindsCurDirs(t *testing.T) {
	root := t.TempDir()
	mkdirMaildir(t, filepath.Join(root, "INBOX"))
	mkdirMaildir(t, filepath.Join(root, "INBOX", "Review"))
	if err := os.MkdirAll(filepath.Join(root, "not-maildir", "cur"), 0700); err != nil {
		t.Fatal(err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	d := &daemon{
		opts:    Options{Root: root},
		watcher: watcher,
		watched: map[string]struct{}{},
		curDirs: map[string]struct{}{},
	}

	if err := d.refreshWatches(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		filepath.Join(root, "INBOX", "cur"),
		filepath.Join(root, "INBOX", "Review", "cur"),
	}
	for _, path := range want {
		if _, ok := d.curDirs[path]; !ok {
			t.Fatalf("cur dir %s was not discovered; got %v", path, d.curDirs)
		}
	}
	if _, ok := d.curDirs[filepath.Join(root, "not-maildir", "cur")]; ok {
		t.Fatal("non-maildir cur directory was discovered")
	}
}

func mkdirMaildir(t *testing.T, path string) {
	t.Helper()
	for _, name := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(path, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
}
