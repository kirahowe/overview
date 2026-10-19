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
  `<home>/overview/sessions/`.
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

The list puts the sessions that need you in a section at the top: a session
needs you when it is blocked, or when it is idle and its last reply ends with
a question, and the longest wait comes first. Every other session sits in a
section for its project, which is the nearest parent directory with a `.jj` or
`.git` in it, or the session's own directory when there is none. Projects are
in alphabetical order. Inside one, working sessions come before idle ones, and
the latest activity comes first. The project's terminals come after its
sessions, marked `$`, and a project you pinned is listed even with nothing
in it. A session that another app started, such as
an editor plugin's chat panel, has no terminal. Unless it needs you, it goes
in a last section, "in other apps", dimmed and named after the app that runs
it. A session appears once, and each row shows its state. When the entry
under the cursor goes away, the cursor moves to the one after it, or to the
one before it when it was last.

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

Keys: `j`/`k` move, `n` jumps to the next session that needs you, and `/`
searches. Search looks at the project, title, last prompt and name, and
the app's name for a session in another app, ignoring case. Each
space-separated word has to match one of those fields, and a word never
matches across two of them. The cursor moves to the first match, `up` and
`down` step through the matches, and `esc` clears the search. `enter` (or
`tab`) moves the keyboard into the selected session, and `ctrl+[` brings it
back to the list from any pane on the right. On a background session,
`enter` first attaches to it in a window of its own; the attach stays there
for when you come back to it. On a session in another app, `enter` brings
that app to the front. While you are searching, `enter` acts on the
selected match and clears the search. On a
terminal, `enter` moves the keyboard into the pane right of the list, and of
several splits there, the one you used last. `x` stops a background session
or closes a terminal, and asks first; closing a terminal kills what runs in
it. `y` copies the attach command. To end a session in another app, close
it in that app. `c` starts a new session at the root of the selected entry's
project, or where overview was started when the list is empty, shows it on
the right and moves the keyboard into it. `t` does the same with a
terminal. `d` shows the digests,
where `[` and `]` step through older ones and `D` writes a new one. `q`
detaches, leaving the view and its sessions running, and `Q` quits the view.
`?` lists every key.

`a` adds a project to the list. A picker takes the list's place and offers
the folders beside the projects already listed, and typing words narrows
them down as search does. A query that starts with `/` or `~/` is a path
instead: the picker offers the directories in it, and `tab` completes the
selected one. `enter` pins the project the selected directory belongs to,
or the path you typed when nothing matches. A path that ends in `/`, as
`tab` leaves it, selects nothing until you press `down`, so `enter` pins
that directory's project. `esc` closes the picker. A
pinned project stays in the list when nothing runs in it, on a row of its
own, and `c` or `t` there starts something in it. `x` on that row unpins
the project. The pinned projects are kept in `<home>/overview/projects`.

The mouse works too. A click selects a session, a double click moves the
keyboard into it as `enter` does, and the wheel moves through the list and
scrolls the digests. A click also closes help and cancels a stop. tmux has
the mouse on, so a click on a pane focuses it, and you can drag the borders
between panes. The list keeps the width you drag it to. Most terminals,
Ghostty included, select text when you hold shift.

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

## Configuration

overview takes its settings from flags and from one file,
`~/.config/overview/config`, and reads them once when it starts. `--config
<dir>` uses another directory in place of `~/.config/overview`, for the
file, the themes and `tmux.conf` alike. The file holds `key = value` lines
for `home`, `theme`, `socket` and `claude`, each taking what its flag takes.
Lines starting with `#` are comments, and a comment cannot follow a value on
the same line:

```
# the Claude Code config directory to watch
home   = work
theme  = revdiff
socket = overview
claude = /opt/bin/claude
```

A flag wins over the file, and the file over the default. An unknown key is
an error. A theme or claude given as a path, with a `/` in it, may start
with `~/`. A relative one is from the current directory in a flag and from
the config directory in the file. `--claude` and `claude =` pick the claude
that overview runs, by default `claude` on your PATH. `--orchestrator-dir` runs the orchestrator
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

The keys are `accent` (focused borders, headings, keys in the help),
`border`, `text`, `muted`, `cursor-fg` and `cursor-bg` (the selected row
while the list has the keyboard), `cursor-dim-fg` and `cursor-dim-bg` (the
selected row otherwise), `status-fg` and `status-bg` (the bottom bar),
`pill-fg` (text on the state badge beside a session's details), `blocked`,
`asking`, `working`, and
`code`. An unknown key or a bad colour is an error, so a typo does not
silently fall back to the default.

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
- The view never sends a session a message. It shows you the session's own
  terminal, and stopping keeps its conversation.
- A terminal is told from a session by its panes: a window with a pane on
  the tty of a listed Claude session is that session's. A Claude typed into
  a terminal turns the terminal into a session once Claude Code lists it,
  and when the session ends the window is a terminal again.
- Only sessions started inside overview's tmux can be shown live. A session
  started in another terminal stays there, and the right side only describes
  it.
