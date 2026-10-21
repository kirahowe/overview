// Package fleet reads and records the state of the Claude Code sessions that
// belong to one config directory.
package fleet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Home is one Claude Code config directory, such as ~/.claude-work, and the
// claude that runs against it. Everything overview reads or writes sits under
// a single Home, so sessions from another config directory never show up
// beside it.
type Home struct {
	Dir    string // the config directory, absolute
	Claude string // the claude binary to run
	// OrchestratorDir is the directory the orchestrator session runs in,
	// DefaultOrchestratorDir unless a flag says otherwise.
	OrchestratorDir string
	// User is the user's home directory, which short directory names in
	// a digest are relative to.
	User string
	// Exe is the overview binary, and ConfigDir overview's config
	// directory when it is not the default, "" when it is. The
	// orchestrator runs overview with them.
	Exe, ConfigDir string
}

// ResolveHome finds the config directory that spec names, as --home or the
// config file's home line gives it. A bare name such as "work" means
// ~/.claude-work, and ~ is user, the user's home directory. There is no
// default: someone with several config directories would see an empty
// fleet from a guess, with nothing saying why.
func ResolveHome(spec, user string) (string, error) {
	dir := spec
	if dir == "" {
		return "", errors.New("no config directory: pass --home <name|path> (--home ~/.claude for the default), or put a home = <name|path> line in overview's config file")
	}
	switch {
	case dir == "~" || strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(user, dir[1:])
	case !strings.ContainsRune(dir, filepath.Separator) && !strings.HasPrefix(dir, "."):
		dir = filepath.Join(user, ".claude-"+dir)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("config directory %s does not exist", dir)
	}
	return dir, nil
}

// Name is the short name shown in the header: "work" for ~/.claude-work.
func (h Home) Name() string {
	return strings.TrimPrefix(filepath.Base(h.Dir), ".claude-")
}

func (h Home) dataDir() string      { return filepath.Join(h.Dir, "overview") }
func (h Home) sessionsDir() string  { return filepath.Join(h.dataDir(), "sessions") }
func (h Home) digestsDir() string   { return filepath.Join(h.dataDir(), "digests") }
func (h Home) pendingFile() string  { return filepath.Join(h.dataDir(), "digest-pending") }
func (h Home) cursorFile() string   { return filepath.Join(h.dataDir(), "digest-cursor") }
func (h Home) projectsFile() string { return filepath.Join(h.dataDir(), "projects") }

// ReviewsDir holds the review of each session that has one, a log named
// after the session.
func (h Home) ReviewsDir() string { return filepath.Join(h.dataDir(), "reviews") }
