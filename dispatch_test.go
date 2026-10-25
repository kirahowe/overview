package main

import (
	"reflect"
	"testing"
)

func TestParseCLI(t *testing.T) {
	tests := []struct {
		name string
		args []string
		cmd  string
		rest []string
		err  bool
		help bool
	}{
		{"no args launches", nil, "", nil, false, false},
		{"flags only launch", []string{"--home", "x"}, "", nil, false, false},
		{"command first", []string{"sessions", "--home", "x"}, "sessions", nil, false, false},
		{"command after flags", []string{"--home", "x", "sessions"}, "sessions", nil, false, false},
		{"command between flags", []string{"--home", "x", "digest", "--model", "m"}, "digest", nil, false, false},
		{"activity after flags", []string{"--home=x", "activity"}, "activity", nil, false, false},
		{"orchestrator after flags", []string{"--home", "x", "orchestrator", "start"}, "orchestrator", []string{"start"}, false, false},
		{"orchestrator action then flags", []string{"orchestrator", "start", "--every", "2h"}, "orchestrator", []string{"start"}, false, false},
		{"publish file", []string{"--home", "x", "publish", "f.md"}, "publish", []string{"f.md"}, false, false},
		{"themes name", []string{"themes", "one-dark"}, "themes", []string{"one-dark"}, false, false},
		{"typo after flags", []string{"--home", "x", "sesions"}, "", nil, true, false},
		{"typo first", []string{"sesions"}, "sesions", nil, true, false},
		{"stray word on launch", []string{"foo", "--home", "x"}, "foo", nil, true, false},
		{"extra arg", []string{"sessions", "extra"}, "", nil, true, false},
		{"extra arg after flags", []string{"--home", "x", "sessions", "extra"}, "", nil, true, false},
		{"extra publish arg", []string{"publish", "a", "b"}, "", nil, true, false},
		{"double dash then command", []string{"--", "sessions"}, "sessions", nil, false, false},
		{"double dash then typo", []string{"--", "sesions"}, "", nil, true, false},
		{"double dash then flag-like word", []string{"--", "--home"}, "", nil, true, false},
		{"flag then double dash then typo", []string{"--home", "x", "--", "sesions"}, "", nil, true, false},
		{"unknown flag", []string{"--bogus"}, "", nil, true, false},
		{"help flag", []string{"-h"}, "", nil, true, true},
		{"long help flag", []string{"--help"}, "", nil, true, true},
		{"help after command", []string{"sessions", "-h"}, "", nil, true, true},
		{"hook first", []string{"hook"}, "hook", []string{}, false, false},
		{"review keeps its flags", []string{"review", "view", "--store", "s"}, "review", []string{"view", "--store", "s"}, false, false},
		{"hook after flags", []string{"--home", "x", "hook"}, "", nil, true, false},
		{"review after flags", []string{"--home", "x", "review"}, "", nil, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := parseCLI(tc.args)
			if (err != nil) != tc.err {
				t.Fatalf("err = %v, want error %v", err, tc.err)
			}
			if isHelp(err) != tc.help {
				t.Fatalf("isHelp = %v, want %v", isHelp(err), tc.help)
			}
			if tc.err {
				return
			}
			if c.cmd != tc.cmd || (len(c.rest) > 0 || len(tc.rest) > 0) && !reflect.DeepEqual(c.rest, tc.rest) {
				t.Fatalf("got %q %q, want %q %q", c.cmd, c.rest, tc.cmd, tc.rest)
			}
		})
	}
}
