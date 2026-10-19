package tmux

import (
	"fmt"
	"testing"

	"overview/internal/tmuxtest"
)

// Layouts captured from tmux 3.8 in a 200x50 window.
const (
	// Pane %0 on the left, %1 above %2 on the right.
	layoutThree = "21be,200x50,0,0{100x50,0,0,0,99x50,101,0[99x25,101,0,1,99x24,101,26,2]}"
	// Two columns beside a column that holds a pane and a split in a split.
	layoutNested = "b923,200x50,0,0{50x50,0,0,0,49x50,51,0,5,99x50,101,0[99x25,101,0,1,99x24,101,26{49x24,101,26,2,49x24,151,26[49x12,151,26,3,49x11,151,39,4]}]}"
)

// withChecksum builds a layout string from a body, so tests can write
// expected results without computing checksums by hand.
func withChecksum(body string) string { return fmt.Sprintf("%04x,%s", checksum(body), body) }

func TestChecksumMatchesTmux(t *testing.T) {
	for _, l := range []string{layoutThree, layoutNested} {
		if got := fmt.Sprintf("%04x", checksum(l[5:])); got != l[:4] {
			t.Errorf("checksum of %q = %s", l[5:], got)
		}
	}
}

func TestWithout(t *testing.T) {
	for _, tc := range []struct {
		name, layout, pane, want string
	}{
		{
			"left pane of two columns, right column takes the width",
			layoutThree, "%0",
			withChecksum("200x50,0,0[200x25,0,0,1,200x24,0,26,2]"),
		},
		{
			"top of a stack, the pane below takes the height",
			layoutThree, "%1",
			withChecksum("200x50,0,0{100x50,0,0,0,99x50,101,0,2}"),
		},
		{
			"leftmost of three columns, space split by size",
			layoutNested, "%0",
			// The columns were 49 and 99 wide and share the 51 freed cells
			// 1:2, the odd cell going to the first. The nested split
			// is rescaled to the wider column.
			withChecksum("200x50,0,0{66x50,0,0,5,133x50,67,0[133x25,67,0,1,133x24,67,26{66x24,67,26,2,66x24,134,26[66x12,134,26,3,66x11,134,39,4]}]}"),
		},
		{
			"removing one of two leaves its sibling, which is spliced into the like split above",
			layoutNested, "%2",
			withChecksum("200x50,0,0{50x50,0,0,0,49x50,51,0,5,99x50,101,0[99x25,101,0,1,99x12,101,26,3,99x11,101,39,4]}"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Without(tc.layout, tc.pane)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestWithoutErrors(t *testing.T) {
	for name, tc := range map[string][2]string{
		"missing pane":    {layoutThree, "%9"},
		"empty pane":      {layoutThree, ""},
		"only pane":       {withChecksum("200x50,0,0,0"), "%0"},
		"bad checksum":    {"0000,200x50,0,0,0", "%0"},
		"empty":           {"", "%0"},
		"truncated":       {withChecksum("200x50,0,0{100x50,0,0,0,99x50,101,0,1"), "%0"},
		"trailing":        {withChecksum("200x50,0,0,0x"), "%1"},
		"zero size":       {withChecksum("0x50,0,0{100x50,0,0,0,99x50,101,0,1}"), "%0"},
		"not a number":    {withChecksum("axb,0,0,0"), "%0"},
		"split no kids":   {withChecksum("200x50,0,0{}"), "%0"},
		"bad json":        {`{"V":2,"L":`, "%0"},
		"json odd type":   {`{"V":2,"L":{"t":"q","w":1,"h":1}}`, "%0"},
		"json no kids":    {`{"V":2,"L":{"t":"h","w":9,"h":9,"x":0,"y":0,"c":[]}}`, "%0"},
		"json bad pane":   {`{"V":2,"L":{"t":"p","w":9,"h":9,"x":0,"y":0,"I":"x"}}`, "%0"},
		"json only pane":  {`{"V":2,"L":{"t":"p","w":9,"h":9,"x":0,"y":0,"I":"%0"}}`, "%0"},
		"json other pane": {`{"V":2,"L":{"t":"p","w":9,"h":9,"x":0,"y":0,"I":"%0"}}`, "%1"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Without(tc[0], tc[1]); err == nil {
				t.Errorf("want an error, got %q", got)
			}
		})
	}
}

func TestWithoutJSONForm(t *testing.T) {
	// What tmux 3.8 prints for layoutThree to an ordinary client.
	j := `{"V":2,"L":{"t":"h","w":200,"h":50,"x":0,"y":0,"c":[{"t":"p","w":100,"h":50,"x":0,"y":0,"l":1,"i":0,"I":"%0"},{"t":"v","w":99,"h":50,"x":101,"y":0,"c":[{"t":"p","w":99,"h":25,"x":101,"y":0,"l":0,"i":1,"I":"%1"},{"t":"p","w":99,"h":24,"x":101,"y":26,"a":true,"i":2,"I":"%2"}]}]}}`
	got, err := Without(j, "%0")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Without(layoutThree, "%0")
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// tmux itself is the judge of whether the result is a layout it accepts:
// remove each pane of a real window, then apply the result to what is left.
func TestWithoutAcceptedByTmux(t *testing.T) {
	tm := tmuxtest.New(t, "").Run
	for victim := 0; victim < 6; victim++ {
		tm("kill-server") //nolint:errcheck
		if out, err := tm("new-session", "-d", "-x", "200", "-y", "50", "sleep 300"); err != nil {
			t.Fatalf("new-session: %v %s", err, out)
		}
		for _, flag := range []string{"-h", "-v", "-h", "-v", "-h"} {
			if out, err := tm("split-window", flag, "-d", "sleep 300"); err != nil {
				t.Fatalf("split-window: %v %s", err, out)
			}
		}
		// Split inside the last pane too, so a split holds a split.
		tm("split-window", "-v", "-d", "-t", "%2", "sleep 300") //nolint:errcheck
		layout, err := tm("display-message", "-p", "#{window_layout}")
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("%%%d", victim)
		next, err := Without(layout, id)
		if err != nil {
			t.Fatalf("Without(%s, %s): %v", layout, id, err)
		}
		if out, err := tm("kill-pane", "-t", id); err != nil {
			t.Fatalf("kill-pane: %v %s", err, out)
		}
		if out, err := tm("select-layout", next); err != nil {
			t.Fatalf("tmux rejected %s (from %s, minus %s): %v %s", next, layout, id, err, out)
		}
	}
}
