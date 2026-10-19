package fleet

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Projects returns the project directories pinned to the list, in the order
// they were added. A missing file means none are pinned.
func (h Home) Projects() []string {
	b, err := os.ReadFile(h.projectsFile())
	if err != nil {
		return nil
	}
	var dirs []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !slices.Contains(dirs, line) {
			dirs = append(dirs, line)
		}
	}
	return dirs
}

// PinProject adds dir, cleaned and made absolute, unless it is already pinned.
func (h Home) PinProject(dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dirs := h.Projects()
	if slices.Contains(dirs, dir) {
		return nil
	}
	return h.saveProjects(append(dirs, dir))
}

// UnpinProject removes dir. A directory that is not pinned is not an error.
func (h Home) UnpinProject(dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	dirs := h.Projects()
	i := slices.Index(dirs, dir)
	if i < 0 {
		return nil
	}
	return h.saveProjects(slices.Delete(dirs, i, i+1))
}

// saveProjects replaces the file with a rename, like saveSession, so the
// view never reads half a list.
func (h Home) saveProjects(dirs []string) error {
	if err := os.MkdirAll(h.dataDir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(h.dataDir(), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(strings.Join(dirs, "\n") + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), h.projectsFile()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
