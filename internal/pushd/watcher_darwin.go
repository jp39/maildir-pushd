//go:build darwin && cgo

package pushd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"
	"github.com/fsnotify/fsnotify"
)

// FSEvents watches the tree recursively without opening every message. The
// kqueue backend used by fsnotify on macOS needs a descriptor for each child
// file, which exceeds process limits for normal-sized corporate mailboxes.
func newWatcher(root string) (*mailWatcher, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	stream := &fsevents.EventStream{
		Paths:   []string{resolved},
		Flags:   fsevents.FileEvents | fsevents.NoDefer | fsevents.WatchRoot,
		Latency: 250 * time.Millisecond,
		Events:  make(chan []fsevents.Event, 128),
	}
	events := make(chan fsnotify.Event, 4096)
	errors := make(chan error, 16)
	done := make(chan struct{})
	stopped := make(chan struct{})
	if err := stream.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer close(stopped)
		defer close(events)
		defer close(errors)
		for {
			select {
			case <-done:
				return
			case batch := <-stream.Events:
				for _, event := range batch {
					if event.Flags&(fsevents.MustScanSubDirs|fsevents.RootChanged) != 0 {
						reason := errWatchRescan
						if event.Flags&fsevents.RootChanged != 0 {
							reason = errWatchRootChanged
						}
						select {
						case errors <- fmt.Errorf("%w: %s", reason, event.Path):
						case <-done:
							return
						}
					}
					path := event.Path
					if !filepath.IsAbs(path) {
						path = "/" + path
					}
					rel, err := filepath.Rel(resolved, path)
					if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						continue
					}
					path = filepath.Join(root, rel)
					var op fsnotify.Op
					if event.Flags&fsevents.ItemCreated != 0 {
						op |= fsnotify.Create
					}
					if event.Flags&fsevents.ItemRemoved != 0 {
						op |= fsnotify.Remove
					}
					if event.Flags&fsevents.ItemModified != 0 {
						op |= fsnotify.Write
					}
					if event.Flags&(fsevents.ItemInodeMetaMod|fsevents.ItemChangeOwner|fsevents.ItemXattrMod) != 0 {
						op |= fsnotify.Chmod
					}
					if event.Flags&fsevents.ItemRenamed != 0 {
						op |= fsnotify.Rename
						if _, err := os.Lstat(path); err == nil {
							op |= fsnotify.Create
						}
					}
					if op == 0 {
						continue
					}
					select {
					case events <- fsnotify.Event{Name: path, Op: op}:
					case <-done:
						return
					}
				}
			}
		}
	}()
	var once sync.Once
	return &mailWatcher{
		Events: events, Errors: errors,
		// The root stream already includes all subdirectories, including ones
		// created later. Keep Add compatible with the directory discovery code.
		add: func(path string) error {
			rel, err := filepath.Rel(root, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("watch path outside root: %s", path)
			}
			return nil
		},
		close: func() error {
			once.Do(func() { close(done); stream.Stop(); <-stopped })
			return nil
		},
	}, nil
}
