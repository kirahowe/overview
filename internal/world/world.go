// Package world joins what overview reads about the fleet into one model.
// Derive is a pure function of the sources' latest values, and the only
// place those sources are joined: it decides which project each session
// and terminal belongs to, which issues each session has as tasks, which
// issues wait on you, the order of everything, and the header's counts. Every view reads the
// World it returns and joins nothing itself. See docs/model-design.md.
package world

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/review"
	"overview/internal/tmux"
)

// Inputs are the latest values of the sources, unjoined. Derive treats
// every slice and map as a set, with one exception: Panes keeps tmux's
// order within a window, since a terminal is named after its window's
// first pane.
type Inputs struct {
	Rows     []fleet.Row               // the live sessions, the orchestrator among them; session ids are unique
	TTYs     map[int]string            // pid → tty, for sessions in a terminal
	Hosts    map[int]Host              // pid → the app an interactive session with no terminal runs inside
	Panes    []tmux.Pane               // every pane on the view's tmux server
	Session  string                    // the view's tmux session
	Self     string                    // the view's own pane
	Opening  string                    // a pane c started whose session is not listed yet
	Roots    map[string]string         // dir → project root, symlinks resolved
	Pinned   []string                  // the pinned projects' directories
	Trackers map[string]Tracker        // the issues of the projects that track them, by root
	Reviews  map[string]review.Summary // each session's review, by session id, for those that have one
	// Orchestrator is how the orchestrator was doing when the rows were
	// read: its health reads files and the clock, so the refresh reads it.
	Orchestrator fleet.Health
	// Written is when each transcript was last written, by path, as the
	// refresh found it.
	Written map[string]time.Time
	// Loaded says a refresh has landed, and RefreshErr why the latest one
	// failed, nil once one works. A failed refresh keeps what the last
	// one found.
	Loaded     bool
	RefreshErr error
	User       string // the user's home, for names
}

// Tracker is one project's issues as the last read of them found them.
type Tracker struct {
	ID   string         // the issues project id
	Open []issues.Issue // the open issues, in the tool's order
	// Err is why the last read failed, on one line, "" when it worked.
	// After a failure Open is what the last read that worked found.
	Err string
}

// Failed says whether the last read of the tracker failed.
func (t Tracker) Failed() bool { return t.Err != "" }

// World is the fleet as the view shows it. Nothing changes a World once
// Derive has built it, and every pointer in it points into the same
// World.
type World struct {
	// Projects are the projects the list shows, by name as NameLess
	// orders names: each with a session, a terminal or an issue that
	// waits on you in it, and each pinned one. A project with only away
	// sessions, or only issues that another project's sessions have as
	// tasks, is not listed, but is still its sessions' Project and
	// ProjectOf finds it.
	Projects  []*Project
	Away      []*Session          // the sessions other apps run, newest started first
	Agents    []*Session          // every session once: needs you, working, idle
	Sessions  map[string]*Session // by session id
	Tasks     map[Key]*Task
	Terminals []*Terminal // every terminal once, by window
	// Need counts the sessions that need you and the issues that wait on
	// you. Working and Idle count each other session once.
	Need, Working, Idle int
	// NoHook says interactive sessions are listed and none reports through
	// the hook, so none of them can show as blocked on a prompt. Background
	// sessions report that through Claude Code and need no hook.
	NoHook   bool
	Trackers []string // the roots whose trackers Derive needs, sorted
	// Orchestrator is Inputs.Orchestrator as it is, for the footer, and
	// Loaded and RefreshErr are the inputs' as they are, for the list.
	Orchestrator fleet.Health
	Loaded       bool
	RefreshErr   error

	byName  map[string]*Project // every project, listed or not
	tracked map[string]Tracker  // the trackers in the world, by root
	roots   map[string]string
	user    string
}

// Project is one section of the list: every root that shares a display
// name is one project.
type Project struct {
	Root  string   // the least of Roots, where c and s act
	Roots []string // every root with this name that the world has, sorted
	Name  string   // "~" for the user's home, else the root as fleet.ShortDir shows it
	ID    string   // the issues project id: the first tracked root's, else Root's base name
	// Tracked says some root of the project tracks its work with issues,
	// and Failed that the last read of one of those failed.
	Tracked, Failed bool
	Pinned          bool           // one of Roots is pinned
	Pins            []string       // the pinned directories it holds, as Inputs.Pinned gives them, sorted
	Open            []issues.Issue // the open issues of its tracked roots, by number then root
	Sessions        []*Session     // its sessions in a terminal, newest started first
	Members         []*Session     // every session whose cwd is in it, away or not, newest started first
	Terminals       []*Terminal    // its terminals, by window
	// Waiting are the issues that wait on you, as WaitingLess orders
	// them: those in review or blocked, and those in progress that no
	// live session is linked to.
	Waiting []*Task
}

// Empty says whether nothing is listed in the project: a project that
// only has away sessions, or issues that another project's sessions have
// as tasks, or a pinned one with nothing running yet. Of these,
// World.Projects lists only the pinned ones.
func (p *Project) Empty() bool {
	return len(p.Waiting) == 0 && len(p.Sessions) == 0 && len(p.Terminals) == 0
}

// Key names a task: its tracker's root and its issue's number.
type Key struct {
	Root string
	ID   int
}

// String is "root#id", the value of a window's tmux task option.
func (k Key) String() string { return k.Root + "#" + strconv.Itoa(k.ID) }

// ParseKey reads a Key from its String. ok is false for anything else,
// such as a window with no task option.
func ParseKey(s string) (Key, bool) {
	i := strings.LastIndexByte(s, '#')
	if i <= 0 {
		return Key{}, false
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil || strconv.Itoa(n) != s[i+1:] {
		return Key{}, false
	}
	return Key{s[:i], n}, true
}

// Task is an open issue the world has something to say about: one that
// is in progress, in review or blocked, or that a live session is linked
// to or a terminal was opened for. Whether a session has it as a task is
// the issue's status: a session has the issues in progress it is linked
// to. Every other open issue it is linked to waits on you, or on nobody.
type Task struct {
	Key
	Project *Project
	Issue   issues.Issue
	// IssuesID is the issues project id of its own root, as IssuesID has
	// it, which its ref and a session's opening prompt name. A project
	// whose roots track under different ids has a different one per root.
	IssuesID  string
	Sessions  []*Session  // the live sessions linked to it, whatever its status, the latest linked first
	Terminals []*Terminal // the terminals opened for it, by window
	// Started is when the work on it began: the earliest time any of its
	// sessions linked to it, or for a session whose links to it carry no
	// time, when that session started. Zero when none says.
	Started time.Time
}

// Ref is how the task's issue is named: its ref as the issues tool gives
// it, or else IssuesID and the number.
func (s *Task) Ref() string {
	if s.Issue.Ref != "" {
		return s.Issue.Ref
	}
	return s.IssuesID + "#" + strconv.Itoa(s.ID)
}

// Held says whether a live session has the issue as a task: it is in
// progress and a live session is linked to it.
func (s *Task) Held() bool { return s.Issue.Status == issues.InProgress && len(s.Sessions) > 0 }

// Waits says whether the issue waits on you: it is in review or blocked,
// or in progress with no live session linked to it.
func (s *Task) Waits() bool {
	switch s.Issue.Status {
	case issues.Review, issues.Blocked:
		return true
	case issues.InProgress:
		return len(s.Sessions) == 0
	}
	return false
}

// Lead is what stands for the task when it is entered, as LeadOf picks it
// from all of its sessions and terminals: the session linked to it last
// that has a pane, so enter on an issue that waits on you goes to the
// session to ask about the work.
func (s *Task) Lead() (*Session, *Terminal) { return LeadOf(s.Sessions, s.Terminals) }

// LeadOf is what stands for a task when it is entered, of the sessions and
// terminals given, in the task's order: the first session with a pane,
// else the first terminal, else the first session. Both are nil when
// there is neither.
func LeadOf(sessions []*Session, terminals []*Terminal) (*Session, *Terminal) {
	for _, x := range sessions {
		if x.Pane != "" {
			return x, nil
		}
	}
	if len(terminals) > 0 {
		return nil, terminals[0]
	}
	if len(sessions) > 0 {
		return sessions[0], nil
	}
	return nil, nil
}

// Session is a live Claude session other than the orchestrator.
type Session struct {
	fleet.Row
	Project *Project // its cwd's project
	// Holds are its tasks: the issues in progress its links resolve to,
	// its subagents' among them, each once, in the order it first linked
	// them. Row.Tasks are the links themselves.
	Holds []*Task
	Pane  string // the pane that shows it in the view's session, "" for none
	Host  *Host  // the app it runs inside, nil when it has a terminal or is a background session
	TTY   string // the terminal an interactive session runs in, "" for none
	// Elsewhere says an interactive session runs in a pane of another
	// tmux session on the view's server, such as another home's view.
	Elsewhere bool
	Review    review.Summary // its review's notes, zero for none
	Written   time.Time      // when its transcript was last written, zero if unknown
}

// Terminal is a window of the view's session that runs no Claude session:
// shells, a dev server, a one-off command.
type Terminal struct {
	Window  string // "@7"
	Pane    string // its first pane
	Title   string // "node", or the shell's name when only shells run
	Path    string // the first pane's directory
	Root    string // its project root: the window's project option, else the first pane's directory's root
	Project *Project
	Task    *Task // the open issue of its project it was opened for, nil for none
}

// Host is the app an interactive session runs inside when it has no
// terminal, such as an editor plugin's chat panel. Name is empty when no
// ancestor is an app bundle.
type Host struct {
	Name string // "Obsidian"
	Path string // "/Applications/Obsidian.app"
}

// Label is how the list and the details name the host.
func (h Host) Label() string {
	if h.Name == "" {
		return "no terminal"
	}
	return h.Name
}

// Root is the root of the project dir belongs to, "" for no dir: as
// Inputs.Roots has it, or dir itself, cleaned, when it lacks dir.
func (w World) Root(dir string) string { return rootOf(w.roots, dir) }

// Name is the display name of the project dir belongs to.
func (w World) Name(dir string) string { return nameOf(w.user, w.Root(dir)) }

// Short is a directory as the view shows it: under ~ when it is in the
// user's home.
func (w World) Short(dir string) string { return fleet.ShortDir(dir, w.user) }

// Tracker is the tracker of root as the world has it: one of a root of
// a project in it, or of a root a link resolves into. ok is false for a
// root with no tracker, or one the world does not hold.
func (w World) Tracker(root string) (t Tracker, ok bool) {
	t, ok = w.tracked[root]
	return t, ok
}

// IssuesID is the issues project id of root: its tracker's id, else the
// root's base name. It is the id of that root alone, which a prompt that
// opens work on one of its issues must name, not the project's ID.
func (w World) IssuesID(root string) string {
	if t, ok := w.tracked[root]; ok && t.ID != "" {
		return t.ID
	}
	return baseName(root)
}

// ProjectOf is the project dir belongs to, listed or not, nil when the
// world has none with its name.
func (w World) ProjectOf(dir string) *Project { return w.byName[w.Name(dir)] }

func rootOf(roots map[string]string, dir string) string {
	if dir == "" {
		return ""
	}
	if r, ok := roots[dir]; ok {
		return r
	}
	return filepath.Clean(dir)
}

func nameOf(user, root string) string {
	if user != "" && root == user {
		return "~"
	}
	return fleet.ShortDir(root, user)
}
