package pushd

import (
	"errors"
	"github.com/fsnotify/fsnotify"
)

var errWatchRescan = errors.New("filesystem events require rescan")
var errWatchRootChanged = errors.New("watch root changed")

// mailWatcher keeps platform-specific filesystem resources behind the event API.
type mailWatcher struct {
	Events <-chan fsnotify.Event
	Errors <-chan error
	add    func(string) error
	close  func() error
}

func (w *mailWatcher) Add(path string) error { return w.add(path) }
func (w *mailWatcher) Close() error          { return w.close() }
