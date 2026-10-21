package diffview

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"overview/internal/tui"
)

// palette is every style the viewer draws with, read once from the theme.
type palette struct {
	text, muted, accent, border Style
	added, removed              Style // markers and line numbers
	addedLine, removedLine      Style // the code of a changed line
	addedWord, removedWord      Style // the words that changed in it
	cursor, selected            Style // the cursor's mark and a range's
	file, hunk                  Style
	user, agent                 Style // who wrote a note or reply
	unsent, waiting, resolved   Style
	status, statusKey           Style
	warn                        Style
	syntax                      *chroma.Style
}

func newPalette(t tui.Theme) palette {
	c := func(x lipgloss.Color) Color { return Color(x) }
	text := Style{Fg: c(t.Text)}
	p := palette{
		text:        text,
		muted:       Style{Fg: c(t.Muted)},
		accent:      Style{Fg: c(t.Accent), Bold: true},
		border:      Style{Fg: c(t.Border)},
		added:       Style{Fg: c(t.Added)},
		removed:     Style{Fg: c(t.Removed)},
		addedLine:   Style{Fg: c(t.Text), Bg: c(t.AddedBg)},
		removedLine: Style{Fg: c(t.Text), Bg: c(t.RemovedBg)},
		addedWord:   Style{Fg: c(t.Text), Bg: c(t.AddedWordBg)},
		removedWord: Style{Fg: c(t.Text), Bg: c(t.RemovedWordBg)},
		cursor:      Style{Fg: c(t.Accent), Bold: true},
		selected:    Style{Fg: c(t.Accent)},
		file:        Style{Fg: c(t.Accent), Bold: true},
		hunk:        Style{Fg: c(t.Muted)},
		user:        Style{Fg: c(t.Accent), Bold: true},
		agent:       Style{Fg: c(t.Working), Bold: true},
		unsent:      Style{Fg: c(t.Asking)},
		waiting:     Style{Fg: c(t.Working)},
		resolved:    Style{Fg: c(t.Muted), Italic: true},
		status:      Style{Fg: c(t.StatusFg), Bg: c(t.StatusBg)},
		statusKey:   Style{Fg: c(t.StatusFg), Bg: c(t.StatusBg), Bold: true},
		warn:        Style{Fg: c(t.Blocked), Bold: true},
		syntax:      styles.Get(t.Syntax),
	}
	return p
}

// over is st with the background of bg, for code drawn on a changed
// line.
func (st Style) over(bg Style) Style {
	st.Bg = bg.Bg
	return st
}

// tokenStyle is the colour chroma's style gives a token type, on base.
func (p palette) tokenStyle(tt chroma.TokenType, base Style) Style {
	if p.syntax == nil {
		return base
	}
	e := p.syntax.Get(tt)
	st := base
	if e.Colour.IsSet() {
		st.Fg = Color(e.Colour.String())
	}
	st.Bold = e.Bold == chroma.Yes
	st.Italic = e.Italic == chroma.Yes
	return st
}

// paint writes segs as the terminal draws them, every segment's style
// from scratch, so no style leaks from one segment or line into the next.
func paint(segs []Seg, prof termenv.Profile) string {
	var b strings.Builder
	for _, s := range segs {
		if s.Text == "" {
			continue
		}
		seq := sgr(s.St, prof)
		if seq != "" {
			b.WriteString("\x1b[" + seq + "m")
		}
		b.WriteString(s.Text)
		if seq != "" {
			b.WriteString("\x1b[0m")
		}
	}
	return b.String()
}

// sgr is the parameters of the escape that draws st.
func sgr(st Style, prof termenv.Profile) string {
	var ps []string
	if st.Bold {
		ps = append(ps, "1")
	}
	if st.Dim {
		ps = append(ps, "2")
	}
	if st.Italic {
		ps = append(ps, "3")
	}
	if st.Underline {
		ps = append(ps, "4")
	}
	if st.Reverse {
		ps = append(ps, "7")
	}
	if st.Fg != "" {
		if seq := prof.Color(string(st.Fg)); seq != nil {
			ps = append(ps, seq.Sequence(false))
		}
	}
	if st.Bg != "" {
		if seq := prof.Color(string(st.Bg)); seq != nil {
			ps = append(ps, seq.Sequence(true))
		}
	}
	return strings.Join(ps, ";")
}
