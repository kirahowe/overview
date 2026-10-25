package tui

import (
	"maps"
	"sync"

	"overview/internal/issues"
	"overview/internal/tmux"
	"overview/internal/world"
)

// The model keeps the sources' latest values in a world.Inputs, and every
// fact the views show comes from the World derived from them, once per
// change. See docs/model-design.md.

// setInputs changes the inputs, the only place they change, and gives
// them a new generation. change sets what its message delivers, and may
// be nil when only the model's own bookkeeping moved. The rest is composed
// from the model as it is: the roots from the refreshes' and the pane
// listings', the trackers from the reads that landed, the pane c opened,
// and who the view is.
func (m model) setInputs(change func(*world.Inputs)) model {
	in := m.in
	if change != nil {
		change(&in)
	}
	roots := maps.Clone(m.paneRoots)
	if roots == nil {
		roots = map[string]string{}
	}
	// A refresh's root wins over a listing's, as it is the newer read of
	// a session's directory.
	maps.Copy(roots, m.rowRoots)
	in.Roots = roots
	in.Trackers = m.trackers.inputs()
	in.Opening = m.opened.ID
	in.Session, in.Self, in.User = m.session, m.self, m.user
	m.in = in
	m.gen = m.memo.next()
	return m
}

// setOpened sets the pane c started, which is no terminal while it waits
// for its session to be listed.
func (m model) setOpened(p tmux.Pane) model {
	was := m.opened.ID
	m.opened = p
	if p.ID != was {
		return m.setInputs(nil)
	}
	return m
}

// inputs is what the World is told of the trackers: each one read, with
// its last good issues, and whether its last read failed.
func (ts trackers) inputs() map[string]world.Tracker {
	out := make(map[string]world.Tracker, len(ts))
	for r, t := range ts {
		out[r] = world.Tracker{ID: t.id, Open: t.issues, Failed: t.err != nil}
	}
	return out
}

// memo holds the Worlds and lists derived for the latest generations. It
// is shared by every copy of the model, as a model and the one before it
// are both read in one update, so it keeps a few of each. Generations come
// from it too, so no two copies of the model ever share one with
// different inputs.
type memo struct {
	mu     sync.Mutex
	gen    int
	worlds []memoWorld
	lists  []memoList
}

type memoWorld struct {
	gen int
	w   *world.World
}

type memoList struct {
	gen    int
	filter string
	secs   []section
}

// memoKeep is how many Worlds and lists the memo keeps.
const memoKeep = 4

// next is a new generation.
func (c *memo) next() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	return c.gen
}

// world is the World of the model's inputs, derived once per generation.
func (m model) world() *world.World {
	derive := m.deps.derive
	if derive == nil {
		derive = world.Derive
	}
	if m.memo == nil {
		w := derive(m.in)
		return &w
	}
	m.memo.mu.Lock()
	defer m.memo.mu.Unlock()
	for _, c := range m.memo.worlds {
		if c.gen == m.gen {
			return c.w
		}
	}
	w := derive(m.in)
	m.memo.worlds = keepLast(append(m.memo.worlds, memoWorld{m.gen, &w}))
	return &w
}

// sections is the list as shown, with the filter applied, presented once
// per generation and filter.
func (m model) sections() []section {
	w := m.world()
	if m.memo == nil {
		return present(w, m.filter)
	}
	m.memo.mu.Lock()
	for _, c := range m.memo.lists {
		if c.gen == m.gen && c.filter == m.filter {
			m.memo.mu.Unlock()
			return c.secs
		}
	}
	m.memo.mu.Unlock()
	secs := present(w, m.filter)
	m.memo.mu.Lock()
	m.memo.lists = keepLast(append(m.memo.lists, memoList{m.gen, m.filter, secs}))
	m.memo.mu.Unlock()
	return secs
}

func keepLast[T any](xs []T) []T {
	if len(xs) > memoKeep {
		return xs[len(xs)-memoKeep:]
	}
	return xs
}

// streamOf is the workstream of issue id at root, nil when there is none.
func (m model) streamOf(root string, id int) *world.Stream {
	return m.world().Streams[world.Key{Root: root, ID: id}]
}

// sessionsOn counts the sessions working on issue i of the project at
// root, as the cockpit's issues card and the task picker show them.
func (m model) sessionsOn(root string) func(issues.Issue) int {
	w := m.world()
	return func(i issues.Issue) int {
		if st := w.Streams[world.Key{Root: root, ID: i.ID}]; st != nil {
			return len(st.Sessions)
		}
		return 0
	}
}
