# Tasks and the cockpit

Written 2026-10-23, revised 2026-11-16. This is the design for showing
which task each session works on, and for a dashboard of one task,
called the cockpit. Phases 1 to 3 are built on the first version of
this design, which grouped a project's sessions under its issues as
workstreams. Phase 5 replaces that grouping with the model described
here, and is not built yet.

## What it is for

You run several tasks in one project at once: rework the UI, add a
feature, fix a bug. A flat group of a project's sessions and terminals
would not say which task each one is for, or how far a task has got.

How many sessions and terminals a task takes, and whether its agents
work in worktrees, is up to the agents. overview does not follow
worktrees. Sessions start at the project's root, and one session can
run subagents across several worktrees.

Two outside tools set the look. herdr groups agents into spaces, with a
list of every agent by priority under them. A team dashboard for a local
dev stack shows one task as a set of cards with the keys that act on it
written beside them. overview takes the first idea for the list and the
second for the cockpit, and keeps its own keys.

## Sessions have tasks

A project that tracks its tasks with the `issues` tool already has the
unit of work: an issue. overview models three things:

- An issue, read from the tracker, with its status.
- A session: a top-level Claude session, whether you started it or an
  agent did. Its subagents are not modelled as anything of their
  own. The harness has its own ways to show and manage them, and
  overview does not repeat them.
- A terminal: a pane with no Claude session in it.

They are related by one rule: a session has a set of tasks, the issues
in progress that it linked to. A link made by one of its subagents
counts as the session's own. A terminal opened with `t` on an issue has
that issue as its task while the issue is open.

The first version made the issue the container, with each session
listed under every issue it was linked to. An orchestrating session
that ran six issues through subagents was then listed six times, and
each copy showed the session's own state, so an issue in review sat
over a row that said "working now". With the session as the row and its
tasks as labels on it, a session is listed once however many tasks it
has, and every state on screen belongs to the thing it describes.

The issue's status says who it is waiting on, so overview needs no
other record of whether work on it is still going:

- In progress: an agent has it. It is a task of each live session that
  linked to it.
- In review or blocked: it is waiting on you, whichever session did the
  work.
- In progress with no live session linked to it: it has stalled, and is
  waiting on you too.
- Ready, in the inbox, done or dropped: nobody is on it, and the list
  does not show it.

A project without `.issues/` has no tasks. Its sessions and terminals
are listed without labels. overview never requires `issues`.

### Linking a session to an issue

A session is linked to an issue in one of two ways, and neither asks the
agent to do anything new:

- You start the task from overview. `enter` on a ready issue in the
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
link nothing. overview reads a subagent's transcript only for its
links. The links of all of a session's transcripts are ordered by time,
and the reader keeps each reference once, where it was last linked,
with its first time, and the newest eight. `issues start` can name its
project with `-C`, `--dir` or `--project`, so the link records what the
command gave and the view resolves it to a project. Mentioning an issue
anywhere else, in a prompt or a command, links nothing, and neither does a
command that only quotes `issues start`.

The reader of commands follows the shapes agents write. It is no shell.
It follows a `cd` or `pushd` to a directory the command writes out, with
`~` as the user's home. It reads a function defined in the command as
its body wherever a later command calls it, so `i() { issues --dir .
"$@"; }; i start 6` links as `issues --dir . start 6` does. It expands no
variables and no substitutions. When a start's directory depends on one,
or the start comes after a `cd` the reader could not follow, a loop or a
group, the link keeps the issue's number and no directory. Such a start
counts for the session's own project, and only for an issue that is in
progress, in review or blocked there. A start puts its issue in
progress, so a number that is still ready or in the inbox in the
session's own project was started somewhere else, and links nothing.

The transcripts from 2026-10-20 to 2026-11-14, outside overview's own
sessions, hold 30 commands that started an issue. A reader that gave up
on any directory it could not read linked 21 of them. Of the other
nine, six came after `cd ~/…` (one of those after a `for` loop as well),
two named the directory as `--dir $W`, and one went through a function.
Three of the nine still have no directory and link by the rule above.

The hook takes no part in linking. It records a session's state and,
when the session is blocked, its reason. The links come from the
transcripts alone, so they exist wherever the hook is not installed.

### When a task ends

An issue stops being a task when it leaves progress. A session that
puts its issue in review loses that label, and the issue moves to the
list of what waits on you. Approving or rejecting it there takes it out
of the list. Sending it back makes it ready and starts a session on
it, which has it as a task again once it runs `issues start`.

### The review bottleneck

Ending at `done` only works if issues reach `done`. They stop at
`review`, because the issues skill makes human acceptance the normal
boundary for `done`, and you do not need to review every issue. Until
that changes, issues pile up in the list of what waits on you.

The fix belongs to `issues` and its skill, not to overview. One shape
for it: an agent closes an issue itself once its acceptance criteria
are checked with receipts, and stops at `review` only for issues
marked as needing you, or when it is unsure. overview could then list
the issues done today, so you can look over what closed without
anything waiting on you. This has to be worked out before tasks are
useful day to day, but it does not block building them.

What overview does is give the verdicts where you look at the work.
`m` in the list or the cockpit opens a box for the issue, with the
verdicts `issues tui` gives: approve (done), send back (ready, with a
note) and reject (dropped). It runs them through `issues approve`,
`issues changes` and `issues reject`, which record the note in the
issue's details log the way the tui does. Sending work back starts a
session on the issue with the note under the opening prompt, since the
next step is almost always an agent making the change. An issue that is
not in review can be marked done or dropped, but not sent back.

## The list

The list keeps its place on the left and is split in two.

The top holds the projects in alphabetical order. Inside a project come
its sessions, one line each, marked by state, with each session's tasks
as labels at the end of its line: the issue numbers, in the order the
session linked them, and `+n` for those that do not fit. Then come the
project's terminals, marked with a `$` and no state, with a terminal's
task as its label. Last come the issues that wait on you, one line each
for the issue's number, title, status and priority: those in review,
then the blocked ones, then those that have stalled, and by priority
within each. An issue that a live session has as a task is not listed
on a line of its own. A terminal outside any project goes under `~`.
Pinned projects are listed like any other.

```
finance-aggregator
⋮ Open project issues            #14 #19     working
$ bb
  #23 Preview failure after a succ…       review p2
  #24 Say when a failed statement…        review p2
```

The bottom holds every session in every project, ordered by priority,
after herdr's agent list. Sessions that need you come first, longest
wait first, then working sessions, then idle ones. Each carries its
tasks as labels, as in the top half. Terminals are never in it. A
session can appear twice, once in each half, and the cursor moves
through both.

`enter` on a session or a terminal goes to its pane. `enter` on an
issue that waits on you goes to the live session that linked to it
last, so you can ask it about the work, and opens the cockpit for the
issue when no such session is alive. `m` on an issue gives it a
verdict.

`s` on any entry of a tracked project opens a task picker in the
list's place, styled like the add-project picker. It lists the project's
open issues: ready first, then the inbox, then the rest in issues' own
order, and by priority within each. Typing narrows them by words, on
the reference and the title. `enter` on an issue that sessions already
have as a task moves the cursor to the session that linked to it last,
and on any other issue starts claude on it with the opening prompt.
This is how a pinned project with a backlog and nothing running gets
its first task.
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

`o` turns the whole window into the cockpit for an issue or a project,
and `o` or `esc` turns it back. On an issue it opens that issue's
cockpit. On a session or terminal with one task it opens that task's,
and on one with several tasks or none, its project's. On a project it
opens the project's. The list zooms its own pane
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

For an issue, in this order:

- The header: the issue's reference, title, status and priority, how
  long since a session first linked to it, the state of each live
  session that has it as a task, and the keys that act on it: `c`
  claude, `t` terminal and `m` mark, plus `enter` attach and `v` review
  while a live session has it. An issue with only terminals offers the
  first three. While the issues card
  has the keyboard, `enter` and `m` act on its issue, so the header
  drops both.
- The issue: its details from `issues show`, with the acceptance
  criteria, those still to do first, and how many are checked.
- Changes: the diff since the issue was taken on, the earliest time
  any session linked to it, as a stat per file, and how
  much is still uncommitted. Work that subagents do in other worktrees
  shows only once it reaches the project's checkout.
- Review: the open notes across the sessions that have it as a task, the replies
  waiting on you, and when notes were last sent.
- The project's other issues: those in review, those in progress with
  the sessions that have them, the blocked ones, all the ready ones,
  and the inbox count. `enter` on one that a live session has as a task
  moves the list's cursor to that session, with the cockpit following, rather
  than starting a second session. On any other issue, a paused one
  included, it starts a session, as the task picker does.

For a project, the issues card is the cockpit, under the project's
uncommitted changes. A project without `.issues/` gets a card
in the issues card's place that says what `issues` does and that
`issues init` in the project's root starts it.

The last card that does not fit whole is shown shortened in the lines
left, when there are at least three: its rule, what fits of its body,
and a line above and below counting what it leaves out there, as `↑ 3
more issues`. The issues card keeps its cursor in that view, and the
first issue in view of a group whose heading is cut takes the heading. A card is dropped, from the bottom up, only when fewer lines
than that are left. The issues card lists every issue and is the last
card in both cockpits, so it takes whatever rows the others leave and
scrolls with its cursor; the changes card above it caps its files, so
it stays short. The issue card shows four lines of the issue's summary.
When the issues card is dropped
anyway, the keyboard goes back to the list, and `tab` says the window
has no room for the card.

The cards come from running jj, git and `issues`, which the view never
does on its own goroutine. Commands run for the selected entry when it
is selected and when a hook event reports a session in its project. No
timer runs them, since every jj command snapshots the working copy. A
task's base commit is found once for the time it was taken on,
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
3. Starting a task from a ready issue.
4. Maybe: tabs. Making each task a tmux session of its own would
   give each one a tab bar of its sessions and terminals for free, from
   tmux's status line. That changes how overview lays out its windows,
   so it waits until the first three have been used for a while.
5. Sessions as rows, with their tasks as labels, and the issues that
   wait on you listed once each, as this revision describes. The links
   and the reader of commands stay. What goes is the workstream as a
   container: `Stream`, a session's primary workstream and the entries
   that alias a session under several issues. Property tests for a
   session's tasks, for the issues that wait on you, and for every
   session appearing once in each half of the list.

Each phase updates the README and adds to `scripts/e2e`.
