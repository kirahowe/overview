package tui

import (
	"bufio"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DefaultTheme is the theme used when nothing chooses another.
const DefaultTheme = "one-dark"

//go:embed themes/*.theme
var builtinThemes embed.FS

// Theme is the view's palette: one colour for each role the view draws.
type Theme struct {
	Name string

	Accent, Border, Text, Muted lipgloss.Color
	CursorFg, CursorBg          lipgloss.Color
	CursorDimFg, CursorDimBg    lipgloss.Color
	StatusFg, StatusBg, PillFg  lipgloss.Color
	Blocked, Asking, Working    lipgloss.Color
	Code                        lipgloss.Color
}

// themeKeys names each colour in a theme file, in the order Format writes
// them.
var themeKeys = []struct {
	key string
	get func(*Theme) *lipgloss.Color
}{
	{"accent", func(t *Theme) *lipgloss.Color { return &t.Accent }},
	{"border", func(t *Theme) *lipgloss.Color { return &t.Border }},
	{"text", func(t *Theme) *lipgloss.Color { return &t.Text }},
	{"muted", func(t *Theme) *lipgloss.Color { return &t.Muted }},
	{"cursor-fg", func(t *Theme) *lipgloss.Color { return &t.CursorFg }},
	{"cursor-bg", func(t *Theme) *lipgloss.Color { return &t.CursorBg }},
	{"cursor-dim-fg", func(t *Theme) *lipgloss.Color { return &t.CursorDimFg }},
	{"cursor-dim-bg", func(t *Theme) *lipgloss.Color { return &t.CursorDimBg }},
	{"status-fg", func(t *Theme) *lipgloss.Color { return &t.StatusFg }},
	{"status-bg", func(t *Theme) *lipgloss.Color { return &t.StatusBg }},
	{"pill-fg", func(t *Theme) *lipgloss.Color { return &t.PillFg }},
	{"blocked", func(t *Theme) *lipgloss.Color { return &t.Blocked }},
	{"asking", func(t *Theme) *lipgloss.Color { return &t.Asking }},
	{"working", func(t *Theme) *lipgloss.Color { return &t.Working }},
	{"code", func(t *Theme) *lipgloss.Color { return &t.Code }},
}

var hexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// validColor accepts #rgb, #rrggbb, or an ANSI 256-colour index.
func validColor(s string) bool {
	if hexColor.MatchString(s) {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 0 && n <= 255
}

// parseSettings reads "key = value" lines. Blank lines and lines starting
// with # are comments; a value may itself start with #, so comments never
// trail a value.
func parseSettings(src string) (map[string]string, error) {
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

// ParseTheme reads a theme file. Its colours go over base, so a file only
// needs the colours it changes. A "base = <name>" line picks a built-in
// theme to start from instead of base.
func ParseTheme(name, src string, base Theme) (Theme, error) {
	kv, err := parseSettings(src)
	if err != nil {
		return Theme{}, fmt.Errorf("theme %s: %w", name, err)
	}
	t := base
	if b, ok := kv["base"]; ok {
		if t, err = builtinTheme(b); err != nil {
			return Theme{}, fmt.Errorf("theme %s: base: %w", name, err)
		}
		delete(kv, "base")
	}
	t.Name = name
	for _, k := range themeKeys {
		v, ok := kv[k.key]
		if !ok {
			continue
		}
		if !validColor(v) {
			return Theme{}, fmt.Errorf("theme %s: %s: %q is not #rgb, #rrggbb or 0-255", name, k.key, v)
		}
		*k.get(&t) = lipgloss.Color(v)
		delete(kv, k.key)
	}
	for k := range kv {
		return Theme{}, fmt.Errorf("theme %s: unknown key %q", name, k)
	}
	return t, nil
}

// Format writes every colour of t as a theme file, which is a starting point
// for a theme of one's own.
func (t Theme) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", t.Name)
	for _, k := range themeKeys {
		fmt.Fprintf(&b, "%-13s = %s\n", k.key, *k.get(&t))
	}
	return b.String()
}

// builtinTheme reads a theme shipped in the binary. Built-ins set every
// colour, so they start from nothing.
func builtinTheme(name string) (Theme, error) {
	src, err := builtinThemes.ReadFile("themes/" + name + ".theme")
	if err != nil {
		return Theme{}, fmt.Errorf("no built-in theme %q (have %s)", name, strings.Join(builtinNames(), ", "))
	}
	return ParseTheme(name, string(src), Theme{})
}

func builtinNames() []string {
	entries, _ := fs.ReadDir(builtinThemes, "themes")
	var names []string
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".theme"))
	}
	return names
}

// ConfigDir is where overview's own settings and themes live:
// $XDG_CONFIG_HOME/overview, or ~/.config/overview.
func ConfigDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "overview"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "overview"), nil
}

// ChooseTheme names the theme to use: the flag, then OVERVIEW_THEME, then
// the theme line of the config file, then the default.
func ChooseTheme(flag, configDir string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := os.Getenv("OVERVIEW_THEME"); env != "" {
		return env, nil
	}
	src, err := os.ReadFile(filepath.Join(configDir, "config"))
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultTheme, nil
	} else if err != nil {
		return "", err
	}
	kv, err := parseSettings(string(src))
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(configDir, "config"), err)
	}
	for k := range kv {
		if k != "theme" {
			return "", fmt.Errorf("%s: unknown key %q", filepath.Join(configDir, "config"), k)
		}
	}
	if kv["theme"] != "" {
		return kv["theme"], nil
	}
	return DefaultTheme, nil
}

// LoadTheme finds a theme by name or path. A name is looked up in the
// config directory's themes folder first, so a file there can replace a
// built-in. Colours a file leaves out come from the default theme.
func LoadTheme(name, configDir string) (Theme, error) {
	path := ""
	switch {
	case strings.ContainsRune(name, filepath.Separator):
		path = name
	default:
		user := filepath.Join(configDir, "themes", name+".theme")
		if _, err := os.Stat(user); err == nil {
			path = user
		}
	}
	if path == "" {
		return builtinTheme(name)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return Theme{}, err
	}
	base, err := builtinTheme(DefaultTheme)
	if err != nil {
		return Theme{}, err
	}
	return ParseTheme(strings.TrimSuffix(filepath.Base(path), ".theme"), string(src), base)
}

// ThemeInfo is one theme LoadTheme can find by name. Path is empty for a
// built-in that no file replaces.
type ThemeInfo struct {
	Name, Path string
}

// Themes lists every theme LoadTheme can find by name, sorted.
func Themes(configDir string) []ThemeInfo {
	byName := map[string]string{}
	for _, n := range builtinNames() {
		byName[n] = ""
	}
	files, _ := filepath.Glob(filepath.Join(configDir, "themes", "*.theme"))
	for _, f := range files {
		byName[strings.TrimSuffix(filepath.Base(f), ".theme")] = f
	}
	var out []ThemeInfo
	for n, p := range byName {
		out = append(out, ThemeInfo{n, p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
