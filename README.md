# overview

A terminal view of every Claude Code session running under one config
directory, with a periodic digest of what the sessions did and what is
waiting on you.

```
overview --home work        # live view of ~/.claude-work
overview sessions           # print the sessions once
overview digest             # write a digest now
```

`--home` takes a path or a short name: `work` means `~/.claude-work`. Without
the flag, the `home =` line of overview's config file decides. With neither,
overview stops rather than guess, so pass `--home ~/.claude` for Claude
Code's default directory. One process looks at one config directory, and
all of its state lives in `<home>/overview/`, so personal and work
sessions never appear together.

## How it works

Three sources feed the view. None of them needs a model.

- `claude agents --json` says which sessions are running and whether each is
  busy or idle.
- A hook records what that listing cannot show, mainly that a session is
  blocked on a permission prompt. Each session has one small file in
  `<home>/overview/sessions/`, and a second, `<id>.tasks`, once it is
  linked to an issue.
- The session's transcript supplies its title and last prompt.

A session that has not loaded the hook still appears. It shows busy or idle
only, and never blocked.

The digest reads the transcripts changed since the last digest, extracts each
session's prompts and final replies, and asks a model to sort them into
"needs your attention", "finished" and "in progress". The model runs through
`claude -p` with no tools and receives only that extract, so a digest cannot
change anything. When nothing happened, no model is called. Digests are kept
in `<home>/overview/digests/`.

## The view

`overview` runs in a tmux server of its own, on the socket `overview`, and
attaches your terminal to it. `--socket <name>` or a `socket =` line in the
config file picks another name, which keeps a trial run off the server you
use. The list of sessions runs down the left side,
and the selected session itself takes the rest, live, so you can read it and
type into it without leaving the list. Run `overview` again from
any terminal and you get the same view back, with every session still running.
If the settings have changed since the view started, from a flag such as
another `--theme` or from the config file, or overview has been rebuilt, the
view restarts with them in the same pane, and the session beside it keeps
running.

The list starts with a header: the home's name, then how many sessions
need you (`!`), how many work (`●`) and how many are idle (`○`). Below it
each project has a section, headed by a rule with its name. A project is
the nearest parent directory with a `.jj` or `.git` in it, or the session's
own directory when there is none, and projects are in alphabetical order.
Every entry takes one line. Inside a project, working sessions come before
idle ones, and the latest activity comes first. A session's row shows its
state and for how long: `!` blocked, `?` asking, a spinner while it works,
and `○` when idle. A session needs you when it is blocked, or when it is
idle and its last reply ends with a question. The project's terminals come
after its sessions, marked `$`, and a project you pinned is listed even
with nothing in it. A session that another app started, such as an editor
plugin's chat panel, has no terminal. Unless it works on a task, it goes
in a section of its own, "in other apps", dimmed and named after the app
that runs it. When the entry under the cursor goes away, the cursor moves
to the one after it, or to the one before it when it was last.

The agents list comes last. It holds every session in the list once more,
in order of priority: those that need you first, longest wait first, then
the working ones, then the idle ones. A session there acts exactly as it
does in its project, and its row names its project, or the task it works
on as `#6`. When the whole list fits, the agents list sits at the bottom of
the pane; otherwise it follows the projects. The footer shows the keys for
the selected entry, or a note when overview has something to say.

A project that tracks its work with the `issues` tool, in a `.issues`
directory, groups its sessions by task. A task, or workstream, is an open
issue that is in progress, in review or blocked, or that a session works
on. Its row shows the issue's number and title, its status unless it is in
progress, and its priority. A project's workstreams come before its other
entries: those in review first, then those in progress, then the blocked
ones, and by priority within each. The sessions and terminals of a
workstream follow its row, indented, and a workstream with none says `no
sessions`. A session works on an issue when a prompt, the first or any
later one, starts with `Work on <project>#<n>`, or when it or one of its
subagents runs `issues start <n>`. The hook keeps these links in
`<id>.tasks`, beside the session's record. One linked to several open
issues, as an orchestrating session running several tasks may be, is
listed under each, and its primary entry is under the one it was linked
to last. When an issue is done or dropped, its workstream goes, and its
sessions go back to the project. A workstream in review counts as needing
you. overview reads a project's issues with the `issues` binary, and reads
them again only when a file in `.issues` changes. The list never waits
for a read, and a run that takes more than three seconds is stopped.
When the last read of a project's issues failed, its heading says
`issues failed` where it would count the tasks, and its cockpit's issues
card says why. Without the binary or a `.issues`, a project's sessions are listed as
above.

`o` opens the cockpit for the selected entry: for its workstream, if it
is one or sits under one, and otherwise for its project. The list's pane
zooms to fill its window, so the session beside it keeps running out of
sight and comes back as it was. The list stays down the left at its
usual width, and cards about the workstream or project take the rest.
When the window is too narrow for both, only the cards show. `o` or
`esc` closes the cockpit. So does going to another pane, since tmux ends
the zoom then. A pane of the window closing also ends tmux's zoom, but
the cockpit stays open and zooms again; a list left alone in its window
fills it without a zoom. While the cockpit is open the list stays in its window,
and the cards follow the cursor. `enter`, `c`, `t` and `v` close it
first, then do what they always do.

A workstream's cockpit starts with a header: the issue's reference,
title, status and priority, how long ago its first session started, and
its sessions and terminals. Then come the issue's details from `issues
show`, with its acceptance criteria and how many are done. The changes
and the review sit side by side in a wide window. The changes are the
files changed since the first session started, most changed first, and
how many of them are still uncommitted. The review counts the open notes
across the workstream's sessions and the replies waiting on you. Last
are the project's other open issues: those in review, those in progress,
the blocked ones, the next three ready ones by priority, and how many sit
in the inbox. A
project's cockpit shows its root and the kind of repository, its
sessions and terminals, its issues, and its uncommitted changes. A card
the window has no room for is left out, from the bottom up.

`tab` moves the keyboard into the issues card and back. There, `j` and
`k` move between the issues, and `enter` starts a session on the
selected one, with the first prompt `Work on <project>#<n>: <title>`, as
`c` does on a workstream. The cockpit closes, and the new session shows
beside the list with the keyboard in it.

A project that does not track its tasks gets a card in the issues
card's place. It says what the `issues` tool would add, and that
`issues init` in the project's root starts it, or that the tool needs
installing when overview has none. overview reads the changes with git
or jj when the cockpit opens, every five seconds while it is open, and
when one of its sessions changes state. It reads an issue's details
again only when a file in `.issues` changes.

A terminal is a window in overview's tmux that runs no Claude session: a
shell for a one-off command, or a dev server. `t` opens one at the root of
the selected entry's project. Its row is named after what runs in it, such
as `node`, or after the shell when nothing else does, and shows the
directory it is in. A terminal belongs to the project it was opened for. One
that overview did not open, such as a window you made with `ctrl+b c`,
belongs to the project of the directory its first pane is in.

The right side follows the cursor. Each session that runs in overview's tmux
has a tmux window of its own in the session `overview-<home>`, and so does
each terminal. Moving the cursor moves the list into the selected entry's
window. A session with no pane there, such as a background session, one in
another app, or one started in some other terminal or in the overview of
another config directory, gets a pane that shows where it runs, what it is
waiting on, and its last few turns from the transcript.

You can split the right side as you would any tmux window. The prefix is
tmux's default `ctrl+b`, and `ctrl+b %` and `ctrl+b "` split the current pane
in its own directory. The splits belong to the session's window, so they go
when you move to another session and come back with it. `ctrl+[` returns to
the list from any of them.

While the keyboard is in a pane on the right, the list stays where it is,
beside you. The cursor follows the window you are typing in: when Claude
exits and leaves a split, the window becomes a terminal and the cursor
stays on it, and when you start `claude` in a terminal, or a session's id
changes, the cursor moves to the session there. Otherwise the cursor stays
put, even when a search stops matching its entry; the search is cleared
when you come back. Once the keyboard is back in the list, or you use the
mouse or a key there, the cursor's own rules apply again.

Keys: `j`/`k` move, `n` jumps to the next session that needs you or
workstream in review, and `/` searches. Search looks at the project,
title, last prompt and name, and the app's name for a session in another
app, ignoring case. A workstream matches on its reference, such as
`overview#6` or `#6`, its title and its project, and when it matches,
everything under it shows. Each space-separated word has to match one of
those fields, and a word never matches across two of them. The cursor
moves to the first match, `up` and `down` step through the matches, and
`esc` clears the search. `enter` (or `tab`) moves the keyboard into the
selected session, and `ctrl+[` brings it back to the list from any pane on
the right. On a background session, `enter` first attaches to it in a
window of its own; the attach stays there for when you come back to it. On
a session in another app, `enter` brings that app to the front. On a
workstream, `enter` does what it does on the entry shown beside it: the
first session or terminal under it with a pane, or else its first
session.
While you are searching, `enter` acts on the selected match and clears the
search. On a terminal, `enter` moves the keyboard into the pane right of
the list, and of several splits there, the one you used last. `x` stops a
background session or closes a terminal, and asks first; closing a
terminal kills what runs in it. `y` copies the attach command. To end a
session in another app, close it in that app. `v` reviews the selected
session's changes, as the next section describes. `c` starts a new session
at the root of the selected entry's project, or where overview was started
when the list is empty, shows it on the right and moves the keyboard into
it. On a workstream, or on anything under one, the new session's first
prompt is `Work on <project>#<n>: <title>`, so it works on that issue. `t`
does the same with a terminal, and a terminal opened on a workstream is
listed under it. `d` shows the digests, where `[` and `]` step through
older ones and `D` writes a new one. `o` opens the cockpit, as described
above. `q` detaches, leaving the view and its
sessions running, and `Q` quits the view. `?` lists every key.

`a` adds a project to the list. A picker takes the list's place and offers
the folders beside the projects already listed, and typing words narrows
them down as search does. A query that starts with `/` or `~/` is a path
instead: the picker offers the directories in it, and `tab` completes the
selected one. `enter` pins the project the selected directory belongs to,
or the path you typed when nothing matches. A path that ends in `/`, as
`tab` leaves it, selects nothing until you press `down`, so `enter` pins
that directory's project. `esc` closes the picker. A
pinned project stays in the list when nothing runs in it, on a row of its
own that offers `c` and `t`, and either starts something in it. `x` on that row unpins
the project. The pinned projects are kept in `<home>/overview/projects`.

The mouse works too. A click selects a session, a double click moves the
keyboard into it as `enter` does, and the wheel moves through the list and
scrolls the digests. A click also closes help and cancels a stop. tmux has
the mouse on, so a click on a pane focuses it, and you can drag the borders
between panes. The list keeps the width you drag it to, in every window it
moves into and when the terminal is resized. It stays at least 24 columns
wide and leaves 20 beside it, when the window has room for both. Most
terminals, Ghostty included, select text when you hold shift.

The first run writes overview's tmux settings to `tmux.conf` in overview's
config directory (`~/.config/overview` unless `--config` names another).
From then on the file is yours to change, and overview never writes it
again. tmux reads it when the server starts. To apply an edit to the
running server, press `ctrl+b :` inside overview and run `source-file`
with the file's path. The `ctrl+[` binding and the splits live there.
tmux can only tell `ctrl+[` from Esc when the terminal sends extended
keys. A terminal that does not sends a plain Esc, which goes
to the session. Sessions started from overview get `CLAUDE_CONFIG_DIR`
set to the config directory it is watching, so they show up in the list.
Terminals get it too, so a `claude` you type into one shows up as well.
That is Claude Code's own way to pick a config directory, and the only
environment variable overview sets or reads.

## Reviewing a session's changes

`v` on a session opens a review of what it changed, in a split above
the session, and moves the keyboard into it. `v` again, from the list,
goes back to that review. The review shows the diff side by side when
the pane is wide enough and inline otherwise, and `m` switches between
the two. It starts from where the repository was when the session
started, so the commits the session made are part of it, and `b`
switches to only the changes not yet committed. It works the same in a
git repository and a jj one.

You write notes on the diff. `enter` or `c` opens a note on the line
under the cursor, and `v` there starts a range: move to the range's
other end and press `enter`. `enter` saves the note, `ctrl+j` starts a
new line in it and `esc` cancels, asking first when you have written
something. A note sits under its lines, and `enter` on a note replies
to it. `e` edits a note or reply you have not sent yet.

`s` sends everything you have not sent yet to the session, as one
message pasted into its prompt, with the commands the agent answers
with. That works whether the session is idle or busy: a busy session
reads the message after its current step. Sending refuses when the
prompt has a draft in it or the session is showing a question, so it
never mixes into what you were typing.

The agent answers each note in its thread with `overview review reply`,
and can say it has dealt with one with `overview review addressed`. Its
replies appear under your notes as it writes them, and the diff reloads
as the session works. The list shows how many notes a session has open,
and `reply` when an answer waits for you. A note is never deleted: `x`
resolves it, and `x` again reopens it. Only you can do either; nothing
the agent runs can. `h` shows the resolved notes again.

`z` makes the review taller, then gives it the whole window, then puts
it back. `?` lists every key, and `q` closes the review. The notes stay
in `<home>/overview/reviews/`, one file per session, and come back when
you open the review again.

## Configuration

overview takes its settings from flags and from one file,
`~/.config/overview/config`, and reads them once when it starts. `--config
<dir>` uses another directory in place of `~/.config/overview`, for the
file, the themes and `tmux.conf` alike. The file holds `key = value` lines
for `home`, `theme`, `socket`, `claude` and `issues`, each taking what its flag takes.
Lines starting with `#` are comments, and a comment cannot follow a value on
the same line:

```
# the Claude Code config directory to watch
home   = work
theme  = revdiff
socket = overview
claude = /opt/bin/claude
issues = ~/.local/bin/issues
```

A flag wins over the file, and the file over the default. An unknown key is
an error. A theme, claude or issues given as a path, with a `/` in it, may start
with `~/`. A relative one is from the current directory in a flag and from
the config directory in the file. `--claude` and `claude =` pick the claude
that overview runs, by default `claude` on your PATH, which overview looks
up as it starts so the view and its panes run that one whatever PATH its
tmux has. `--issues` and `issues =` pick the `issues` CLI the same way, by
default `issues` on your PATH. It is optional: if there is no `issues` on
your PATH, nothing is wrong and overview works as before. One you name
with `--issues` or `issues =` that cannot be run stops overview as it
starts. `--orchestrator-dir` runs the orchestrator
somewhere other than the `orchestrator/` directory beside the `bin/` that
holds overview.

The list itself runs as `overview view`, which overview starts in its tmux
with every setting given as a flag, resolved, including the path of the
tmux socket, and which reads no config file. It finds its own pane by its
process id and talks to no other tmux server. You never need to run it
yourself.

## Themes

The view comes with three themes: `one-dark`, the default, after Atom's One
Dark; `one-light`, after Atom's One Light; and `revdiff`, with revdiff's
amber accent. Themes draw on the terminal's own background, so `one-light`
needs a light terminal. `--theme` picks one for a
run. Without the flag, overview uses the `theme =` line in
`~/.config/overview/config`, then `one-dark`:

```
theme = revdiff
```

A theme is a file of `key = colour` lines. A colour is `#rgb`, `#rrggbb` or
an ANSI 256-colour index, and lines starting with `#` are comments.
`overview themes` lists the themes and marks the one in use, and
`overview themes <name>` prints one with all its keys, which is the easiest
way to start your own:

```
overview themes one-dark > ~/.config/overview/themes/mine.theme
```

A file in `~/.config/overview/themes/` is found by its name without the
`.theme`, and it replaces a built-in of the same name. `--theme` also takes a
path. A theme only needs the colours it changes. The rest come from
`one-dark`, or from the built-in named on a `base =` line:

```
base   = revdiff
accent = #c678dd
```

The keys are `accent` (headings, the home's name, keys in the footer and
the help), `border` (rules, and the cursor's bar while the keyboard is
elsewhere), `text`, `muted`, `cursor-bg` (the cursor's bar while the list
has the keyboard), `cursor-dim-bg` (the background of the selected row),
`status-fg` and `status-bg` (the review's bottom bar),
`pill-fg` (text on the state badge beside a session's details), `blocked`,
`asking`, `working`,
`code`, and for reviews `added` and `removed` (the signs and numbers of
changed lines), `added-bg` and `removed-bg` (their backgrounds),
`added-word-bg` and `removed-word-bg` (the words that changed), and
`syntax`, the name of the chroma style that colours code, such as
`onedark` or `github`. An unknown key, a bad colour or an unknown style
is an error, so a typo does not silently fall back to the default.

## Install

overview needs tmux.

```
go build -o bin/overview . && ln -sf "$PWD/bin/overview" ~/.local/bin/overview
```

Then add the hook to the config directory's `settings.json`, beside any hooks
already there, for each of `SessionStart`, `UserPromptSubmit`, `PostToolUse`,
`Notification`, `Stop` and `SessionEnd`:

```json
{"hooks": [{"type": "command", "command": "\"$HOME\"/.local/bin/overview hook"}]}
```

Sessions that do not read `settings.json` have to be given the hook by their
launcher. The hook finds the config directory from the transcript path that
Claude Code hands it. overview's own digest runs start with every hook
turned off, so they never show up as sessions.

Digests come from a background session, the orchestrator. `overview
orchestrator start` starts it, and `stop` stands it down. Its schedule lives
in the session itself and is lost when the process restarts, so on macOS a
launchd agent runs `overview orchestrator ensure` every 15 minutes to restart
it when it goes silent:

```
scripts/launchd install work    # or remove, or print to see the agent first
```

The agent runs the `overview` and `claude` that are on your PATH when you
install it, by their full paths, so it needs no PATH of its own. It logs
to `~/Library/Logs/overview/`.

## Limits

- An interrupted turn fires no hook. The view falls back to Claude Code's own
  busy or idle in that case, but a session interrupted while blocked stays
  marked blocked until its next prompt.
- The view never sends a session a message, except a review's notes when
  you press `s`. It shows you the session's own terminal, and stopping
  keeps its conversation.
- Only you resolve a note through overview, and the agent has no command
  for it. A program running as you could still write to the review's
  file directly.
- Sending notes reads Claude Code's prompt off the screen to check it is
  empty. A Claude Code that draws its prompt differently makes sending
  refuse until overview learns the new look.
- A terminal is told from a session by its panes: a window with a pane on
  the tty of a listed Claude session is that session's. A Claude typed into
  a terminal turns the terminal into a session once Claude Code lists it,
  and when the session ends the window is a terminal again.
- Only sessions started inside overview's tmux can be shown live. A session
  started in another terminal stays there, and the right side only describes
  it.
