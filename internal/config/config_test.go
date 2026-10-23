package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	got, err := Parse("# mine\n\ntheme = revdiff\nhome = ~/.claude-work\nsocket = ov2\nclaude = /opt/claude\nissues = ~/bin/issues\n")
	want := Values{Home: "~/.claude-work", Theme: "revdiff", Socket: "ov2", Claude: "/opt/claude", Issues: "~/bin/issues"}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
	for name, src := range map[string]string{
		"misspelt key": "them = one-dark",
		"no equals":    "theme one-dark",
		"empty value":  "theme =",
		"orchestrator": "orchestrator-dir = /x",
		"issue":        "issue = /x",
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
		{"defaults", Values{}, Values{}, Values{Theme: DefaultTheme, Socket: DefaultSocket, Claude: DefaultClaude, Issues: DefaultIssues}},
		{"file", Values{}, Values{Home: "work", Theme: "revdiff", Socket: "s", Claude: "c", Issues: "i"},
			Values{Home: "work", Theme: "revdiff", Socket: "s", Claude: "c", Issues: "i"}},
		{"flags beat the file", Values{Home: "kira", Theme: "one-light", Socket: "f", Claude: "fc", Issues: "fi"},
			Values{Home: "work", Theme: "revdiff", Socket: "s", Claude: "c", Issues: "i"},
			Values{Home: "kira", Theme: "one-light", Socket: "f", Claude: "fc", Issues: "fi"}},
		{"mixed", Values{Theme: "one-light"}, Values{Home: "work"},
			Values{Home: "work", Theme: "one-light", Socket: DefaultSocket, Claude: DefaultClaude, Issues: DefaultIssues}},
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

// A claude named without a path is looked up once, and a path is kept.
func TestFindClaude(t *testing.T) {
	look := func(name string) (string, error) {
		if name == "claude" {
			return "/opt/bin/claude", nil
		}
		return "", errors.New(name + ": not found")
	}
	for in, want := range map[string]string{"claude": "/opt/bin/claude", "nope": "nope", "/x/claude": "/x/claude"} {
		c := Config{Dir: "/d", User: "/u", Values: Values{Claude: in}}
		if got := c.FindClaude(look).Claude; got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// An issues path is made absolute like claude's. A missing default is
// not an error: it resolves to "" and the feature is off. A missing one
// the user named is.
func TestIssuesPathAndFind(t *testing.T) {
	for _, c := range []struct {
		flags, file Values
		want        string
	}{
		{Values{Issues: "~/bin/issues"}, Values{}, "/u/bin/issues"},
		{Values{}, Values{Issues: "bin/issues"}, "/d/bin/issues"},
		{Values{Issues: "/x/issues"}, Values{Issues: "/y/issues"}, "/x/issues"},
		{Values{}, Values{}, DefaultIssues},
	} {
		got, err := Resolve("/d", c.flags, c.file, "/u", "/w")
		if err != nil || got.Issues != c.want {
			t.Errorf("%+v over %+v: %q, %v; want %q", c.flags, c.file, got.Issues, err, c.want)
		}
	}
	look := func(name string) (string, error) {
		if name == "issues" || name == "/x/issues" {
			return "/opt/bin/" + filepath.Base(name), nil
		}
		return "", errors.New(name + ": not found")
	}
	for in, want := range map[string]string{"issues": "/opt/bin/issues", "/x/issues": "/opt/bin/issues"} {
		c := Config{Dir: "/d", User: "/u", Values: Values{Issues: in}}
		if got, err := c.FindIssues(look); err != nil || got.Issues != want {
			t.Errorf("%q: %q, %v; want %q", in, got.Issues, err, want)
		}
	}
	// The default missing from PATH turns the feature off.
	if got, err := (Config{Values: Values{Issues: DefaultIssues}}).FindIssues(func(string) (string, error) {
		return "", errors.New("not found")
	}); err != nil || got.Issues != "" {
		t.Errorf("no issues on PATH: %q, %v", got.Issues, err)
	}
	// One the user named, missing or not executable, stops overview.
	for _, in := range []string{"nope", "/gone/issues"} {
		c := Config{Dir: "/d", User: "/u", Values: Values{Issues: in}}
		if _, err := c.FindIssues(look); err == nil || !strings.Contains(err.Error(), in) {
			t.Errorf("%q: err %v, want one naming it", in, err)
		}
	}
}

// A configured issues file that is not executable stops overview, as
// exec.LookPath sees it.
func TestFindIssuesRefusesAFileItCannotRun(t *testing.T) {
	p := filepath.Join(t.TempDir(), "issues")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Config{Values: Values{Issues: p}}
	if _, err := c.FindIssues(exec.LookPath); err == nil {
		t.Fatal("a file without the executable bit was taken")
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := c.FindIssues(exec.LookPath); err != nil || got.Issues != p {
		t.Fatalf("%q, %v", got.Issues, err)
	}
}
