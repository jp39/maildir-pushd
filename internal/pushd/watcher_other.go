//go:build !darwin || !cgo

package pushd

import "github.com/fsnotify/fsnotify"

func newWatcher(root string) (*mailWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &mailWatcher{Events: watcher.Events, Errors: watcher.Errors, add: watcher.Add, close: watcher.Close}, nil
}
