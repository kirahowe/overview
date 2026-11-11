# Remove the socket files the e2e suite leaves behind

## Context

Each run of `scripts/e2e` leaves socket files named `ov-<hash>.sock` in
tmux's socket directory. On Kira's machine that is
`/private/tmp/tmux-501`, which also holds the live `overview` socket. On
2026-11-11 two runs left 22 of them, 11 each.

The files are the view's socket for its agents' pane. `agentsSocket` in
`internal/tui/relay.go` puts it beside tmux's socket and names it from a
hash of that socket's path and the session name. `listenAgents` turns
off unlink on close, so that a view which ends after the next one has
started does not remove the new view's socket. The file stays until a
later view at the same path replaces it. For the real server the path
never changes, so there is only ever one file.

The e2e servers run under `tmux -L ovtest-e2e-<tag><pid>`. Their sockets
land in the same directory, and the pid in the name makes every run's
paths new, so nothing replaces the files. The cleanup in `private()` in
`e2e/e2e_test.go` kills the server and removes tmux's socket file. It
does not remove the view's.

Nothing listens on the files once a run has ended, and macOS empties
`/private/tmp` on restart, so they do no harm. They do pile up between
restarts in the directory that holds the live server's sockets. The live
view's own socket matches `ov-*.sock` too, so no cleanup may remove
files by that pattern.

## Acceptance criteria

- [ ] A run of `scripts/e2e` leaves tmux's socket directory with the
      same files it had before the run.
- [ ] The suite removes only files it can name. It never removes by the
      `ov-*.sock` pattern, and it never touches the `overview` socket or
      the socket of a view it did not start.
- [ ] The suite checks this itself, so a new leak fails the run.

## Plan

Two ways to do it, not yet chosen:

- Export the socket's name from `internal/tui` and have `private()`
  remove the view's socket for each session the test started, next to
  tmux's own file.
- Give the e2e servers a directory of their own, as `internal/tmuxtest`
  does, and remove the directory. `--socket` takes a name today, which
  tmux resolves in its default directory, so this needs a way to say
  where that directory is.

## Log
