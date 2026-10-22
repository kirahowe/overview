# Reviewing a session's changes

Written 2026-10-21. This is the design for overview's own diff viewer,
where you review what a session changed, leave notes on lines, and send
them to the session with one key.

## What it is for

When a session finishes a change, you press `r` on it in the list. A
diff of its changes opens in a split above the session, side by side or
inline. You move through the diff, leave notes on lines or ranges, and
press `s` to send every unsent note to the session as one message. The
agent answers in the note's thread and changes the code. The diff
reloads as it does, and its replies appear under your notes. A note is
never deleted. Only you resolve one.

Hunk and revdiff were considered and set aside. Hunk renders badly in
places, is slow on large repositories and is hard to bend to this
workflow. revdiff is inline only and hands its annotations over in a
batch when it exits. Owning the viewer makes the rendering testable, and
keeps the notes in overview's own files, with no daemon in between.

## Facts settled before building

Checked against Claude Code in a private tmux server:

- A pane in copy mode, as when the user has scrolled back, takes a
  paste unbracketed, so every line would arrive as its own prompt, and
  eats the Enter. Sending takes the pane out of copy mode first, in the
  same tmux call that reads its screen.
- Pasting a message with `paste-buffer -p` (bracketed paste) and then
  sending Enter submits it when the session is idle. When it is busy, the
  message is queued and the session reads it after its current tool call,
  in the same turn. One mechanism covers both, so no hook is involved.
- A paste of 31 lines shows as `[Pasted text #1 +31 lines]` and submits
  whole.
- The prompt is a box between two lines of `─`, starting with `❯`. A
  draft shows as text after the `❯` or on the box's further lines. Shell
  mode shows `!` instead of `❯`. A dialog, such as the folder-trust
  question, has no box. Sending refuses unless the box is there and empty.
  The captures are test fixtures in `internal/review/testdata/`.
- `jj diff --git` writes git's format. A path with a space is not quoted
  in the `diff --git` line. Renames come as `rename from`/`rename to`, and
  binary files as `Binary files ... differ`.

## Packages

Each package keeps decisions pure and effects at its edges, as the rest
of overview does.

### internal/diff (pure)

The diff as data. `Parse` reads git's unified format, as both `git diff`
and `jj diff --git` write it, into `[]File`. `Format` writes it back, so
the parser is tested by round trips. `WordDiff` finds the changed spans
between a removed line and the added line paired with it. `Pairs` lines a
hunk up for the side-by-side view: context on both sides, and each run of
removals beside the additions that follow it.

### internal/vcs (pure core, thin runner)

Finds a directory's repository, jj before git, and builds the commands
that get a diff and resolve a base. A `Runner` seam runs them.

- jj: `jj --no-pager --color=never diff --git --from <base>`. The base
  for "since the session started" is the working-copy commit at the last
  operation before the session's start time, read from `jj op log` and
  `jj --at-op <op> log -r @`. "Uncommitted" is `@-`.
- git: `git diff -M <base>` with prefixes, colour and external tools
  pinned on the command line, plus untracked files from
  `git ls-files --others --exclude-standard`, shown as added files. The
  base for "since the session started" is the commit `HEAD` named at that
  time, from `git reflog`. "Uncommitted" is `HEAD`.

Everything is passed as arguments. Nothing is read from the environment.

### internal/review (pure core, store at the edge)

A review is an append-only log of events, one JSON object per line, in
`<home>/overview/reviews/<session-id>.jsonl`. The events are `note`,
`reply`, `edit`, `resolve`, `reopen`, `addressed` and `sent`. `Fold` turns
the log into notes with their threads. Nothing is ever removed from the
log, so a note can only be resolved, never lost.

Rules `Fold` enforces, whatever is in the file:

- Only the user resolves or reopens a note.
- The agent can reply and mark a note addressed. `addressed` is a claim:
  the note shows "agent says addressed" until you resolve it, and a later
  reply from you clears the claim.
- Only an unsent note or reply can be edited, by its author.

These rules bind everything overview offers the agent, which has no
command to resolve or delete. They cannot bind a program that writes
the file itself: the log is the user's file, and a process running as
the user can append an event that claims to be the user's, or remove
the file. The agent is never told the format, and it is never asked to
touch the file directly.

Writers append under an exclusive `flock` with `O_APPEND`, one write per
event batch. Readers take a shared lock, and the parser skips a torn or
malformed line, so a viewer and an agent can write to one review at once.

An anchor is a path, a side (old or new), a line range and the text of
those lines. `Locate` finds an anchor in the current diff: at its own
lines if their text still matches, else at the nearest place where the
same lines appear on that side. Failing both, the note is outdated and
keeps the code it was written against, as GitHub does.

`Message` writes the text that `s` sends: each unsent note and reply
with its location, the code it is on and its id, then the commands the
agent answers with. `Ready` reads a captured pane and says whether the
session's prompt is there and empty.

### internal/diffview (the viewer)

A bubbletea program run as `overview review view` in a pane, with every
setting as a flag, as `overview view` is.

- **Layout** is a pure function from the diff, the notes and the options
  (split or unified, width, resolved notes shown) to a list of items:
  file headers, hunk headers, diff rows and note boxes. It is rebuilt
  only when one of those changes.
- **Rendering** is a pure function from the model to the frame, and only
  draws the items on screen, so the size of the diff does not slow down
  scrolling. Text is cleaned first: tabs expand, and control characters
  and escape sequences in the code become visible placeholders. Long
  lines wrap. Every line of a frame is exactly the pane's width, which
  property tests check against wide characters, emoji, tabs and
  combining marks.
- **Syntax highlighting** runs per file in a Cmd with chroma, after the
  diff is shown. The first frames are plain, and colour arrives without
  blocking a key.
- **Reloads** follow the session: the viewer checks the transcript's
  modification time twice a second, and a change, once it has settled,
  reloads the diff. One reload runs at a time, and a result from an
  older one is dropped. The cursor stays on the same line of the same
  file across a reload.
- **The review file** is checked the same way, so the agent's replies
  appear as it writes them.

Keys: `j`/`k` move, `ctrl+d`/`ctrl+u` page, `g`/`G` go to the ends,
`]`/`[` jump between files, `}`/`{` between hunks, `n`/`N` between notes.
`enter` (or `c`) writes a note on the line, or a reply on a note. `v`
starts a range. `x` resolves or reopens a note, `e` edits an unsent one,
`s` sends, `m` switches split and unified, `b` changes the base, `z`
changes the pane's size, `h` shows resolved notes, `?` lists every key
and `q` closes the review. In the note editor `enter` saves, `ctrl+j`
starts a new line and `esc` cancels, asking first when there is text.

Without a session pane to send to, `s` copies the message instead.

### In the list

`v` on a session opens its review in a split above it, or moves to the
review when one is open. The pane is marked with the pane option
`@overview-review`, so the listing knows it. A session with notes shows
how many are open and whether a reply from the agent waits for you.

### For the agent

The message tells the agent how to answer, with the review's path:

```
overview review notes --store <path>              the open notes, with threads
overview review reply --store <path> <id> <<'EOF'      reply in a note's thread
overview review addressed --store <path> <id> [text]    say a note is dealt with
```

There is no command to resolve or delete a note.

## Testing

- `internal/diff`: parser tables from real `git` and `jj` output, round
  trips of generated files, and properties of `WordDiff` and `Pairs`.
- `internal/review`: properties of `Fold` (an agent can never resolve,
  a note never disappears, a torn log parses to a prefix), `Locate` on
  edited files, `Ready` on the captured fixtures, and concurrent appends.
- `internal/vcs`: command building and output parsing as tables, and
  real `git` and `jj` repositories in `t.TempDir()`, skipped when either
  is missing.
- `internal/diffview`: properties of the frame (exact width and height,
  no escape from the code reaching the terminal, wrapping keeps all the
  text), and key sequences through `Update`.
- `internal/tmux`: split, capture and paste on a private server.
