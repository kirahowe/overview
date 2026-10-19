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
the flag, `CLAUDE_CONFIG_DIR` decides. With neither, overview stops rather
than guess, so pass `--home ~/.claude` for Claude Code's default directory. One process looks at one config
directory, and all of its state lives in `<home>/overview/`, so personal and
work sessions never appear together.

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
attaches your terminal to it. The window has two panes. The left one lists the
sessions, and the right one is the selected session itself, live, so you can
read it and type into it without leaving the list. Run `overview` again from
any terminal and you get the same view back, with every session still running.

The list puts the sessions that need you in a section at the top: a session
needs you when it is blocked, or when it is idle and its last reply ends with
a question, and the longest wait comes first. Every other session sits in a
section for its project, which is the nearest parent directory with a `.jj` or
`.git` in it, or the session's own directory when there is none. Projects are
in alphabetical order. Inside one, working sessions come before idle ones, and
the latest activity comes first. A session that another app started, such as
an editor plugin's chat panel, has no terminal. Unless it needs you, it goes
in a last section, "in other apps", dimmed and named after the app that runs
it. A session appears once, and each row shows its state.

The right pane follows the cursor. A session that runs in overview's tmux is
shown as it is, and moving the cursor swaps another one in, while the rest
wait out of sight in the tmux session `overview-<home>-parked`. A session
with no pane there, such as a background session, one in another app, or one
started in some other terminal, gets a pane that shows where it runs, what it
is waiting on, and its last few turns from the transcript.

Keys: `j`/`k` move, `n` jumps to the next session that needs you, and `/`
searches. Search looks at the project, title, last prompt, name and label, and
the app's name for a session in another app, ignoring case. Each
space-separated word has to match one of those fields, and a word never
matches across two of them. The cursor moves to the first match, `up` and
`down` step through the matches, and `esc` clears the search. `enter` (or
`tab`) moves the keyboard into the selected session, and `ctrl+]` brings it
back to the list. On a background session, `enter` first attaches to it in
the right pane; the attach stays there for when you come back to it. On a
session in another app, `enter` brings that app to the front. While you are
searching, `enter` acts on the selected match and clears the search. `x` stops
a background session (it asks first), and `y` copies the attach command. To
end a session in another app, close it in that app. `c` starts a new session
at the root of the selected session's project, or where overview was started
when the list is empty, and shows it in the right pane. `d` shows the digests,
where `[` and `]` step through older ones and `D` writes a new one. `q`
detaches, leaving the view and its sessions running, and `Q` quits the view.
`?` lists every key.

The mouse works too. A click selects a session, a double click moves the
keyboard into it as `enter` does, and the wheel moves through the list and
scrolls the digests. A click also closes help and cancels a stop. tmux has
the mouse on, so a click on the right pane focuses it, and you can drag the
border between the panes. Most terminals, Ghostty included, select text when
you hold shift.

The first run writes overview's tmux settings to
`~/.config/overview/tmux.conf`, and from then on the file is yours to change.
tmux reads it when the server starts. After an edit, run
`tmux -L overview source-file ~/.config/overview/tmux.conf`. The `ctrl+]`
binding lives there. Sessions started from overview get `CLAUDE_CONFIG_DIR`
set to the config directory it is watching, so they show up in the list.

## Themes

The view comes with three themes: `one-dark`, the default, after Atom's One
Dark; `one-light`, after Atom's One Light; and `revdiff`, with revdiff's
amber accent. Themes draw on the terminal's own background, so `one-light`
needs a light terminal. `--theme` picks one for a
run. Without the flag, overview uses `OVERVIEW_THEME`, then a `theme =` line
in `~/.config/overview/config` (or under `$XDG_CONFIG_HOME`), then
`one-dark`:

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
launcher. A launcher can also set `OVERVIEW_LABEL` in a session's
environment, and overview shows that label on the session's row and
searches it.

Digests come from a background session, the orchestrator. `overview
orchestrator start` starts it, and `stop` stands it down. Its schedule lives
in the session itself and is lost when the process restarts, so on macOS a
launchd agent runs `overview orchestrator ensure` every 15 minutes to restart
it when it goes silent:

```
scripts/launchd install work    # or remove, or print to see the agent first
```

The agent uses whichever `overview` and `claude` are on your PATH when you
install it, and logs to `~/Library/Logs/overview/`.

## Limits

- An interrupted turn fires no hook. The view falls back to Claude Code's own
  busy or idle in that case, but a session interrupted while blocked stays
  marked blocked until its next prompt.
- The view never sends a session a message. It shows you the session's own
  terminal, and stopping keeps its conversation.
- Only sessions started inside overview's tmux can be shown live. A session
  started in another terminal stays there, and the right pane only describes
  it.
