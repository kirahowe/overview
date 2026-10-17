# Handoff: overview as the command centre for stock Ghostty

Written 2026-10-17 at the end of a planning session in the `agents` repo.
Pick it up in a fresh session started from this repo.

## Status, 2026-10-17: built

The plan below has been built, with four changes. The rest of this file
is the original plan, kept for its reasoning.

- **No quick terminal.** Kira dropped it after trying it. Cmd+1 to the
  overview tab is enough, and the dropdown needed overview started in it
  by hand after every Ghostty restart, because `initial-command` only
  runs in the first terminal. The hotkey is gone from the Ghostty config.

- **The tab bar stays visible.** Stock Ghostty 1.3.1 on macOS cannot hide
  it: `window-show-tab-bar` is GTK-only, and `macos-titlebar-style =
  hidden` turns tabs off, so Cmd+T opens a window. Kira chose a compact
  tab bar (`macos-titlebar-style = tabs`) with new tabs at the end and
  overview started in tab 1 by `initial-command`. The config is in
  `~/code/configs/ghostty/config.ghostty`, and the live config file is a
  symlink to it.
- **The TUI finds the terminal, not the hook.** The TUI already knows
  each interactive session's tty, so on Enter it writes the title token
  to that tty, asks Ghostty which terminal shows it, restores the old
  title, and caches the id. The hook is unchanged. Sessions started before
  this change can be jumped to as well. See `internal/ghostty` and
  `internal/tui/jump.go`.
- **Only Ghostty sessions are touched.** Before writing to a tty, overview
  walks the session's process ancestry and writes nothing unless Ghostty
  is an ancestor. tmux, iTerm and Terminal.app sessions get the old tty
  note.

The original plan's open questions came out like this:

- Hooks get `CLAUDE_PID` in their environment. The hook itself has no
  tty. This no longer matters, because the hook doesn't do the lookup.
- Ghostty coalesces titles. A token overwritten within about 50 ms is
  never visible, so Locate rewrites it on each polling round.
- One osascript call costs about 75 ms. A first jump takes 0.4 to 0.55 s,
  and a cached one about 130 ms.
- Ghostty has no xterm title stack (`CSI 22/23 t`). Restoring the old
  title can therefore freeze a title that Ghostty derived itself.
- `focus terminal id` selects the tab, focuses the split and raises the
  window.
- Two overview instances are safe. The TUI never runs digests on a timer.
  Only `D` runs one by hand.

Verification: `scripts/check` gates every commit, and `scripts/check
--deep` runs the property tests at 2000 cases. `OVERVIEW_E2E=1
scripts/e2e-ghostty` drives a real overview in throwaway Ghostty windows
and checks which terminal gets focus.

## Decision

Kira is moving off the Agents app. The new setup:

- **One Ghostty window.** It uses stock Ghostty.app (1.3.1 is installed).
  Each agent session gets a tab, and the tab bar is hidden, so only one
  session is on screen at a time, like the detail area in Agents. Native
  splits, Cmd+D and pane navigation work inside a session, because splits
  belong to a tab. Kira does not want a window per project; with this
  many projects open, that would be chaos.
- **overview has a tab of its own.** It is pinned as the first tab, so
  Cmd+1 always goes home. Kira can work from there and hop in and out of
  sessions.
- **The same overview runs in the quick terminal.** That is Ghostty's
  dropdown on a global hotkey. It stays alive while hidden, so from any
  session one keystroke shows the queue. Picking a session there jumps to
  it and the dropdown hides.
- **Jumping:** overview selects the session's tab and focuses its
  terminal through Ghostty's AppleScript API. Jumping to a session is also
  how Kira switches projects, so there is no sidebar to manage by hand.

The Agents app is frozen: no new work, nothing deleted.

Kira's guiding rule: start with the simplest thing that works, and add
complexity only where it removes a real friction.

### Why

- Agents was built around jj workspace management, and Kira doesn't need
  it any more. Coding agents manage their own worktrees now.
- Kira wants to do everything from the keyboard. Switching projects in
  Agents was the main friction, along with hand-managing a sidebar while
  many projects are open at once.
- **herdr was rejected.** It is a Rust agent multiplexer at v0.9. Once
  overview tracks agent state, herdr adds little over tmux, and it is
  younger and rougher.
- **tmux was rejected.** It would replace Ghostty's native splits, which
  Kira finds much nicer (smooth scrolling, native selection, no
  copy-mode).
- **A stripped-down Agents app is the fallback.** It becomes worth it
  only if Kira wants something Ghostty can't do from outside, such as
  per-session badges drawn on the panes themselves.

## What we verified

- Ghostty 1.3's AppleScript dictionary (`sdef /Applications/Ghostty.app`)
  exposes windows, tabs and terminals. A terminal has `id`, `name` and
  `working directory`. The commands include `focus`, `select tab`,
  `activate window`, `split`, `input text` and `perform action`.
  Mitchell called the API a preview, so it may change.
- `osascript -e 'tell application "Ghostty" to get id of focused terminal
  of selected tab of front window'` works and returns a UUID such as
  `D29572A4-1C61-4D8A-A92E-5BE8F251E2F3`. Kira has already granted the
  Automation permission from Agents.
- **A shell cannot see its own terminal's ID.** Stock Ghostty sets no
  `GHOSTTY_SURFACE_ID`. That variable came from the Agents app's embedded
  libghostty. A window opened through AppleScript had only
  `GHOSTTY_RESOURCES_DIR`, `GHOSTTY_BIN_DIR`, `GHOSTTY_SHELL_FEATURES`,
  `TERM_PROGRAM` and `TERM`.

## The plan

### 1. Map each session to a terminal with a title token

At `SessionStart`, the overview hook:

1. finds the session's tty,
2. writes a unique token to that tty as a title escape (`OSC 2`),
3. asks Ghostty for the `id` of `every terminal whose name contains
   <token>`,
4. stores that UUID in the session's record (a new field on `Session` in
   `internal/fleet/state.go`).

Claude Code overwrites the title soon afterwards. That doesn't matter,
because the token only has to last for one query. The UUID stays fixed
for the life of the pane.

If the lookup fails, the session still shows up in overview; only the
jump is unavailable. Keep it that way: failure here must never block or
slow the hook.

Open questions to settle first:

- **Finding the tty.** The hook payload has no PID or tty. Today the TUI
  gets each session's tty with `ps -o pid=,tty=` on the PID from
  `claude agents --json` (`internal/tui/tui.go`, `terminals`). The hook
  probably needs to walk up its parent processes to the `claude` process
  and read that process's tty. Check what the hook's own process tree
  looks like before you choose.
- **Timing.** Claude Code may set its own title at about the same moment.
  Test whether the token survives long enough. If it doesn't, write it
  and query in a short retry loop.
- **Cost.** `osascript` start-up time inside a hook. `SessionStart` fires
  once per session, so this should be fine, but measure it.

The simpler fallback is to record whichever terminal is focused at
session start. It gets background or scripted launches wrong, so use it
only if the token approach turns out to be unworkable.

### 2. Jump action in the TUI

Enter (or a new key) on an interactive session selects the tab that holds
the stored terminal and focuses the terminal. The jump has to work from
both places overview runs: its own tab and the quick terminal. Check
whether the quick terminal hides by itself when focus moves to the main
window; Ghostty's autohide should do this by default. If it doesn't,
hide it through `perform action`.

Background sessions keep the current `claude attach` behaviour. Sessions
without a stored terminal fall back to showing the tty, as they do now.

### 2a. overview is the only way to navigate

Kira doesn't care how many tabs there are, as long as they never have to
scroll through a tab list. Tabs are only where sessions live. overview has
to make dozens of sessions feel as manageable as the Agents sidebar does
today, and that sidebar works because sessions are organised by project
and easy to reach. So:

- **Organise by project.** The current list groups only by state (needs
  you, working, idle). A proposal to confirm with Kira: keep "needs you"
  pinned at the top, and group everything else by project, meaning the
  repo or working directory. The state still shows on each row.
- **Make search the main way to jump.** `/` already filters. The filter
  should match project, title and last prompt, and Enter on a match
  should jump straight there. Typing a few letters and pressing Enter
  should be the quickest way to reach any session.
- **Keep the keys the same in both places.** overview has to behave the
  same in its own tab and in the quick terminal.

### 3. Ghostty config (Kira's dotfiles, not overview code)

- Hide the tab bar in the one main window.
- Pin overview as the first tab, so Cmd+1 goes home.
- Run overview in the quick terminal on a global hotkey.

New sessions start in a new tab (Cmd+T). Check how a hidden tab bar
interacts with Cmd+T and with tab ordering before relying on "first tab
is overview".

Two overview instances running at once (the tab and the dropdown) should
be harmless, because both only read state, but confirm that the digest
timer doesn't run twice.

## Constraints

- **Repo:** overview is a colocated jj repo. Load the `jujutsu` skill
  before any VCS work. The handoff commit sits on top of `825a95d7`
  ("Restyle the view after revdiff").
- **Language:** Go (`go build -o bin/overview .`). The tests are in
  `internal/fleet/fleet_test.go`.
- **Out of scope for now:** sessions that aren't Claude, such as Codex.
  overview only tracks Claude Code sessions under one config directory.
- **Testing the Ghostty side:** opening windows through AppleScript works
  from an agent session. Close any windows the tests open.
