# One model of the fleet

Written 2026-10-24. This is the design for how overview knows what it
shows: where each fact comes from, the one place facts are joined, and
how the view turns that into the screen. It replaces the joins that the
list, the cockpit and the task picker each did for themselves.

## What went wrong

A session working on relay#54 was listed under its project, not under
the issue. Every workstream on the screen said "no sessions".

- The link between a session and an issue lived only in
  `<home>/overview/sessions/<id>.tasks`, which only `overview hook`
  writes. The hook was never installed in that config directory, so no
  session was ever linked, and nothing on screen said so.
- The links were a copy of facts the transcript already holds: the
  session's first prompt was `Work on relay#54: …`, and another
  session's transcript has `issues --dir . start 64`. The view already
  read every transcript, for titles and last prompts.
- "Which sessions work on issue X" was answered in five places, by
  `buildList`, `streamSessions`, `targetRows`, `others` and
  `streamStart`, each with slightly different rules (one counted the
  orchestrator, the others left it out). Each call rebuilt everything
  from the raw inputs, a dozen times per key press.

The tests drove the hook directly, so they passed against a setup the
real machine never had.

## The shape

```
 sources (effects, in Cmds)        model                      screen
 ────────────────────────────      ─────────────────────      ─────────────
 claude agents --json   ─┐
 transcripts (+subagents)├─► fleet.Row ─┐
 hook record (blocked)  ─┘              │
 ps (ttys, hosts)       ────────────────┤
 tmux panes             ────────────────┼─► world.Inputs ─► world.Derive ─► World
 project roots          ────────────────┤                         │
 pins                   ────────────────┤                         ├─► list (filter)
 issues list            ────────────────┘                         ├─► cockpit cards
                                                                  ├─► task picker
                                                                  └─► stage, reads
```

There are three layers.

1. **Sources.** Each source is read in a Cmd and lands in the model as a
   value, unjoined. Reading is the only effect, and each source has one
   reader.
2. **The world.** `world.Derive(Inputs) World` is a pure function and the
   only place sources are joined. It decides which project a session or
   terminal belongs to, which workstreams exist, which sessions and
   terminals sit under each, the order of everything, and the counts.
   The model derives it once per change to its inputs and keeps it. No
   other code joins two sources.
3. **Presentation.** The list, the cockpit, the task picker, the holder
   and the header read the World and the UI state (cursor, filter,
   panels) and only lay out what they find. The filter lives here,
   because it is a view of the World and not a fact about it.

## Sources

### Sessions: `fleet.Row`

`claude agents --json` says which sessions run and whether each is busy
or idle. It is the authority on which sessions exist.

The transcript is the authority on what a session has said and done. A
`fleet.Transcripts` index reads each transcript incrementally from the
offset it last reached, and folds each new line into the session's
facts:

- its title (`ai-title`) and last prompt (`last-prompt`),
- its last reply and when it was made, and
- its **links**: every `Work on <project>#<n>` that opens a user prompt
  in the main conversation, and every `issues … start <n>` run through
  the Bash tool whose result was not an error, read with the cwd the
  transcript recorded for that line and timed by the line's timestamp.

A session's subagents keep their transcripts in
`<session>/subagents/*.jsonl`. Their `issues start`s link the parent,
which is how an orchestrating session takes on several issues. Their
prompts link nothing.

The fold is pure: folding a transcript in any number of chunks gives the
facts that folding it whole gives. A file that shrinks or is replaced is
read again from the start. The parsers that read a link,
`OpeningTask` and `IssuesStarts`, are the ones the hook used, and they
keep their rule that a false link is worse than a missed one.

The hook records only what no other source can show: that a session is
blocked on a prompt, with the reason. Its record keeps the state, when it
began and the reason. It writes no links and no prompts. A session that
has never reported through the hook is `Hooked == false`. When no live
session is hooked, the header says the hook is not installed, since the
view then cannot show a blocked session.

### Everything else

| source | read by | lands as |
| --- | --- | --- |
| process table | `psTable` | ttys and hosts by pid |
| tmux panes | `readPanes` | `[]tmux.Pane`, with the roots of their directories |
| project roots | `findRoots`, on the Cmd that carries the directories | `dir → root` |
| pins | `Home.Projects` | `[]root` |
| issues | `issues list`, per tracked root, when `.issues` changes | `world.Tracker` per root |
| reviews | `readReviews` | `review.Summary` per session |
| issue details, changes | the cockpit's reads | caches by target, presentation only |

## The world

```go
package world

type Inputs struct {
	Rows     []fleet.Row
	TTYs     map[int]string     // pid → tty
	Hosts    map[int]Host       // interactive sessions with no terminal
	Panes    []tmux.Pane
	Session  string             // the view's tmux session
	Self     string             // the view's own pane
	Opening  string             // a pane c started whose session is not listed yet
	Roots    map[string]string  // dir → project root
	Pinned   []string
	Trackers map[string]Tracker // by root
	User     string             // the user's home, for names
}

type Tracker struct {
	ID     string         // the issues project id
	Open   []issues.Issue // the open issues, in the tool's order
	Failed bool           // the last read failed; Open is the last that worked
}

type World struct {
	Projects []*Project          // by name
	Away     []*Session          // unlinked sessions that other apps run
	Agents   []*Session          // every session once: needs you, working, idle
	Sessions map[string]*Session // by session id
	Streams  map[Key]*Stream
	Need, Working, Idle int
	Hooked   bool                // some live session reports through the hook
	Trackers []string            // the roots whose trackers Derive needs
}

type Project struct {
	Root, Name, ID string
	Tracked, Failed, Pinned bool
	Open      []issues.Issue // its open issues
	Streams   []*Stream      // its workstreams, in list order
	Sessions  []*Session     // sessions linked to none of its workstreams
	Terminals []*Terminal    // terminals opened for none
}

type Key struct {
	Root string
	ID   int
} // String() is "root#id", the tmux task option's value

type Stream struct {
	Key
	Project   *Project
	Issue     issues.Issue
	Sessions  []*Session  // working first, then newest activity
	Terminals []*Terminal // by window
	Started   time.Time   // the earliest link of any of its sessions
}

type Session struct {
	fleet.Row
	Project *Project  // its cwd's project
	Streams []*Stream // linked open issues, oldest link first; the last is primary
	Pane    string    // the pane that shows it in the view's session, "" for none
	Host    *Host     // the app it runs inside, nil when it has a terminal
}

type Terminal struct {
	Window, Pane, Title, Path string
	Project *Project
	Stream  *Stream // the workstream it was opened for, nil for none
}
```

The rules that used to be spread across the view now live in `Derive`.

- The orchestrator is not part of the world.
- A session's project is the root of its cwd. A link resolves to a
  workstream as `resolveTask` resolves it today: the link's project id,
  or else its directory's tracker's id, preferring the session's own
  project, and only when that project lists the issue as open.
- A workstream exists for each open issue that is in progress, in
  review or blocked, or that a live session is linked to. It sorts by
  status rank, then priority, then number.
- A linked session is listed under each of its workstreams and not in
  its project. A terminal opened for a workstream is listed under it.
- A session that runs inside another app, and is linked to nothing,
  goes in Away.
- A pinned project with nothing in it is still a Project, with empty
  lists.
- `Agents` orders every session: those that need you first, longest
  wait first, then working, then idle.
- `Need` counts the sessions that need you and the workstreams in
  review. `Working` and `Idle` count each session once.
- `Trackers` names the roots of the projects in the world and of the
  directories links name, which are the trackers a refresh reads.

`Derive` is a function of its inputs as sets: shuffling any input slice
or map gives the same World. Every pointer in a World points into the
same World, and nothing changes a World once it is built.

## Presentation

The list is `present(World, query) []section`: the World's projects with
their workstreams, sessions and terminals, then Away, then Agents, each
kept or dropped by the query. A workstream that matches the query keeps
everything under it. It does not regroup or reorder anything.

The cockpit's target is a `*Stream` or a `*Project` from the World. Its
header lists `Stream.Sessions` and `Stream.Terminals`. Its changes count
from `Stream.Started`. Its issues card counts `len(Stream.Sessions)` for
the other issues. The task picker reads the same `Stream` for the number
of sessions on each issue, and `enter` goes to that workstream when it
has any. The stage looks up `Session.Pane`.

## Control flow

The model holds three kinds of state, and nothing else:

- **inputs**: the latest value from each source, in `world.Inputs`,
  changed only by the message that delivers it, which bumps a
  generation;
- **UI state**: cursor, filter, panels, cockpit and pickers, changed
  only by `apply`;
- **effect bookkeeping**: which reads, spawns and zooms are in flight.

`Update` runs in a fixed order:

1. `reduce`: the message changes inputs or UI state.
2. `derive`: when the inputs' generation moved, `world.Derive` runs once
   and the World replaces the old one. The list is memoized on the
   World and the query.
3. `settle`: the cursor, the panels and the cockpit's pick are made
   valid for the new list.
4. `effects`: the desired tmux layout, frames and reads are compared with
   what is in flight, and the differences go out as Cmds.

Nothing in steps 2 to 4 changes inputs, so one message makes one World.

## Testing

- The transcript fold: property tests that chunked folds equal the whole
  fold, that links come only from the two shapes, and that subagent
  prompts link nothing. Table tests on real transcript lines.
- `Derive`: property tests that it ignores input order, that every
  session appears exactly once in Agents, that a linked session is under
  each of its open workstreams and not in its project, that counts match
  a recount, and that a workstream's Started is the minimum of its links.
- `present`: an empty query shows the whole World, and a query never
  adds an entry.
- The e2e fake claude writes a transcript with its first prompt, so the
  workstream test links a session the way a real one is linked, with no
  hook.
