package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jp39/maildir-pushd/internal/pushd"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}

	var opts pushd.Options
	var showDefaultFilter bool
	flag.StringVar(&opts.Root, "maildir", filepath.Join(home, ".mail"), "Maildir root")
	flag.StringVar(&opts.Source, "source", "INBOX", "source mailbox to filter when files appear in cur/")
	flag.StringVar(&opts.ScriptPath, "script", filepath.Join(home, ".config", "maildir-sieve", "filter.sieve"), "Sieve script used by the default filter command")
	flag.StringVar(&opts.FilterCmd, "filter-cmd", pushd.DefaultFilterCmd(), "shell command for filtering source cur/ files; use - to disable")
	flag.StringVar(&opts.PushCmd, "push-cmd", "", "shell command to push local changes, for example 'mbsync --push oracle'; empty disables push")
	flag.StringVar(&opts.LockFile, "lock-file", "", "optional flock file shared with other mail sync commands")
	flag.DurationVar(&opts.Debounce, "debounce", 5*time.Second, "quiet period before filtering and pushing")
	flag.DurationVar(&opts.RescanInterval, "rescan-interval", time.Minute, "interval for discovering newly created Maildir folders")
	flag.DurationVar(&opts.FilterTimeout, "filter-timeout", 2*time.Minute, "timeout for one filter command")
	flag.DurationVar(&opts.PushTimeout, "push-timeout", 5*time.Minute, "timeout for the push command")
	flag.BoolVar(&showDefaultFilter, "print-default-filter-cmd", false, "print the default filter command and exit")
	flag.Parse()

	if showDefaultFilter {
		fmt.Println(pushd.DefaultFilterCmd())
		return
	}

	opts.Out = os.Stdout
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = pushd.Run(ctx, opts)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "maildir-pushd:", err)
		os.Exit(1)
	}
}
