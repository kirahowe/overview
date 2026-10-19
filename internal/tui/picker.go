package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// The add-project picker takes over the list pane while it is open: a line
// to type into, and the directories that match. With plain words it offers
// the folders beside every project the view knows, which is where a new
// project usually is. A query that starts with / or ~ is a path instead,
// and offers what is in its directory. The directories are read in Cmds,
// and everything else here is a pure function of what they returned.

// picker is the picker's state. The zero value is a closed picker.
type picker struct {
	open  bool
	query string
	sel   int
	// from names what paths answers: "" for the folders beside the known
	// projects, else the directory a path query is in. An answer read for
	// another source is stale and dropped.
	from  string
	paths []string
	read  bool // paths holds the answer for from
}

// pickedMsg answers a read of the directories for source.
type pickedMsg struct {
	source string
	paths  []string
}

// pinnedMsg answers a pin with the project root that was pinned.
type pinnedMsg struct {
	root string
	err  error
}

// readDirs lists the directories in dir by name, hidden ones and symlinks
// to directories included.
func readDirs(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		isDir := e.IsDir()
		if e.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
				isDir = info.IsDir()
			}
		}
		if isDir {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// isPathQuery says whether a query is a path rather than words.
func isPathQuery(q string) bool { return strings.HasPrefix(q, "/") || strings.HasPrefix(q, "~") }

// expandPath turns a path query into a path: a leading ~ or ~/ is home.
func expandPath(q, home string) string {
	switch {
	case q == "~":
		return home
	case strings.HasPrefix(q, "~/"):
		return home + q[1:]
	}
	return q
}

// splitPathQuery is the directory a path query is in and the start of the
// name typed after it: "~/code/ov" is (home+"/code", "ov").
func splitPathQuery(q, home string) (dir, prefix string) {
	p := expandPath(q, home)
	if q == "~" {
		return home, ""
	}
	i := strings.LastIndexByte(p, '/')
	dir, prefix = p[:i], p[i+1:]
	if dir == "" {
		dir = "/"
	}
	return filepath.Clean(dir), prefix
}

// pickSource is what a query needs read: "" for the folders beside the
// known projects, or a path query's directory.
func pickSource(q, home string) string {
	if !isPathQuery(q) {
		return ""
	}
	dir, _ := splitPathQuery(q, home)
	return dir
}

// pickParents are the directories that hold the known project roots, each
// once and in order.
func pickParents(roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range roots {
		if r == "" || r == "/" {
			continue
		}
		if p := filepath.Dir(r); !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// pickMatches is what the picker offers for query, out of paths, the
// directories read for it. Words keep the paths whose short form holds
// every word, ignoring case, leave out hidden ones and the projects shown
// already, and sort by the short form. A path query keeps the paths named
// with what was typed after its last slash, ignoring case, hidden ones only
// when that starts with a dot, and sorts by name.
func pickMatches(query, home string, paths []string, shown map[string]bool, short func(string) string) []string {
	keep, by := func(string) bool { return false }, short
	if isPathQuery(query) {
		_, prefix := splitPathQuery(query, home)
		lp := strings.ToLower(prefix)
		keep = func(p string) bool {
			name := filepath.Base(p)
			return strings.HasPrefix(strings.ToLower(name), lp) && (!strings.HasPrefix(name, ".") || strings.HasPrefix(prefix, "."))
		}
		by = filepath.Base
	} else {
		terms := strings.Fields(strings.ToLower(query))
		keep = func(p string) bool {
			return !shown[p] && !strings.HasPrefix(filepath.Base(p), ".") && matchFields(terms, short(p))
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		if !seen[p] && keep(p) {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := by(out[i]), by(out[j])
		if a != b {
			return nameLess(a, b)
		}
		return out[i] < out[j]
	})
	return out
}

// pickComplete is the query tab turns query into, with match selected: the
// match and a slash, written under ~ when the query was.
func pickComplete(query, home, match string) string {
	if strings.HasPrefix(query, "~") && home != "" && (match == home || strings.HasPrefix(match, home+"/")) {
		return "~" + strings.TrimPrefix(match, home) + "/"
	}
	return strings.TrimSuffix(match, "/") + "/"
}

// pickTarget is what enter pins: the selected match, or else, for a path
// query, the path typed, which the Cmd pins only if it is a directory.
func pickTarget(query, home string, matches []string, sel int) (path string, typed bool) {
	if sel >= 0 && sel < len(matches) {
		return matches[sel], false
	}
	if isPathQuery(query) {
		return filepath.Clean(expandPath(query, home)), true
	}
	return "", false
}

// readPathsCmd reads the directories in each of dirs with read, and
// answers for source. A directory that cannot be read adds nothing.
func readPathsCmd(read func(string) ([]string, error), source string, dirs []string) tea.Cmd {
	return func() tea.Msg {
		var paths []string
		for _, d := range dirs {
			names, _ := read(d)
			for _, n := range names {
				paths = append(paths, filepath.Join(d, n))
			}
		}
		return pickedMsg{source, paths}
	}
}

// pinCmd pins the project path is in, after checking that a typed path is
// a directory. The project is the repo root, as for a session's directory.
func pinCmd(read func(string) ([]string, error), pin func(string) error, path string, typed bool, resolve func(string) string, exists func(string) bool) tea.Cmd {
	return func() tea.Msg {
		if typed {
			if _, err := read(path); err != nil {
				return pinnedMsg{err: err}
			}
		}
		root := repoRoot(resolve(path), exists)
		return pinnedMsg{root, pin(root)}
	}
}
