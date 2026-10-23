# Workstreams and the cockpit

Written 2026-10-23. This is the design for grouping a project's sessions
by the task they work on, and for a dashboard of one task, called the
cockpit. Phases 1 and 2 are built: the workstreams in the list, and the
cockpit.

## What it is for

You run several tasks in one project at once: rework the UI, add a
feature, fix a bug. Today the list shows a project's sessions and
terminals in one flat group, so nothing says which task each one is
for, or how far a task has got.

A workstream is one task. How many sessions and terminals it takes, and
whether its agents work in worktrees, is up to the agents. overview does
not follow worktrees. Sessions start at the project's root, and one
session can run subagents across several worktrees.

Two outside tools set the look. herdr groups agents into spaces, with a
list of every agent by priority under them. A team dashboard for a local
dev stack shows one workstream as a set of cards with the keys that act
on it written beside them. overview takes the first idea for the list
and the second for the cockpit, and keeps its own keys.

## Workstreams are issues

A project that tracks its tasks with the `issues` tool already has the
unit: an issue. A workstream is an open issue that is in progress, in
review or blocked, or that has a session linked to it, together with the
sessions and terminals linked to it. Several sessions can work on one
issue, and one session can work on several.

A project without `.issues/` has no workstreams. Its sessions and
terminals sit under the project as they do today, and nothing about it
changes beyond the look. overview never requires `issues`.

### Linking a session to an issue

A session is linked to an issue in one of two ways, and neither asks the
agent to do anything new:

- You start the workstream from overview. `enter` on a ready issue in the
  cockpit starts claude at the project's root with an opening prompt of
  a fixed form, `Work on overview#6: <title>`. The agent decides the
  rest.
- An agent picks up an issue itself with `issues start 6`, as the issues
  skill already tells it to.

The hook sees both. It reads every prompt on `UserPromptSubmit` and
every Bash command on `PostToolUse`. A prompt links its session when it
starts with `Work on <project>#<n>`, whether it is the first prompt or a
later one, and a command links it when it runs `issues start <n>`. The
hook records the reference in `<id>.tasks`, beside the session's record,
so the record itself is never rewritten for a link. The file is a log the hook only appends to: each link is one JSON line,
with the time the issue was first linked, written in a single append, so a
session and its parallel subagents linking at once never drop each other's
links. The hook never rewrites the file, because a read-then-rewrite would
lose appends made in between. The reader keeps each reference once, where
it was last linked, with its first time, and the newest eight. The hook
appends a link only when it changes which references the file reads as or
their order, so replaying `issues start 3 && issues start 4` adds
nothing. The session's end removes the file. That is string
matching on input the hook already receives, with no subprocess, so it
keeps to the hook rules. `issues start` can name its project with `-C`,
`--dir` or `--project`, so the hook records what the command gave and
the view resolves it to a project. Mentioning an issue anywhere else, in
a prompt or a command, links nothing.

A session linked to several open issues appears under each of them,
since an orchestrating session can run several tasks at once. Its
primary entry is under the issue it was linked to last, and the agents
list names that issue beside it.

A terminal opened with `t` on a workstream belongs to that workstream.
Any other terminal belongs to its project, as now.

### When a workstream ends

A workstream ends when its issue is `done` or `dropped`. Its sessions
that are still running go back under the project, unless another open
issue they are linked to still holds them. A workstream whose sessions
have all exited stays in the list while its issue is open, with no
sessions under it, so a paused task is still visible.

An issue in `review` puts its workstream in front of you: it sorts
first in its project and counts as needing you.

### The review bottleneck

Ending at `done` only works if issues reach `done`. Today they stop at
`review`, because the issues skill makes human acceptance the normal
boundary for `done`, and you do not need to review every issue. Until
that changes, workstreams pile up in review.

The fix belongs to `issues` and its skill, not to overview. One shape
for it: an agent closes an issue itself once its acceptance criteria
are checked with receipts, and stops at `review` only for issues
marked as needing you, or when it is unsure. overview could then list
the workstreams done today, so you can look over what closed without
anything waiting on you. This has to be worked out before workstreams
are useful day to day, but it does not block building them.

## The list

The list keeps its place on the left and is split in two.

The top holds the projects in alphabetical order. Inside a project, its
workstreams come first: those in review, then those in progress, then
the blocked ones, and by priority within each. A workstream takes one
line for its issue number, title and priority, and its sessions and
terminals follow it, one line each. Sessions are marked by state and
terminals with a `$` and no state. Then come the project's unlinked
sessions and terminals, as today. A terminal outside any project goes
under `~`. Pinned projects work as they do now.

The bottom holds every session in every project, ordered by priority,
after herdr's agent list. Sessions that need you come first, longest
wait first, then working sessions, then idle ones. Terminals are never
in it. This replaces the "needs you" section at the top of today's
list. A session can appear twice, once in each half, and the cursor
moves through both.

The look changes along the way:

- A header line above the list: the home's name, the counts that sit in
  the status bar today, and the digest's age and the orchestrator's
  health.
- The selected row gets an accent bar, `▌`, at its left edge, and a
  faint background that keeps the row's own colours, which today's
  solid bar cannot.
- Sections get a rule with the title on the left and a count on the
  right.
- The bottom bar lists the keys as `key label • key label`, with the
  keys in the accent colour, and changes with what is selected.

## The cockpit

`o` turns the whole window into the cockpit for the selected workstream
or project, and `o` or `esc` turns it back. The list zooms its own pane
with tmux's `resize-pane -Z`, so the session beside it keeps running and
comes back exactly as it was. The list stays down the left, and the
cards take the rest.

The zoom is one tmux command that sets the state rather than toggling
it, `if-shell -F '#{window_zoomed_flag}'` around `resize-pane -Z`, and
the view sends one at a time, so fast presses and a review's split
cannot leave the window zoomed the wrong way. An `o` while the list is
moving between windows waits for the move. tmux ends a zoom when the
user goes to another pane of the window, and the cockpit then closes.
It also ends one when a pane of the window closes; the cockpit stays
open then and zooms again, or fills the window unzoomed when the list
is left alone, since tmux cannot zoom a lone pane. `esc` clears a filter
on the list before it closes the cockpit.

For a workstream, in this order:

- The header: the issue's reference, title, status and priority, how
  long the workstream has run, the state of each of its sessions, and
  the keys that act on it (`enter` attach, `v` review, `c` claude, `t`
  terminal).
- The issue: its details from `issues show`, with the acceptance
  criteria and which are checked.
- Changes: the diff since the workstream was taken on, the earliest
  time any of its sessions linked to it, as a stat per file, and how
  much is still uncommitted. Work that subagents do in other worktrees
  shows only once it reaches the project's checkout.
- Review: the open notes across the workstream's sessions, the replies
  waiting on you, and when notes were last sent.
- The project's other issues: those in review, those in progress with
  their workstreams, the blocked ones, the next ready ones, and the
  inbox count. `enter` on a ready one starts a workstream for it, and on one
  that is a workstream already it moves the list's cursor there, with
  the cockpit following, rather than starting a second session.

For a project, the issues card is the cockpit, with the project's
uncommitted changes under it. A project without `.issues/` gets a card
in the issues card's place that says what `issues` does and that
`issues init` in the project's root starts it.

A card the window has no room for is dropped, from the bottom up,
rather than squeezed. To keep the issues card short enough to show,
each of its groups lists a few issues (three in review, four in
progress, two blocked, three up next) and counts the rest, and the
issue card lists six criteria at most. When the issues card is dropped
anyway, the keyboard goes back to the list, and `tab` leaves it there.

The cards come from running jj, git and `issues`, which the view never
does on its own goroutine. Commands run for the selected entry when it
is selected and when a hook event reports a session in its project. No
timer runs them, since every jj command snapshots the working copy. A
workstream's base commit is found once for the time it was taken on,
and found again only when that time moves earlier. Their results are cached
per project. jj and git sit behind `vcs.Runner`, as the review's
commands do, and `issues` behind a seam of its own. It is called as
`issues --dir <root> list --json` and `issues --dir <root> show <n>
--json`, and parsing every command's output is pure.

## Phases

1. Workstreams in the list, with the restyle. The hook's two parsers,
   the issues seam, grouping a project's entries under its issues, and
   the two halves of the list. Property tests for the parsers, the
   grouping and the cursor moving through both halves.
2. The cockpit: the zoom, the cards, and the commands behind their
   seams.
3. Starting a workstream from a ready issue.
4. Maybe: tabs. Making each workstream a tmux session of its own would
   give each one a tab bar of its sessions and terminals for free, from
   tmux's status line. That changes how overview lays out its windows,
   so it waits until the first three have been used for a while.

Each phase updates the README and adds to `scripts/e2e`.
