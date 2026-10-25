# overview

A Go TUI that tracks Claude Code sessions. A hook records each session's state;
the view joins those records with `claude agents --json` and the transcripts.
See README.md for behaviour and docs/ for design notes.

## Build and test

- `scripts/check` is the gate: gofmt, go vet, staticcheck (if installed),
  `go test -race`, and a build to `bin/overview`. It must pass before every
  commit. `scripts/check --deep [n]` also reruns property tests with n
  cases each, 2000 by default.
- A single package: `go test -race ./internal/fleet`.
- `scripts/e2e` builds the binary and drives it in pairs of private tmux
  servers (`ovtest-e2e-<tag><pid>` and `ovtest-e2e-term-<tag><pid>`, the
  second holding the terminals overview is launched in). It covers launch
  and the holder; the rule along each pane's top and its title; `c` and
  the list moving into a session's window; splits staying with their
  window and the list keeping its dragged width; `j` moving the
  cursor with the keyboard back in the list; a
  relaunch restarting the view for new flags, a theme given as a relative
  path and a changed config file, and only attaching otherwise; `t`, a
  terminal's place in the list and the keyboard moving into it; a session
  that exits beside a split leaving its window as a terminal; `x` closing
  a terminal with the list in it; pinned projects, `c` in one and `a` with
  a typed path and `tab`; the list staying put while a terminal becomes a
  session under the keyboard; and `q` detaching only the terminal it was
  typed in; and a review, from `v` through a note sent into a fake
  session's prompt, the agent's reply and `x` resolving it; and a
  workstream, from a project's issue in progress through `c` starting a
  session with the prompt that names it and `t` listing a terminal under
  it, and `s` zooming the list with an issue's details beside the
  picker; and `o` zooming the list into the cockpit and `esc` putting it
  back. It builds only
  with `-tags e2e`, so the gate skips it; run it after changing launch,
  the view, tmux handling or reviews.
- Never point a test or a trial run at the `overview` socket, not even to
  read it. Use `--socket <name>` and `--config <dir>`.
- Go 1.27 or later, no cgo.

## Testing rules

- Pure logic (state transitions, clipping, row classification, file formats)
  gets property tests with `pgregory.net/rapid`, next to table tests. Put them
  in `props_test.go` in the package. If a property fails, find out whether the
  code or the property is wrong before changing either.
- rapid saves a failing case as a `.fail` file under `testdata/rapid/` and
  replays every file there on each run, so they are regression seeds. Commit
  one only once the bug is fixed and it passes. Seeds go stale when a
  generator changes (the draws shift and the seed stops replaying its bug), so
  prefer a named regression test for anything worth keeping.
- External commands (claude, ps, tmux, pbcopy) sit behind small seams:
  a function variable or an interface, set by tests. Logic must be testable
  without those programs installed or running.
- Tests use a `t.TempDir()` home and never touch the real `~/.claude-*`.
  A property test makes one per case under a `t.TempDir()` taken outside
  `rapid.Check`.
- A test that needs a real tmux gets a private server from
  `internal/tmuxtest`, which puts the socket in a short directory under
  `/tmp` and removes it afterwards. tmux never deletes its socket files.

## Hook rules

The hook runs inside every Claude Code session, so it must never block, fail
loudly, or slow a session down. Always drain stdin, always exit 0, write only
when something changed, and do no network or subprocess work.

## Docs and VCS

- Keep README.md in sync with any user-visible change in the same commit.
- The repo is jj (colocated with git). Use `jj`, not raw git. Commit with
  `commitp -m "subject\n\nbody"` as a single `-m` with real newlines.
- Instructions live in AGENTS.md only. Do not add a CLAUDE.md.
