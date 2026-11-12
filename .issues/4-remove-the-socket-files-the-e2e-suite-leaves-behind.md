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

- [x] A run of `scripts/e2e` leaves tmux's socket directory with the
      same files it had before the run.
  - Receipt: a full run on 2026-11-12 passed, and `ls
    /private/tmp/tmux-501` listed the same 61 files before and after.
- [x] The suite removes only files it can name. It never removes by the
      `ov-*.sock` pattern, and it never touches the `overview` socket or
      the socket of a view it did not start.
  - Receipt: the cleanup in `private()` removes tmux's socket file and
    `tui.AgentsSocket(path, session)` for the sessions it was given,
    under a server name it has checked starts with `ovtest-e2e-`.
    Nothing in `e2e/` removes by pattern.
- [x] The suite fails when the view's socket is not where its cleanup
      looks for it.
  - Receipt: with the check pointed at a wrong session name,
    `TestReview` failed with "timed out after 30s waiting for the view's
    socket for its agents' pane", and the directory was unchanged.

## Plan

There were two ways to do it:

- Export the socket's name from `internal/tui` and have `private()`
  remove the view's socket for each session the test started, next to
  tmux's own file.
- Give the e2e servers a directory of their own, as `internal/tmuxtest`
  does, and remove the directory. `--socket` takes a name today, which
  tmux resolves in its default directory, so this needs a way to say
  where that directory is.

The first is the one taken. `tui.AgentsSocket` gives the name.
`private()` is told which sessions get a view and removes each one's
socket after it kills the server. `launchWith` waits for the socket at
that name once the view is up.

The second needs either `--socket` to take a path, which changes the
product for the tests' sake, or `TMUX_TMPDIR`, and tests here do not
isolate themselves through the environment.

## Log

2026-11-12: fixed. A full run of `scripts/e2e` left the directory's 61
files as they were. With the check pointed at a wrong name, `TestReview`
failed after its 30 second wait and still left nothing behind.

The check does not catch a new kind of file beside tmux's socket.
Comparing the directory before and after a run cannot be the check,
because other runs and the live view change it too.
