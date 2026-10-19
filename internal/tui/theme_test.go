package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestBuiltinThemesSetEveryColour(t *testing.T) {
	for _, name := range builtinNames() {
		th, err := builtinTheme(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range themeKeys {
			if *k.get(&th) == "" {
				t.Errorf("%s leaves %s unset", name, k.key)
			}
		}
	}
	if len(builtinNames()) < 2 {
		t.Fatalf("built-ins: %v", builtinNames())
	}
}

func TestParseThemeErrors(t *testing.T) {
	base, _ := builtinTheme(DefaultTheme)
	for name, src := range map[string]string{
		"unknown key":  "accnet = #fff",
		"bad colour":   "accent = orange",
		"out of range": "accent = 256",
		"short hex":    "accent = #ff",
		"no equals":    "accent #fff",
		"empty value":  "accent =",
		"bad base":     "base = nope",
	} {
		if _, err := ParseTheme("x", src, base); err == nil {
			t.Errorf("%s: %q parsed", name, src)
		}
	}
}

func TestParseThemeOverridesOnlyWhatItSets(t *testing.T) {
	base, _ := builtinTheme(DefaultTheme)
	got, err := ParseTheme("mine", "# a comment\n\naccent = #123\ncode=42\n", base)
	if err != nil {
		t.Fatal(err)
	}
	want := base
	want.Name, want.Accent, want.Code = "mine", "#123", "42"
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseThemeBase(t *testing.T) {
	revdiff, _ := builtinTheme("revdiff")
	oneDark, _ := builtinTheme("one-dark")
	got, err := ParseTheme("mine", "base = one-dark\naccent = #c678dd", revdiff)
	if err != nil {
		t.Fatal(err)
	}
	want := oneDark
	want.Name, want.Accent = "mine", lipgloss.Color("#c678dd")
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestLoadTheme(t *testing.T) {
	dir := t.TempDir()
	themes := filepath.Join(dir, "themes")
	if err := os.MkdirAll(themes, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, src string) {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(themes, "one-dark.theme"), "base = one-dark\naccent = #ffffff")
	write(filepath.Join(themes, "calm.theme"), "accent = #00ff00")
	loose := filepath.Join(dir, "loose.theme")
	write(loose, "accent = #0000ff")

	revdiff, _ := builtinTheme("revdiff")
	for name, accent := range map[string]lipgloss.Color{
		"revdiff":  revdiff.Accent, // built in, nothing replaces it
		"one-dark": "#ffffff",      // the user's file replaces the built-in
		"calm":     "#00ff00",      // user only, the rest from the default
		loose:      "#0000ff",      // a path
	} {
		th, err := LoadTheme(name, dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if th.Accent != accent {
			t.Errorf("%s: accent %s, want %s", name, th.Accent, accent)
		}
	}
	def, _ := builtinTheme(DefaultTheme)
	if calm, _ := LoadTheme("calm", dir); calm.Border != def.Border {
		t.Errorf("calm should inherit the default's border, got %s", calm.Border)
	}
	if _, err := LoadTheme("missing", dir); err == nil || !strings.Contains(err.Error(), "one-dark") {
		t.Errorf("missing theme should list the built-ins: %v", err)
	}

	var names []string
	for _, th := range Themes(dir) {
		names = append(names, th.Name)
		builtin := th.Name == "one-light" || th.Name == "revdiff"
		if (th.Path != "") == builtin {
			t.Errorf("%s: path %q", th.Name, th.Path)
		}
	}
	if strings.Join(names, " ") != "calm one-dark one-light revdiff" {
		t.Errorf("themes: %v", names)
	}
}
