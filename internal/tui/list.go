package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"overview/internal/fleet"
)

// needsYouTitle heads the pinned section. Project sections are headed by the
// project's display name.
const needsYouTitle = "needs you"

// section is one block of the list: either the pinned "needs you" block or
// the sessions of one project.
type section struct {
	Title    string
	NeedsYou bool
	Rows     []fleet.Row
}

// buildList is the single source of truth for the session list. The
// renderer, the cursor, n/N and the status bar all read it.
//
// It drops the orchestrator and every row the query does not match, pins the
// rows that need you in a first section (longest wait first), and puts the
// rest into one section per project. Projects are ordered by display name.
// Within a project, working sessions come before the rest, then the newest
// activity first, then the session id. The result depends only on the set of
// rows, never on their order.
func buildList(rows []fleet.Row, query string, project func(cwd string) string) []section {
	terms := strings.Fields(strings.ToLower(query))
	var needs []fleet.Row
	byProject := map[string][]fleet.Row{}
	for _, r := range rows {
		if r.Name == fleet.OrchestratorName {
			continue
		}
		p := project(r.Cwd)
		if !matches(r, p, terms) {
			continue
		}
		if r.NeedsYou() {
			needs = append(needs, r)
		} else {
			byProject[p] = append(byProject[p], r)
		}
	}

	var out []section
	if len(needs) > 0 {
		sort.Slice(needs, func(i, j int) bool {
			if !needs[i].Since.Equal(needs[j].Since) {
				return needs[i].Since.Before(needs[j].Since)
			}
			return needs[i].SessionID < needs[j].SessionID
		})
		out = append(out, section{Title: needsYouTitle, NeedsYou: true, Rows: needs})
	}
	names := make([]string, 0, len(byProject))
	for p := range byProject {
		names = append(names, p)
	}
	sort.Slice(names, func(i, j int) bool { return nameLess(names[i], names[j]) })
	for _, p := range names {
		rs := byProject[p]
		sort.Slice(rs, func(i, j int) bool {
			wi, wj := rs[i].State == fleet.Working, rs[j].State == fleet.Working
			if wi != wj {
				return wi
			}
			if !rs[i].Since.Equal(rs[j].Since) {
				return rs[i].Since.After(rs[j].Since)
			}
			return rs[i].SessionID < rs[j].SessionID
		})
		out = append(out, section{Title: p, Rows: rs})
	}
	return out
}

// nameLess orders project names alphabetically, ignoring case, with the
// exact spelling as the tiebreak so the order is total.
func nameLess(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la != lb {
		return la < lb
	}
	return a < b
}

// matches reports whether every term is found in at least one field. A term
// is matched against one field at a time, so it can never straddle two.
func matches(r fleet.Row, project string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	fields := [...]string{project, r.Title, r.LastPrompt, r.Name, r.Label}
	lower := make([]string, len(fields))
	for i, f := range fields {
		lower[i] = strings.ToLower(f)
	}
	for _, t := range terms {
		found := false
		for _, f := range lower {
			if strings.Contains(f, t) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// flatten is the list as the cursor walks it.
func flatten(secs []section) []fleet.Row {
	var out []fleet.Row
	for _, s := range secs {
		out = append(out, s.Rows...)
	}
	return out
}

// projects maps a working directory to its project's display name. The repo
// root lookup sits behind exists so tests need no filesystem, and its answer
// is cached per directory.
type projects struct {
	exists func(path string) bool
	roots  map[string]string
}

func newProjects() *projects {
	return &projects{
		exists: func(p string) bool { _, err := os.Lstat(p); return err == nil },
		roots:  map[string]string{},
	}
}

// name is the display name of the project cwd belongs to.
func (p *projects) name(cwd string) string {
	root, ok := p.roots[cwd]
	if !ok {
		root = repoRoot(cwd, p.exists)
		p.roots[cwd] = root
	}
	if user, _ := os.UserHomeDir(); root == user {
		return "~"
	}
	return fleet.ShortDir(root)
}

// repoRoot returns the nearest ancestor of cwd (cwd included) that holds a
// .jj or .git entry, file or directory. Without one it returns cwd.
func repoRoot(cwd string, exists func(path string) bool) string {
	if cwd == "" {
		return ""
	}
	for dir := filepath.Clean(cwd); ; {
		if exists(filepath.Join(dir, ".jj")) || exists(filepath.Join(dir, ".git")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

// counts tallies the whole fleet, filter ignored, for the status bar.
func counts(secs []section) (need, working, idle int) {
	for _, s := range secs {
		for _, r := range s.Rows {
			switch {
			case s.NeedsYou:
				need++
			case r.State == fleet.Working:
				working++
			default:
				idle++
			}
		}
	}
	return
}

func (m model) counts() (need, working, idle int) {
	return counts(buildList(m.rows, "", m.projects.name))
}
