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

// Home is one Claude Code config directory, such as ~/.claude-kira. Everything
// overview reads or writes sits under a single Home, so sessions from another
// config directory never show up beside it.
type Home string

// ResolveHome picks the config directory: the flag when given, otherwise
// CLAUDE_CONFIG_DIR. A bare name such as "kira" means ~/.claude-kira. There is
// no default, because bare ~/.claude is not in use on this machine and
// guessing would show an empty fleet.
func ResolveHome(flag string) (Home, error) {
	dir := flag
	if dir == "" {
		dir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if dir == "" {
		return "", errors.New("no config directory: pass --home <name|path> or set CLAUDE_CONFIG_DIR")
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch {
	case dir == "~" || strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(user, dir[1:])
	case !strings.ContainsRune(dir, filepath.Separator) && !strings.HasPrefix(dir, "."):
		dir = filepath.Join(user, ".claude-"+dir)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("config directory %s does not exist", dir)
	}
	return Home(dir), nil
}

// Name is the short name shown in the header: "kira" for ~/.claude-kira.
func (h Home) Name() string {
	return strings.TrimPrefix(filepath.Base(string(h)), ".claude-")
}

func (h Home) dataDir() string     { return filepath.Join(string(h), "overview") }
func (h Home) sessionsDir() string { return filepath.Join(h.dataDir(), "sessions") }
func (h Home) digestsDir() string  { return filepath.Join(h.dataDir(), "digests") }
func (h Home) pendingFile() string { return filepath.Join(h.dataDir(), "digest-pending") }
func (h Home) cursorFile() string  { return filepath.Join(h.dataDir(), "digest-cursor") }
