package tui

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"

	"overview/internal/config"
)

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
	// The review's diff: the markers and line numbers of added and
	// removed lines, their backgrounds, and the stronger backgrounds of
	// the words that changed within them.
	Added, Removed             lipgloss.Color
	AddedBg, RemovedBg         lipgloss.Color
	AddedWordBg, RemovedWordBg lipgloss.Color
	// Syntax is the chroma style that colours code in a review.
	Syntax string
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
	{"added", func(t *Theme) *lipgloss.Color { return &t.Added }},
	{"removed", func(t *Theme) *lipgloss.Color { return &t.Removed }},
	{"added-bg", func(t *Theme) *lipgloss.Color { return &t.AddedBg }},
	{"removed-bg", func(t *Theme) *lipgloss.Color { return &t.RemovedBg }},
	{"added-word-bg", func(t *Theme) *lipgloss.Color { return &t.AddedWordBg }},
	{"removed-word-bg", func(t *Theme) *lipgloss.Color { return &t.RemovedWordBg }},
}

// syntaxKey names the theme's chroma style, the one key that is not a
// colour.
const syntaxKey = "syntax"

var hexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// validColor accepts #rgb, #rrggbb, or an ANSI 256-colour index.
func validColor(s string) bool {
	if hexColor.MatchString(s) {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 0 && n <= 255
}

// ParseTheme reads a theme file. Its colours go over base, so a file only
// needs the colours it changes. A "base = <name>" line picks a built-in
// theme to start from instead of base.
func ParseTheme(name, src string, base Theme) (Theme, error) {
	kv, err := config.Settings(src)
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
	if v, ok := kv[syntaxKey]; ok {
		if styles.Registry[v] == nil {
			return Theme{}, fmt.Errorf("theme %s: syntax: no chroma style %q", name, v)
		}
		t.Syntax = v
		delete(kv, syntaxKey)
	}
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
		fmt.Fprintf(&b, "%-15s = %s\n", k.key, *k.get(&t))
	}
	fmt.Fprintf(&b, "%-15s = %s\n", syntaxKey, t.Syntax)
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
	base, err := builtinTheme(config.DefaultTheme)
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
