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

The live view writes a digest every hour while it is open (`--every 30m` to
change that, `--every 0` to turn it off). Press `d` to write one now.

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
- The view is read only. It does not message or steer sessions.
