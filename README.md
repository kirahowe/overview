# overview

A terminal view of every Claude Code session running under one config
directory, with a periodic digest of what the sessions did and what is
waiting on you.

```
overview --home kira        # live view of ~/.claude-kira
overview sessions           # print the sessions once
overview digest             # write a digest now
```

`--home` takes a path or a short name: `kira` means `~/.claude-kira`. Without
the flag, `CLAUDE_CONFIG_DIR` decides. One process looks at one config
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

`overview` opens a two-pane view. The left pane lists the sessions. A section
at the top holds the ones that need you: a session needs you when it is
blocked, or when it is idle and its last reply ends with a question, and the
longest wait comes first. Every other session sits in a section for its
project, which is the nearest parent directory with a `.jj` or `.git` in it,
or the session's own directory when there is none. Projects are in
alphabetical order. Inside one, working sessions come before idle ones, and
the latest activity comes first. A session that another app started, such as
an editor plugin's chat panel, has no terminal. Unless it needs you, it goes
in a last section, "in other apps", dimmed and named after the app that runs
it. A session appears once, and each row shows its state. The right pane shows
the selected session: where it runs, what it is waiting on, and its last few
turns from the transcript.

Keys: `j`/`k` move, `n` jumps to the next session that needs you, and `/`
searches. Search looks at the project, title, last prompt, name and label, and
the app's name for a session in another app, ignoring case. Each
space-separated word has to match one of those fields, and a word never
matches across two of them. The cursor moves to the first match, `up` and
`down` step through the matches, and `esc` clears the search. `enter` acts on
the selected session: it jumps to an interactive session's Ghostty terminal,
attaches to a background one, and brings the app to the front for a session in
another app. While you are searching, `enter` acts on the selected match and
clears the search. `a` attaches to a background session, `x` stops one (it
asks first), and `y` copies the attach command. To end a session in another app,
close it in that app. `tab` moves between the two panes. `d` shows the
digests, where `[` and `]` step through older ones and `D` writes a new one.
`?` lists every key.

The mouse works too, and does what the keys do. A click selects a session
or focuses the detail pane, a double click acts on a session as `enter`
does, and the wheel moves through the list and scrolls the detail pane and
digests. A click also closes help and cancels a stop. While the view has the
mouse, most terminals, Ghostty included, select text when you hold shift.

Jumping needs stock Ghostty 1.3 or later and its Automation permission.
Interactive sessions run in someone's terminal, so overview finds the
terminal by writing a short-lived title to the session's tty and asking
Ghostty which terminal shows it, then selects that tab and focuses the
terminal. It remembers the terminal for the next jump. It writes that title
only when the session's process runs under Ghostty. A session in tmux or in
another terminal app is left alone, so its title is never touched. When
Ghostty is not running, the session is not in Ghostty, or the terminal cannot
be found, the status line shows the tty instead, and you switch to that tab
yourself.

## Themes

The view comes with two themes: `one-dark`, the default, after Atom's One
Dark, and `revdiff`, with revdiff's amber accent. `--theme` picks one for a
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
`border`, `text`, `muted`, `cursor-fg` and `cursor-bg` (the selected row in
the focused pane), `cursor-dim-fg` and `cursor-dim-bg` (the selected row
otherwise), `status-fg` and `status-bg` (the bottom bar), `pill-fg` (text on
the state badge in the detail pane), `blocked`, `asking`, `working`, and
`code`. An unknown key or a bad colour is an error, so a typo does not
silently fall back to the default.

## Install

```
go build -o bin/overview . && ln -sf "$PWD/bin/overview" ~/.local/bin/overview
```

Then add the hook to the config directory's `settings.json`, beside any hooks
already there, for each of `SessionStart`, `UserPromptSubmit`, `PostToolUse`,
`Notification`, `Stop` and `SessionEnd`:

```json
{"hooks": [{"type": "command", "command": "/Users/kira/.local/bin/overview hook"}]}
```

Sessions that do not read `settings.json` have to be given the hook by their
launcher. `staff` does this for its members when `:overview-bin` is set in
its `config.edn`, and those sessions are labelled with the member's id.

## Limits

- An interrupted turn fires no hook. The view falls back to Claude Code's own
  busy or idle in that case, but a session interrupted while blocked stays
  marked blocked until its next prompt.
- The view never sends a session a message. Attaching hands you the session's
  own terminal, and stopping keeps its conversation.
