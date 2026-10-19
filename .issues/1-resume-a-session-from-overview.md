# Resume a session from overview

## Context

Since 2026-10-18 overview's sessions run in its own tmux server (socket
`overview`). They survive quitting the view, closing Ghostty and detaching,
but not a reboot, a logout or `tmux -L overview kill-server`. When the server
goes, the conversations stay on disk under the config directory, and
`claude --resume` (or `-c`) can pick them up. Overview has no way to do that
yet: `c` only starts a fresh `claude`.

A resumed session should behave like one started with `c`. It runs in a
parked pane with `CLAUDE_CONFIG_DIR` set, shows on the stage, gets the
keyboard, and the cursor moves onto it once `claude agents` lists it
(`start` and `adopt` in `internal/tui`).

## Acceptance criteria

- [ ] From the view, a key resumes a past session of this config directory in
      overview's tmux, in that session's own directory.
- [ ] The resumed session is shown and focused, as `c` does now.
- [ ] A session that is already running is not resumed a second time.
- [ ] Tested through the `multiplexer` fake, like the `c` path.

## Open questions

- How to pick the session: let `claude --resume` show its own picker in the
  new pane, or list recent transcripts in overview (they are already read for
  titles and the digest) and resume by id.
- Which key to use. `r` is refresh now.
- Whether to offer a one-off "resume everything that was running" after a
  reboot, which would need a record of what was running.

## Log
