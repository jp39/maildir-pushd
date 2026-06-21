# maildir-pushd

`maildir-pushd` watches local Maildir `cur/` directories, runs a local filter
for newly acknowledged Inbox messages, and coalesces local Maildir changes into
an `mbsync --push` command.

It is meant for Maildir-based IMAP workflows where:

```text
mbsync pulls remote mail into Maildir new/
the MUA is the only component that consumes new/
the MUA moves acknowledged messages into cur/
maildir-pushd reacts to cur/ changes
maildir-pushd asks maildir-sieve to file new Inbox messages
maildir-pushd debounces local changes and runs mbsync --push
```

This keeps Maildir `new/` as a single-consumer handoff to the MUA while still
allowing automatic local filing and eventual push synchronization.

## Status

This project is intentionally small and conservative:

- It watches every discovered Maildir `cur/` directory below the Maildir root.
- It ignores `new/` and `tmp/` for change decisions.
- It watches mailbox parent directories only to discover newly created folders.
- It runs the filter command only for files created in the configured source
  mailbox's `cur/` directory.
- It schedules a push for meaningful `cur/` changes: create, write, remove,
  rename, or chmod.
- It debounces event bursts before running commands.
- It serializes filter commands before the push command within a debounce
  batch.
- It can use an advisory flock file shared with pull scripts.

`maildir-pushd` does not synchronize mail by itself. It runs the commands you
configure, usually `maildir-sieve` for filtering and `mbsync --push` for
publishing local state back to IMAP.

## Installation

Install the latest published version with:

```sh
go install github.com/jp39/maildir-pushd/cmd/maildir-pushd@latest
```

Or build from a checkout with the Go version declared in `go.mod`:

```sh
go install ./cmd/maildir-pushd
```

To write a local binary directly:

```sh
go build -o ~/.local/bin/maildir-pushd ./cmd/maildir-pushd
```

The default filter command expects
[`maildir-sieve`](https://github.com/jp39/maildir-sieve) to be available in
`PATH`.

## Usage

`maildir-pushd` needs an explicit push command:

```sh
maildir-pushd \
  -maildir ~/.mail \
  -source INBOX \
  -script ~/.config/maildir-sieve/filter.sieve \
  -push-cmd 'mbsync --push work' \
  -lock-file /tmp/fetchmail.lock
```

The default filter command is:

```sh
maildir-sieve -maildir "$MAILDIR_PUSHD_ROOT" -source "$MAILDIR_PUSHD_SOURCE" -script "$MAILDIR_PUSHD_SCRIPT" -apply -file "$MAILDIR_PUSHD_FILE" -changed-exit-code
```

Use `-filter-cmd -` to disable filtering and only debounce push syncs:

```sh
maildir-pushd \
  -maildir ~/.mail \
  -filter-cmd - \
  -push-cmd 'mbsync --push work'
```

Use `-push-cmd -` or leave `-push-cmd` empty to run filters without pushing.

## Systemd User Service

A typical user service looks like this:

```ini
[Unit]
Description=Watch Maildir changes and push them to IMAP

[Service]
Type=simple
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=%h/.local/bin/maildir-pushd \
        -maildir %h/.mail \
        -source INBOX \
        -script %h/.config/maildir-sieve/filter.sieve \
        -push-cmd "mbsync --push work" \
        -lock-file /tmp/fetchmail.lock
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=default.target
```

Enable it with:

```sh
systemctl --user daemon-reload
systemctl --user enable --now maildir-pushd.service
```

Follow logs with:

```sh
journalctl --user -f -u maildir-pushd.service
```

## Flags

- `-maildir`: Maildir root. Defaults to `~/.mail`.
- `-source`: source mailbox to filter when files appear in `cur/`. Defaults to
  `INBOX`.
- `-script`: Sieve script path passed to the default filter command. Defaults
  to `~/.config/maildir-sieve/filter.sieve`.
- `-filter-cmd`: shell command used to filter source `cur/` files. Use `-` to
  disable filtering.
- `-push-cmd`: shell command used to push local changes. Empty or `-` disables
  pushing.
- `-lock-file`: optional flock file shared with other mail sync commands.
- `-debounce`: quiet period before running filters and push. Defaults to `5s`.
- `-rescan-interval`: periodic folder discovery interval. Defaults to `1m`.
- `-filter-timeout`: timeout for one filter command. Defaults to `2m`.
- `-push-timeout`: timeout for the push command. Defaults to `5m`.
- `-print-default-filter-cmd`: print the default filter command and exit.

## Environment For Filter Command

The filter command receives:

- `MAILDIR_PUSHD_ROOT`: configured Maildir root.
- `MAILDIR_PUSHD_SOURCE`: configured source mailbox.
- `MAILDIR_PUSHD_SCRIPT`: configured Sieve script path.
- `MAILDIR_PUSHD_FILE`: candidate message file in the source mailbox's `cur/`
  directory.
- `MAILDIR_PUSHD_MAILBOX`: mailbox containing the candidate.

The default command expects `maildir-sieve` to support `-file` and
`-changed-exit-code`. Exit status `2` from the filter command means "message
changed" and is treated as success. Any other non-zero status is logged as a
filter error.

## Locking Model

`-lock-file` is an advisory `flock` lock shared with other mail commands. A
common setup uses the same lock file in:

- a pull script that runs `mbsync --pull work`;
- `maildir-pushd`, which runs filters and `mbsync --push work`.

This keeps multiple pull triggers from competing with each other and prevents
pull and push batches from running at the same time.

## Limitations

- The daemon watches Maildir filesystem state. It does not parse IMAP state.
- It only filters create events in the configured source mailbox's `cur/`
  directory.
- It depends on the MUA consuming `new/` and moving acknowledged messages to
  `cur/`.
- It does not retry a failed filter on its own unless a later filesystem event
  queues the file again.
- It uses shell commands for filtering and pushing, so quote custom commands
  carefully.

## Testing

Run the unit tests:

```sh
go test ./...
```

Build the command:

```sh
go build ./cmd/maildir-pushd
```

## License

`maildir-pushd` is distributed under the MIT license. Its current runtime
dependencies are permissively licensed:

- `github.com/fsnotify/fsnotify`: BSD-3-Clause style license.
- `golang.org/x/sys`: BSD-3-Clause style license.

These licenses do not prevent publishing this project under MIT.
