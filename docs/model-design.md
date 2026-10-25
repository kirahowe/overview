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
has never reported through the hook is `Hooked == false`. Claude Code
reports a background session's blocked state itself, so only interactive
sessions need the hook. When interactive sessions are listed and none is
hooked, the header says the hook is not installed, since the view then
cannot show any of them as blocked.

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
	Rows     []fleet.Row        // session ids are unique
	TTYs     map[int]string     // pid → tty
	Hosts    map[int]Host       // interactive sessions with no terminal
	Panes    []tmux.Pane        // in tmux's order
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
	Projects  []*Project          // the projects the list shows, by name
	Away      []*Session          // unlinked sessions that other apps run
	Agents    []*Session          // every session once: needs you, working, idle
	Sessions  map[string]*Session // by session id
	Streams   map[Key]*Stream
	Terminals []*Terminal         // every terminal once, by window
	Need, Working, Idle int
	NoHook    bool                // interactive sessions are listed and none reports through the hook
	Trackers  []string            // the roots whose trackers Derive needs, sorted
}

type Project struct {
	Root      string         // the least of Roots, where c and s act
	Roots     []string       // every root in the world with this name
	Name, ID  string
	Tracked, Failed, Pinned bool
	Pins      []string       // its pinned directories, as given, sorted
	Open      []issues.Issue // its open issues, by number then root
	Streams   []*Stream      // its workstreams, in list order
	Sessions  []*Session     // sessions in a terminal linked to none of its workstreams
	Members   []*Session     // every session whose cwd is in it, linked, away or not
	Terminals []*Terminal    // terminals opened for none of its workstreams
	AllTerminals []*Terminal // every terminal in it, its workstreams' too, by window
}

type Key struct {
	Root string
	ID   int
} // String() is "root#id", the tmux task option's value; ParseKey reads it back

type Stream struct {
	Key
	Project   *Project
	Issue     issues.Issue
	Sessions  []*Session  // working first, then newest activity
	Terminals []*Terminal // by window
	Started   time.Time   // the earliest link of any of its sessions, zero for none
}

type Session struct {
	fleet.Row
	Project   *Project  // its cwd's project
	Streams   []*Stream // linked open issues, each at its last link; the last is primary
	Pane      string    // the pane that shows it in the view's session, "" for none
	Host      *Host     // the app it runs inside, nil when it has a terminal
	TTY       string    // the terminal an interactive session runs in, "" for none
	Elsewhere bool      // it runs in a pane of another tmux session on the server
}

type Terminal struct {
	Window, Pane, Title, Path string
	Root    string   // the window's project option, else its first pane's root
	Project *Project
	Stream  *Stream  // the workstream it was opened for, nil for none
}

type Host struct{ Name, Path string } // Label() is how the view names it
```

A few helpers read the World without joining anything:
`Session.Primary()` is the session's last workstream, `Stream.Lead()`
is what enter goes to (its first session with a pane, else its first
terminal, else its first session), and `Project.Empty()` says nothing
is listed in a project. `World.Root(dir)`, `World.Name(dir)` and
`World.ProjectOf(dir)` look a directory up the way `Derive` does, and
`World.Short(dir)` shows it as the view does.
`NameLess`, `ActiveLess`, `StreamLess` and `WindowLess` are the orders
`Derive` uses.

The rules that used to be spread across the view now live in `Derive`.

- The orchestrator is not part of the world: not its sessions, its
  links, its refs' directories nor its hook state. Its tty still keeps
  its pane from being a terminal.
- A session's project is the root of its cwd, as `Inputs.Roots` has it,
  or else the cwd, cleaned. A pinned directory goes through the same
  lookup. A project is named `~` for the user's home and otherwise as
  `fleet.ShortDir` shows its root. Every root with one name is one
  project.
- A link resolves to a workstream as `resolveTask` resolves it: the
  link's project id, or else its directory's tracker's id, preferring
  the session's own project, and only when that project lists the issue
  as open. A session's `Streams` follow its links in order, each
  workstream once, at the place it was last linked.
- A workstream exists for each open issue that is in progress, in
  review or blocked, or that a live session is linked to. It sorts by
  status rank, then priority, then number, then root.
- A linked session is listed under each of its workstreams and not in
  its project's `Sessions`. A terminal opened for a workstream of its
  own project is listed under it, and otherwise in its project.
- A session that runs inside another app, and is linked to nothing,
  goes in Away.
- `Projects` lists each project with a workstream, a session or a
  terminal in it, and each pinned one, with empty lists if it has
  nothing. A project with only away sessions, or only sessions linked
  into another project's workstreams, is not listed, but it is still
  its sessions' `Project`, `ProjectOf` finds it, and its `Members` list
  them.
- `Agents` orders every session: those that need you first, longest
  wait first, then working, then idle.
- `Started` is the earliest `Linked` time of its sessions' links to it,
  or for a session whose links carry no time, when that session
  started.
- `Need` counts the sessions that need you and the workstreams in
  review. `Working` and `Idle` count each session once.
- `Trackers` names the roots of every project, listed or not, and of
  the directories links name, which are the trackers a refresh reads.

`Derive` treats its inputs as sets wherever their order means nothing:
shuffling the rows, the pins or a tracker's issues gives the same
World, and so does any order of the panes that keeps each window's
panes in tmux's order, since a terminal is named after its window's
first pane. A row's `Tasks` keep their order, because the last link is
the primary one. `Project.Open` is sorted by number, so the tool's
order does not reach the World. Every pointer in a World points into
the same World, and nothing changes a World once it is built.

## Presentation

The list is `present(World, query) []section`: the World's projects with
their workstreams, sessions and terminals, then Away, then Agents, each
kept or dropped by the query. A workstream that matches the query keeps
everything under it. A listed project with nothing in it is a
placeholder, keyed by its first pin, which `x` unpins. `present` does not
regroup or reorder anything. Each entry points into the World: a
`*Session`, `*Terminal`, `*Stream` or `*Project`.

The header's counts are `World.Need`, `Working` and `Idle`. When
`World.NoHook` is set, the list's title says the hook is not installed.

The cockpit's target is a `*Stream` or a `*Project` from the World. Its
header lists `Stream.Sessions` and `Stream.Terminals`, or a project's
`Members` and `AllTerminals`. Its changes count
from `Stream.Started`. Its issues card counts `len(Stream.Sessions)` for
the other issues. The task picker reads the same `Stream` for the number
of sessions on each issue, and `enter` goes to that workstream when it
has any. The stage looks up `Session.Pane`, and the holder and the
details read `Session.Host`, `TTY` and `Elsewhere`.

In the model, `setInputs` is the one place the inputs change. It takes
the message's change, composes `Roots` from the refreshes' and the pane
listings' roots, `Trackers` from the reads that landed, and `Opening`
from the pane `c` opened, and takes a new generation. `m.world()` and
`m.sections()` are memoized on the generation (and the filter), through
a cache that every copy of the model shares, so a key press that changes
no input derives nothing.

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
- `Derive`: property tests that it ignores input order as above, that
  every session appears exactly once in Agents, that a linked session is
  under each of its open workstreams and not in its project, that
  `Projects` lists exactly the projects with something in them and the
  pinned ones, that the `Members` lists partition the sessions, that
  counts match a recount, that a workstream's Started is the minimum of
  its links, and that every pointer leads back into the World.
- `present`: an empty query shows the whole World, and a query never
  adds an entry.
- The e2e fake claude writes a transcript with its first prompt, so the
  workstream test links a session the way a real one is linked, with no
  hook.
