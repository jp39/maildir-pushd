package pushd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

const changedExitCode = 2

type Options struct {
	Root           string
	Source         string
	ScriptPath     string
	FilterCmd      string
	PushCmd        string
	LockFile       string
	Debounce       time.Duration
	RescanInterval time.Duration
	FilterTimeout  time.Duration
	PushTimeout    time.Duration
	Out            io.Writer
}

type filterCandidate struct {
	Path    string
	Mailbox string
}

type batch struct {
	Filters []filterCandidate
	Push    bool
}

type daemon struct {
	opts           Options
	watcher        *mailWatcher
	watched        map[string]struct{}
	curDirs        map[string]struct{}
	pendingFilters map[string]filterCandidate
	pendingPush    bool
	sourceSeen     map[string]struct{}
}

func DefaultFilterCmd() string {
	return `maildir-sieve -maildir "$MAILDIR_PUSHD_ROOT" -source "$MAILDIR_PUSHD_SOURCE" -script "$MAILDIR_PUSHD_SCRIPT" -apply -file "$MAILDIR_PUSHD_FILE" -changed-exit-code`
}

func Run(ctx context.Context, opts Options) error {
	var err error
	opts, err = normalizeOptions(opts)
	if err != nil {
		return err
	}

	watcher, err := newWatcher(opts.Root)
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watcher.Close()

	d := &daemon{
		opts:           opts,
		watcher:        watcher,
		watched:        map[string]struct{}{},
		curDirs:        map[string]struct{}{},
		pendingFilters: map[string]filterCandidate{},
	}
	if err := d.refreshWatches(); err != nil {
		return err
	}

	if err := d.scanSource(false); err != nil {
		return err
	}
	d.logf("watching %s", opts.Root)
	return d.loop(ctx)
}

func normalizeOptions(opts Options) (Options, error) {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Root == "" {
		return opts, errors.New("maildir root is required")
	}
	root, err := filepath.Abs(expandHome(opts.Root))
	if err != nil {
		return opts, fmt.Errorf("resolve maildir root: %w", err)
	}
	opts.Root = filepath.Clean(root)
	if opts.Source == "" {
		opts.Source = "INBOX"
	}
	opts.Source = filepath.ToSlash(filepath.Clean(filepath.FromSlash(opts.Source)))
	if opts.ScriptPath == "" {
		opts.ScriptPath = filepath.Join(defaultHome(), ".config", "maildir-sieve", "filter.sieve")
	} else {
		opts.ScriptPath = expandHome(opts.ScriptPath)
	}
	if opts.FilterCmd == "" {
		opts.FilterCmd = DefaultFilterCmd()
	}
	if opts.Debounce == 0 {
		opts.Debounce = 5 * time.Second
	}
	if opts.RescanInterval == 0 {
		opts.RescanInterval = time.Minute
	}
	if opts.FilterTimeout == 0 {
		opts.FilterTimeout = 2 * time.Minute
	}
	if opts.PushTimeout == 0 {
		opts.PushTimeout = 5 * time.Minute
	}
	if opts.LockFile != "" {
		opts.LockFile = expandHome(opts.LockFile)
	}
	return opts, nil
}

func (d *daemon) loop(ctx context.Context) error {
	timer := time.NewTimer(time.Hour)
	stopTimer(timer)
	rescan := time.NewTicker(d.opts.RescanInterval)
	defer rescan.Stop()

	done := make(chan error, 1)
	running := false
	timerFiredWhileRunning := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-d.watcher.Events:
			if !ok {
				return nil
			}
			if d.handleEvent(event) {
				armTimer(timer, d.opts.Debounce)
			}
		case err, ok := <-d.watcher.Errors:
			if !ok {
				return nil
			}
			d.logf("watcher error: %v", err)
			if errors.Is(err, errWatchRootChanged) {
				return err
			}
			if errors.Is(err, errWatchRescan) {
				if scanErr := d.refreshWatches(); scanErr != nil {
					return scanErr
				}
				if scanErr := d.scanSource(true); scanErr != nil {
					return scanErr
				}
				d.pendingPush = true
				armTimer(timer, d.opts.Debounce)
			}
		case <-rescan.C:
			if err := d.refreshWatches(); err != nil {
				d.logf("rescan failed: %v", err)
			}
		case <-timer.C:
			if running {
				timerFiredWhileRunning = true
				continue
			}
			next := d.takeBatch()
			if next.empty() {
				continue
			}
			running = true
			go func() {
				done <- d.runBatch(ctx, next)
			}()
		case err := <-done:
			running = false
			if err != nil && !errors.Is(err, context.Canceled) {
				d.logf("batch failed: %v", err)
			}
			if timerFiredWhileRunning || !d.takePeek().empty() {
				timerFiredWhileRunning = false
				armTimer(timer, d.opts.Debounce)
			}
		}
	}
}

func (d *daemon) refreshWatches() error {
	nextCurDirs := map[string]struct{}{}
	err := filepath.WalkDir(d.opts.Root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			d.logf("walk %s: %v", path, err)
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			return nil
		}

		base := entry.Name()
		if isMaildirSpecial(base) {
			if base == "cur" && isMaildir(filepath.Dir(path)) {
				nextCurDirs[path] = struct{}{}
				return d.addWatch(path)
			}
			return filepath.SkipDir
		}
		return d.addWatch(path)
	})
	if err != nil {
		return fmt.Errorf("scan maildir tree: %w", err)
	}
	d.curDirs = nextCurDirs
	return nil
}

func (d *daemon) addWatch(path string) error {
	path = filepath.Clean(path)
	if _, ok := d.watched[path]; ok {
		return nil
	}
	if err := d.watcher.Add(path); err != nil {
		return fmt.Errorf("watch %s: %w", path, err)
	}
	d.watched[path] = struct{}{}
	d.logf("watch %s", path)
	return nil
}

func (d *daemon) handleEvent(event fsnotify.Event) bool {
	path := filepath.Clean(event.Name)
	if path == "." || strings.HasPrefix(filepath.Base(path), ".") {
		return false
	}

	if event.Op&(fsnotify.Create|fsnotify.Rename) != 0 {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if err := d.refreshWatches(); err != nil {
				d.logf("refresh after directory event failed: %v", err)
			}
			return false
		}
	}

	mailbox, ok := d.mailboxForCurFile(path)
	if !ok {
		return false
	}
	if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
		return false
	}

	d.pendingPush = true
	if event.Op&fsnotify.Create != 0 && mailbox == d.opts.Source && fileExists(path) {
		if d.sourceSeen == nil {
			d.sourceSeen = map[string]struct{}{}
		}
		d.sourceSeen[sourceIdentity(path)] = struct{}{}
		d.pendingFilters[path] = filterCandidate{Path: path, Mailbox: mailbox}
		d.logf("queue filter %s", path)
	}
	return true
}

func (d *daemon) mailboxForCurFile(path string) (string, bool) {
	dir := filepath.Dir(path)
	if filepath.Base(dir) != "cur" {
		return "", false
	}
	if _, ok := d.curDirs[dir]; !ok {
		return "", false
	}
	rel, err := filepath.Rel(d.opts.Root, filepath.Dir(dir))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (d *daemon) takeBatch() batch {
	filters := make([]filterCandidate, 0, len(d.pendingFilters))
	for _, candidate := range d.pendingFilters {
		filters = append(filters, candidate)
	}
	sort.Slice(filters, func(i, j int) bool {
		return filters[i].Path < filters[j].Path
	})
	next := batch{
		Filters: filters,
		Push:    d.pendingPush,
	}
	d.pendingFilters = map[string]filterCandidate{}
	d.pendingPush = false
	return next
}

func (d *daemon) takePeek() batch {
	return batch{
		Filters: make([]filterCandidate, 0, len(d.pendingFilters)),
		Push:    d.pendingPush || len(d.pendingFilters) > 0,
	}
}

func (b batch) empty() bool {
	return !b.Push && len(b.Filters) == 0
}

func (d *daemon) runBatch(ctx context.Context, b batch) error {
	changed := false
	var errs []error
	for _, candidate := range b.Filters {
		if !fileExists(candidate.Path) {
			d.logf("skip disappeared filter candidate %s", candidate.Path)
			continue
		}
		result, err := d.runFilter(ctx, candidate)
		if err != nil {
			d.logf("%v", err)
			errs = append(errs, err)
			continue
		}
		if result {
			changed = true
		}
	}
	if b.Push || changed {
		if err := d.runPush(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *daemon) runFilter(ctx context.Context, candidate filterCandidate) (bool, error) {
	if d.opts.FilterCmd == "-" {
		return false, nil
	}

	env := []string{
		"MAILDIR_PUSHD_ROOT=" + d.opts.Root,
		"MAILDIR_PUSHD_SOURCE=" + d.opts.Source,
		"MAILDIR_PUSHD_SCRIPT=" + d.opts.ScriptPath,
		"MAILDIR_PUSHD_FILE=" + candidate.Path,
		"MAILDIR_PUSHD_MAILBOX=" + candidate.Mailbox,
	}

	d.logf("filter %s", candidate.Path)
	err := d.withLock(ctx, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, d.opts.FilterTimeout)
		defer cancel()
		return runShell(ctx, d.opts.FilterCmd, env, d.opts.Out)
	})
	var exitErr *exec.ExitError
	if err == nil {
		return false, nil
	}
	if errors.As(err, &exitErr) && exitErr.ExitCode() == changedExitCode {
		return true, nil
	}
	return false, fmt.Errorf("filter %s: %w", candidate.Path, err)
}

func (d *daemon) runPush(ctx context.Context) error {
	if d.opts.PushCmd == "" || d.opts.PushCmd == "-" {
		d.logf("push skipped: no push command configured")
		return nil
	}

	d.logf("push start")
	err := d.withLock(ctx, func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, d.opts.PushTimeout)
		defer cancel()
		return runShell(ctx, d.opts.PushCmd, nil, d.opts.Out)
	})
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	d.logf("push end")
	return nil
}

func (d *daemon) withLock(ctx context.Context, fn func(context.Context) error) error {
	if d.opts.LockFile == "" {
		return fn(ctx)
	}
	if err := os.MkdirAll(filepath.Dir(d.opts.LockFile), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(d.opts.LockFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)

	return fn(ctx)
}

func runShell(ctx context.Context, command string, env []string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func isMaildir(path string) bool {
	for _, name := range []string{"cur", "new", "tmp"} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

func isMaildirSpecial(name string) bool {
	return name == "cur" || name == "new" || name == "tmp"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func armTimer(timer *time.Timer, delay time.Duration) {
	stopTimer(timer)
	timer.Reset(delay)
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (d *daemon) logf(format string, args ...any) {
	if d.opts.Out == nil {
		return
	}
	fmt.Fprintf(d.opts.Out, "%s maildir-pushd: %s\n",
		time.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func expandHome(path string) string {
	if path == "~" {
		return defaultHome()
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(defaultHome(), path[2:])
	}
	return path
}

func defaultHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

var sourceUIDMarker = regexp.MustCompile(`,U=\d+`)

func sourceIdentity(path string) string {
	stem, _, _ := strings.Cut(filepath.Base(path), ":")
	return sourceUIDMarker.ReplaceAllString(stem, "")
}

// scanSource recovers missed acknowledgements after an FSEvents overflow.
// Remembering identities avoids rerunning copy rules for historical messages
// or for filenames that changed only in flags or mbsync UID assignment.
func (d *daemon) scanSource(queueNew bool) error {
	if d.sourceSeen == nil {
		d.sourceSeen = map[string]struct{}{}
	}
	sourceCur := filepath.Join(d.opts.Root, filepath.FromSlash(d.opts.Source), "cur")
	entries, err := os.ReadDir(sourceCur)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(sourceCur, entry.Name())
		identity := sourceIdentity(path)
		if _, seen := d.sourceSeen[identity]; queueNew && !seen {
			d.pendingFilters[path] = filterCandidate{Path: path, Mailbox: d.opts.Source}
		}
		d.sourceSeen[identity] = struct{}{}
	}
	return nil
}
