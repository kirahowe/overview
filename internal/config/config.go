// Package config settles overview's settings once, as a process starts:
// each comes from its flag, then from the config file, then from a default.
// The result is a value the caller passes down. Nothing here reads the
// environment, which every process shares and any of them can change.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultSocket names overview's tmux server, so it never shares one
	// with other tmux use.
	DefaultSocket = "overview"
	// DefaultTheme is the theme used when nothing chooses another.
	DefaultTheme = "one-dark"
	// DefaultClaude is the claude binary, found on PATH.
	DefaultClaude = "claude"
)

// Values is what one source says about each setting, "" where it says
// nothing.
type Values struct {
	Home   string // the Claude Code config directory, a name or a path
	Theme  string // a theme name or path
	Socket string // the name of overview's tmux socket
	Claude string // the claude binary
}

// Config is the settings a process runs with. Home stays "" when nothing
// names one, since overview never guesses it.
type Config struct {
	Dir  string // overview's own directory: the config file, themes, tmux.conf
	User string // the user's home directory
	Values
}

// keys maps each config file key to its setting.
var keys = map[string]func(*Values) *string{
	"home":   func(v *Values) *string { return &v.Home },
	"theme":  func(v *Values) *string { return &v.Theme },
	"socket": func(v *Values) *string { return &v.Socket },
	"claude": func(v *Values) *string { return &v.Claude },
}

// Settings reads "key = value" lines. Blank lines and lines starting with #
// are comments; a value may itself start with #, so comments never trail a
// value.
func Settings(src string) (map[string]string, error) {
	kv := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(src))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" {
			return nil, fmt.Errorf("line %d: want key = value, got %q", n, line)
		}
		kv[key] = value
	}
	return kv, sc.Err()
}

// Parse reads a config file. An unknown key is an error, so a typo does
// not silently leave a setting at its default.
func Parse(src string) (Values, error) {
	kv, err := Settings(src)
	if err != nil {
		return Values{}, err
	}
	var v Values
	for k, value := range kv {
		field, ok := keys[k]
		if !ok {
			return Values{}, fmt.Errorf("unknown key %q", k)
		}
		*field(&v) = value
	}
	return v, nil
}

// Resolve settles each setting: the flag, then the file, then the default.
func Resolve(dir string, flags, file Values) (Config, error) {
	pick := func(flag, file, def string) string {
		switch {
		case flag != "":
			return flag
		case file != "":
			return file
		}
		return def
	}
	c := Config{Dir: dir, Values: Values{
		Home:   pick(flags.Home, file.Home, ""),
		Theme:  pick(flags.Theme, file.Theme, DefaultTheme),
		Socket: pick(flags.Socket, file.Socket, DefaultSocket),
		Claude: pick(flags.Claude, file.Claude, DefaultClaude),
	}}
	// tmux -L takes a name; a path would put the socket somewhere else.
	if strings.ContainsRune(c.Socket, '/') {
		return Config{}, fmt.Errorf("socket %q: want a name, not a path", c.Socket)
	}
	return c, nil
}

// DefaultDir is overview's directory for the user whose home is userHome.
func DefaultDir(userHome string) string { return filepath.Join(userHome, ".config", "overview") }

// Load reads the config file in dir, or in the default directory of user,
// the user's home directory, when dir is "", and resolves it against the
// flags. A missing file says nothing.
func Load(dir string, flags Values, user string) (Config, error) {
	if dir == "" {
		dir = DefaultDir(user)
	}
	// Absolute, so it means the same to the processes this one starts.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, err
	}
	path := filepath.Join(dir, "config")
	var file Values
	switch src, err := os.ReadFile(path); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return Config{}, err
	default:
		if file, err = Parse(string(src)); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	c, err := Resolve(dir, flags, file)
	c.User = user
	return c, err
}
