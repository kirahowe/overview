# Fix flaky real-tmux tests that race the pane command

## Context

Two tests in `internal/tmux/tmux_test.go` that drive a real tmux server
failed once each during `scripts/check --deep 500` on 2026-10-24, and
passed when rerun:

- `TestALoneCommandHasNoShellBetween` waits for a pane's script to exec
  `sleep` by polling `#{pane_current_command}` 40 times, 25ms apart. That
  is a fixed one-second budget. Under the load of a deep run, with rapid
  property tests running in other packages, the exec can take longer.
- `TestPaneFieldsFromRealTmux` (around line 428) checks
  `p.Command != "sleep"` straight after spawning, with no wait at all.
  tmux reports the foreground process, which can still be the shell or
  `sh` running the command at that point.

Both share one cause: a test asserts on asynchronous tmux state at a
moment it picked itself, not when that state is known to be reached. The
same pattern may sit in other tests that spawn panes and read them back,
in `internal/tmux`, `internal/tmuxtest` users, and `e2e`.

A flaky test is not acceptable. The gate has to fail only when the code
is wrong.

## Acceptance criteria

- [ ] Every real-tmux test waits for the state it checks through one
      shared helper, with a deadline generous enough for a loaded
      machine. The helper reports the last observed value on failure. No
      test asserts on pane commands, paths or deadness right after a
      spawn or respawn without waiting.
- [ ] Every real-tmux test in `internal/tmux`, and the tui tests that use
      `internal/tmuxtest`, has been audited for fixed sleeps and
      immediate reads of async state, and fixed where found.
- [ ] `go test -race -count=50 ./internal/tmux` passes while
      `scripts/check --deep 2000` runs alongside it.
- [ ] `scripts/check --deep 2000` passes three times in a row.

## Plan

## Log
