# Workstreams and the cockpit

Written 2026-10-23. This is the design for grouping a project's sessions
by the task they work on, and for a dashboard of one task, called the
cockpit. Phases 1 to 3 are built: the workstreams in the list, the
cockpit, and starting a workstream from a ready issue. Phase 4 is open.

## What it is for

You run several tasks in one project at once: rework the UI, add a
feature, fix a bug. A flat group of a project's sessions and terminals
would not say which task each one is for, or how far a task has got.
Workstreams give the list that grouping.

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
terminals sit under the project, ungrouped. overview never requires
`issues`.

### Linking a session to an issue

A session is linked to an issue in one of two ways, and neither asks the
agent to do anything new:

- You start the workstream from overview. `enter` on a ready issue in the
  cockpit starts claude at the project's root with an opening prompt of
  a fixed form, `Work on overview#6: <title>`. The agent decides the
  rest.
- An agent picks up an issue itself with `issues start 6`, as the issues
  skill already tells it to.

The transcript shows both. overview reads each session's transcript
incrementally, from the byte offset it last reached, and links from two
shapes of line. A prompt in the main conversation links its session when
it starts with `Work on <project>#<n>`, whether it is the first prompt or
a later one. A Bash tool call links it when its command runs `issues start
<n>` and the matching tool result was not an error, so a start that failed
links nothing. The time of a link is the line's timestamp, and the
directory is the cwd the transcript recorded for the line, which `cd` in
the command moves. A session's subagents keep their transcripts in
`<session>/subagents/`. Their `issues start`s link the parent, which is
how an orchestrating session takes on several issues, and their prompts
link nothing. The links of all of a session's transcripts are ordered by
time, and the reader keeps each reference once, where it was last linked,
with its first time, and the newest eight. `issues start` can name its
project with `-C`, `--dir` or `--project`, so the link records what the
command gave and the view resolves it to a project. Mentioning an issue
anywhere else, in a prompt or a command, links nothing, and neither does a
command that only quotes `issues start`.

The hook takes no part in linking. It records a session's state and,
when the session is blocked, its reason. The links come from the
transcripts alone, so they exist wherever the hook is not installed.

A session linked to several open issues appears under each of them,
since an orchestrating session can run several tasks at once. Its
primary entry is under the issue it was linked to last, and the agents
list names that issue beside it.

A terminal opened with `t` on a workstream belongs to that workstream.
Any other terminal belongs to its project.

### When a workstream ends

A workstream ends when its issue is `done` or `dropped`. Its sessions
that are still running go back under the project, unless another open
issue they are linked to still holds them. A workstream whose sessions
have all exited stays in the list while its issue is open, with no
sessions under it, so a paused task is still visible.

An issue in `review` puts its workstream in front of you: it sorts
first in its project and counts as needing you.

### The review bottleneck

Ending at `done` only works if issues reach `done`. They stop at
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
sessions and terminals. A terminal outside any project goes
under `~`. Pinned projects are listed like any other.

The bottom holds every session in every project, ordered by priority,
after herdr's agent list. Sessions that need you come first, longest
wait first, then working sessions, then idle ones. Terminals are never
in it. A session can appear twice, once in each half, and the cursor
moves through both.

`s` on any entry of a tracked project opens a task picker in the
list's place, styled like the add-project picker. It lists the project's
open issues: ready first, then the inbox, then the rest in issues' own
order, and by priority within each. Typing narrows them by words, on
the reference and the title. `enter` on an issue that sessions already
work on moves the cursor to its workstream, and on any other issue
starts claude on it with the opening prompt. This is how a pinned
project with a backlog and nothing running gets its first workstream.
The picker's rows and what `enter` does are pure functions of the
tracker, the links and the query. While it is open, the picker zooms
the list's pane as the cockpit does, and shows the selected issue's
details beside the rows. They come from `issues show`, through the
same cache as the cockpit's issue card, so you can read a task in full
before starting it.

The look is one titled rule per panel, as [panels-design.md](panels-design.md)
describes: the list has a rule along its top, and the cockpit's cards use the
same rule. The selected row
gets an accent bar, `▌`, at its left edge and a faint background that
keeps the row's own colours. The bottom bar lists the keys as
`key label • key label`, with the keys in the accent colour, and changes
with what is selected.

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
  the keys that act on it: `c` claude and `t` terminal, plus `enter`
  attach and `v` review once the workstream has a session. A workstream
  with only terminals offers the first two.
- The issue: its details from `issues show`, with the acceptance
  criteria, those still to do first, and how many are checked.
- Changes: the diff since the workstream was taken on, the earliest
  time any of its sessions linked to it, as a stat per file, and how
  much is still uncommitted. Work that subagents do in other worktrees
  shows only once it reaches the project's checkout.
- Review: the open notes across the workstream's sessions, the replies
  waiting on you, and when notes were last sent.
- The project's other issues: those in review, those in progress with
  their workstreams, the blocked ones, the next ready ones, and the
  inbox count. `enter` on one that a session already works on moves the
  list's cursor to its workstream, with the cockpit following, rather
  than starting a second session. On any other issue, a paused one
  included, it starts a session, as the task picker does.

For a project, the issues card is the cockpit, with the project's
uncommitted changes under it. A project without `.issues/` gets a card
in the issues card's place that says what `issues` does and that
`issues init` in the project's root starts it.

The last card that does not fit whole is shown shortened in the lines
left, when there are at least three: its rule, what fits of its body,
and a line above and below counting what it leaves out there, as `↑ 3
more issues`. The issues card keeps its cursor in that view, and the
first issue in view of a group whose heading is cut takes the heading. A card is dropped, from the bottom up, only when fewer lines
than that are left. To keep the issues card short enough to show,
each of its groups lists a few issues (three in review, four in
progress, two blocked, three up next) and counts the rest, and the
issue card shows four lines of the issue's summary. When the issues card is dropped
anyway, the keyboard goes back to the list, and `tab` says the window
has no room for the card.

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

1. Workstreams in the list, with the restyle. The two parsers,
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
