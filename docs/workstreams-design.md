# Tasks and the cockpit

Written 2026-10-23, revised 2026-11-16 and 2026-11-17. This is the
design for how overview shows a project's issues beside its sessions,
and for a dashboard of one project, called the cockpit. Phases 1 to 3
built the first version, which grouped a project's sessions under its
issues as workstreams. Phase 5 made the session the row and its issues
labels on it. Phase 6 dropped the join between sessions and issues
altogether, which is the model described here.

## What it is for

You run several tasks in one project at once: rework the UI, add a
feature, fix a bug. You want to see what is running, what is waiting on
you, and what there is to start next.

How many sessions and terminals a task takes, and whether its agents
work in worktrees, is up to the agents. overview does not follow
worktrees. Sessions start at the project's root, and one session can
run subagents across several worktrees.

Two outside tools set the look. herdr groups agents into spaces, with a
list of every agent by priority under them. A team dashboard for a local
dev stack shows one project as a set of cards with the keys that act on
it written beside them. overview takes the first idea for the list and
the second for the cockpit, and keeps its own keys.

## Sessions and issues are not joined

overview models three things:

- An issue, read from the project's tracker with the `issues` tool,
  with its status.
- A session: a top-level Claude session, whether you started it or an
  agent did. Its subagents are not modelled as anything of their own.
  The harness has its own ways to show and manage them.
- A terminal: a pane with no Claude session in it.

Nothing relates a session to an issue. The list shows sessions and
terminals, and the cockpit and the task picker show issues.

Phases 1 to 5 tried to say which session worked on which issue, from
links read out of each transcript: an opening prompt of the form `Work
on <project>#<n>`, and `issues start <n>` run through the Bash tool, by
the session or by one of its subagents. Each version needed rules to
make a guess look like a fact, and each rule fitted one way of working
and misled in another:

- An orchestrating session that started six issues through subagents
  held all six. An issue in review sat over a row that said "working
  now", because the row showed the session's state, not the issue's.
- A session that only read an issue for context, or filed issues for
  later, was linked to them in the same way.
- An issue's own status and the link disagreed. relay#93 was blocked on
  two other issues while a live session worked on it. It was listed
  as waiting on you, and enter on it, and on the issue that blocked it,
  went to that same session, so the section looked like three things
  when it held one.
- Reading `issues start` took a shell reader of its own, with `cd`,
  `--dir`, functions and `~`, and still could not place a start whose
  directory came from a variable.

Which session works on what depends on how each agent and workflow is
set up, and there is no grouping that fits all of them. herdr can group
agents by tab because the user makes the tab on purpose. overview would
have had to infer it after the fact. So it does not try.

What is left needs no join:

- A session needs you when its own state says so: blocked, asking, out
  of usage.
- An issue waits on you when its tracker says it is in review. A
  project's heading counts those, and so does the list's title. A
  blocked issue is waiting on whatever blocks it, usually another issue,
  so it is not counted.
- A session started on an issue from overview opens with `Work on
  <project>#<n>: <title>`, so the agent knows what to do and its title
  usually names the issue. That is a one-way action, and nothing keeps
  track of it afterwards.

What was given up: overview no longer notices an issue in progress with
no live session on it. The tracker can answer a nearby question by
itself, such as which issues in progress have not changed in some days,
if that turns out to be wanted.

A project without `.issues/` has no issues. Its sessions and terminals
are listed as in any project. overview never requires `issues`.

### The review bottleneck

Issues stop at `review`, because the issues skill makes human acceptance
the normal boundary for `done`, and you do not need to review every
issue. The fix belongs to `issues` and its skill, not to overview. One
shape for it: an agent closes an issue itself once its acceptance
criteria are checked with receipts, and stops at `review` only for
issues marked as needing you, or when it is unsure.

What overview does is give the verdicts where you look at the issues.
`m` in the cockpit's issues card opens a box for the issue under its
cursor, with the verdicts `issues tui` gives: approve (done), send back
(ready, with a note) and reject (dropped). It runs them through `issues
approve`, `issues changes` and `issues reject`, which record the note in
the issue's details log the way the tui does. Sending work back starts a
session on the issue with the note under the opening prompt, since the
next step is almost always an agent making the change. An issue that is
not in review can be marked done or dropped, but not sent back.

## The list

The list keeps its place on the left and is split in two.

The top holds the projects in alphabetical order. Inside a project come
its sessions, one line each, marked by state, then its terminals, marked
with a `$` and no state. A terminal outside any project goes under `~`.
Pinned projects are listed like any other. A tracked project's rule
counts its issues in review, or its open issues when none is in review,
before its sessions and terminals.

```
finance-aggregator ──────── 2 to review · 1 session · 1 terminal
⋮ Open project issues                              working
$ bb
```

The bottom holds every session in every project, ordered by priority,
after herdr's agent list. Sessions that need you come first, longest
wait first, then working sessions, then idle ones. Terminals are never
in it. A session can appear twice, once in each half, and the cursor
moves through both.

`enter` on a session or a terminal goes to its pane.

`s` on any entry of a tracked project opens a task picker in the
list's place, styled like the add-project picker. It lists the project's
open issues: ready first, then the inbox, then the rest in issues' own
order, and by priority within each. Typing narrows them by words, on
the reference and the title. `enter` starts claude on the selected
issue with the opening prompt. This is how a pinned project with a
backlog and nothing running gets its first task. The picker's rows are
a pure function of the tracker and the query. While it is open, the
picker zooms the list's pane as the cockpit does, and shows the
selected issue's details beside the rows. They come from `issues show`,
so you can read an issue in full before starting it.

The look is one titled rule per panel, as [panels-design.md](panels-design.md)
describes: the list has a rule along its top, and the cockpit's cards use the
same rule. The selected row
gets an accent bar, `▌`, at its left edge and a faint background that
keeps the row's own colours. The bottom bar lists the keys as
`key label • key label`, with the keys in the accent colour, and changes
with what is selected.

## The cockpit

`o` turns the whole window into the cockpit for the selected entry's
project, and `o` or `esc` turns it back. The list zooms its own pane
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

The cards, in this order:

- The header: the project's name, root and kind of repository, the
  state of each of its sessions and terminals, and the keys that act
  on it.
- Uncommitted: the files changed in the working copy, as a stat per
  file.
- Issues: the project's open issues, those in review, those in
  progress, the blocked ones, all the ready ones, and the inbox count.
  `enter` on one starts a session on it, as the task picker does, and
  `m` gives it a verdict.

A project without `.issues/` gets a card in the issues card's place
that says what `issues` does and that `issues init` in the project's
root starts it.

The last card that does not fit whole is shown shortened in the lines
left, when there are at least three: its rule, what fits of its body,
and a line above and below counting what it leaves out there, as `↑ 3
more issues`. The issues card keeps its cursor in that view, and the
first issue in view of a group whose heading is cut takes the heading.
A card is dropped, from the bottom up, only when fewer lines than that
are left. The issues card lists every issue and is the last card, so it
takes whatever rows the others leave and scrolls with its cursor; the
changes card above it caps its files, so it stays short. When the
issues card is dropped anyway, the keyboard goes back to the list, and
`tab` says the window has no room for the card.

The cards come from running jj, git and `issues`, which the view never
does on its own goroutine. The changes are read when the cockpit opens
and when a hook event reports a session in its project. No timer runs
them, since every jj command snapshots the working copy. Their results
are cached per project. jj and git sit behind `vcs.Runner`, as the
review's commands do, and `issues` behind a seam of its own. It is
called as `issues --dir <root> list --json` and `issues --dir <root>
show <n> --json`, and parsing every command's output is pure.

## Phases

1. Workstreams in the list, with the restyle. The two parsers,
   the issues seam, grouping a project's entries under its issues, and
   the two halves of the list. Property tests for the parsers, the
   grouping and the cursor moving through both halves.
2. The cockpit: the zoom, the cards, and the commands behind their
   seams.
3. Starting a task from a ready issue.
4. Maybe: tabs. Making each task a tmux session of its own would
   give each one a tab bar of its sessions and terminals for free, from
   tmux's status line. That changes how overview lays out its windows,
   so it waits until the first three have been used for a while.
5. Sessions as rows, with their tasks as labels, and the issues that
   waited on you listed once each. What went was the workstream as a
   container: `Stream`, a session's primary workstream and the entries
   that aliased a session under several issues.
6. No join between sessions and issues, as this revision describes.
   What went: the links and the transcript reader of commands, the
   reading of subagent transcripts, a session's tasks, the issue rows
   in the list, the issue's own cockpit, a terminal's task, and
   `issues.Criteria`. The cockpit is a project's alone.

Each phase updates the README and adds to `scripts/e2e`.
