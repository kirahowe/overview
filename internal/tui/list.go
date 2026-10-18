package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"overview/internal/fleet"
)

// needsYouTitle heads the pinned section and awayTitle the last one. Project
// sections are headed by the project's display name.
const (
	needsYouTitle = "needs you"
	awayTitle     = "in other apps"
)

// section is one block of the list: the pinned "needs you" block, the
// sessions of one project, or the sessions other apps run.
type section struct {
	Title    string
	NeedsYou bool
	Away     bool
	Rows     []fleet.Row
}

// buildList is the single source of truth for the session list. The
// renderer, the cursor, n/N and the status bar all read it.
//
// It drops the orchestrator and every row the query does not match, pins the
// rows that need you in a first section (longest wait first), and puts the
// rest into one section per project. Projects are ordered by display name.
// A session that runs inside another app, as host reports, is not one the
// user drives from a project, so unless it needs them it goes in a last
// section of its own instead. Within a project or that section, working
// sessions come before the rest, then the newest activity first, then the
// session id. The result depends only on the set of rows, never on their
// order.
func buildList(rows []fleet.Row, query string, project func(cwd string) string, host func(fleet.Row) (string, bool)) []section {
	terms := strings.Fields(strings.ToLower(query))
	var needs, away []fleet.Row
	byProject := map[string][]fleet.Row{}
	for _, r := range rows {
		if r.Name == fleet.OrchestratorName {
			continue
		}
		p := project(r.Cwd)
		app, isAway := host(r)
		if !matches(r, p, app, terms) {
			continue
		}
		switch {
		case r.NeedsYou():
			needs = append(needs, r)
		case isAway:
			away = append(away, r)
		default:
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
		sortActive(rs)
		out = append(out, section{Title: p, Rows: rs})
	}
	if len(away) > 0 {
		sortActive(away)
		out = append(out, section{Title: awayTitle, Away: true, Rows: away})
	}
	return out
}

// sortActive puts working sessions first, then the newest activity, then
// orders by session id.
func sortActive(rs []fleet.Row) {
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
// is matched against one field at a time, so it can never straddle two. app
// is the host app's name, "" for a session in a terminal.
func matches(r fleet.Row, project, app string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	fields := [...]string{project, r.Title, r.LastPrompt, r.Name, r.Label, app}
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
// root lookup sits behind exists and resolve so tests need no filesystem, and
// its answer is cached per directory.
type projects struct {
	exists  func(path string) bool
	resolve func(path string) string // symlinks resolved, so /tmp and /private/tmp agree
	roots   map[string]string
	home    string // the resolved home directory, "" if unknown
}

func newProjects() *projects {
	p := &projects{
		exists:  func(p string) bool { _, err := os.Lstat(p); return err == nil },
		resolve: resolvePath,
		roots:   map[string]string{},
	}
	if user, err := os.UserHomeDir(); err == nil {
		p.home = p.resolve(user)
	}
	return p
}

// resolvePath resolves symlinks in p, or only cleans it if that fails (the
// directory may be gone by now).
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// root is the directory of the project cwd belongs to, "" for no cwd.
func (p *projects) root(cwd string) string {
	root, ok := p.roots[cwd]
	if !ok {
		if cwd != "" {
			root = repoRoot(p.resolve(cwd), p.exists)
		}
		p.roots[cwd] = root
	}
	return root
}

// name is the display name of the project cwd belongs to.
func (p *projects) name(cwd string) string {
	root := p.root(cwd)
	if p.home != "" && root == p.home {
		return "~"
	}
	return fleet.ShortDir(root)
}

// repoRoot returns the nearest ancestor of cwd (cwd included) that holds a
// .jj or .git entry, file or directory. Without one it returns cwd. Either
// way the result is clean.
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
			return filepath.Clean(cwd)
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
	return counts(buildList(m.rows, "", m.projects.name, m.hostOf))
}
