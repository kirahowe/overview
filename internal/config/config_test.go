package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse(t *testing.T) {
	got, err := Parse("# mine\n\ntheme = revdiff\nhome = ~/.claude-work\nsocket = ov2\nclaude = /opt/claude\n")
	want := Values{Home: "~/.claude-work", Theme: "revdiff", Socket: "ov2", Claude: "/opt/claude"}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
	for name, src := range map[string]string{
		"misspelt key": "them = one-dark",
		"no equals":    "theme one-dark",
		"empty value":  "theme =",
		"orchestrator": "orchestrator-dir = /x",
	} {
		if _, err := Parse(src); err == nil {
			t.Errorf("%s: no error for %q", name, src)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	for _, c := range []struct {
		name        string
		flags, file Values
		want        Values
	}{
		{"defaults", Values{}, Values{}, Values{Theme: DefaultTheme, Socket: DefaultSocket, Claude: DefaultClaude}},
		{"file", Values{}, Values{Home: "work", Theme: "revdiff", Socket: "s", Claude: "c"},
			Values{Home: "work", Theme: "revdiff", Socket: "s", Claude: "c"}},
		{"flags beat the file", Values{Home: "kira", Theme: "one-light", Socket: "f", Claude: "fc"},
			Values{Home: "work", Theme: "revdiff", Socket: "s", Claude: "c"},
			Values{Home: "kira", Theme: "one-light", Socket: "f", Claude: "fc"}},
		{"mixed", Values{Theme: "one-light"}, Values{Home: "work"},
			Values{Home: "work", Theme: "one-light", Socket: DefaultSocket, Claude: DefaultClaude}},
	} {
		got, err := Resolve("/d", c.flags, c.file, "/u", "/w")
		if err != nil || got.Values != c.want || got.Dir != "/d" {
			t.Errorf("%s: got %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
	if _, err := Resolve("/d", Values{Socket: "/tmp/sock"}, Values{}, "/u", "/w"); err == nil {
		t.Error("a socket path should be refused")
	}
}

// A theme or claude given as a path is made absolute: ~/ from the user's
// home, a flag's relative path from where overview runs, and the file's
// from the config directory. A name or an absolute path stays as it is.
func TestResolveMakesPathsAbsolute(t *testing.T) {
	for _, c := range []struct {
		flags, file   Values
		theme, claude string
	}{
		{Values{Theme: "~/t.theme", Claude: "bin/claude"}, Values{}, "/u/t.theme", "/w/bin/claude"},
		{Values{}, Values{Theme: "themes/x.theme", Claude: "~/bin/claude"}, "/d/themes/x.theme", "/u/bin/claude"},
		{Values{Theme: "revdiff", Claude: "/opt/claude"}, Values{Theme: "x/y"}, "revdiff", "/opt/claude"},
		{Values{}, Values{}, DefaultTheme, DefaultClaude},
	} {
		got, err := Resolve("/d", c.flags, c.file, "/u", "/w")
		if err != nil || got.Theme != c.theme || got.Claude != c.claude {
			t.Errorf("%+v over %+v: theme %q, claude %q, %v; want %q, %q", c.flags, c.file, got.Theme, got.Claude, err, c.theme, c.claude)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir, Values{}, "/u", "/w")
	if err != nil || c.Theme != DefaultTheme || c.Dir != dir {
		t.Fatalf("no file: %+v, %v", c, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("theme = revdiff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(dir, Values{}, "/u", "/w"); err != nil || c.Theme != "revdiff" {
		t.Fatalf("file: %+v, %v", c, err)
	}
	if c, err = Load(dir, Values{Theme: "one-light"}, "/u", "/w"); err != nil || c.Theme != "one-light" {
		t.Fatalf("flag: %+v, %v", c, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte("them = revdiff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, Values{}, "/u", "/w"); err == nil {
		t.Fatal("a misspelt key should fail")
	}
}
