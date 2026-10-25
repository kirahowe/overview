package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"overview/internal/fleet"
	"overview/internal/issues"
	"overview/internal/tmux"
	"overview/internal/world"
)

const (
	ovRoot  = "/u/code/projects/overview"
	webRoot = "/u/code/projects/website"
)

// workModel is the mockup's list: overview tracks its issues, with a
// review that nobody works on, a task with two sessions and a terminal,
// and one with a session that asks something; website does not.
func workModel(t *testing.T, w, h int) model {
	t.Helper()
	now := time.Now()
	sess := func(id, name, cwd string, st fleet.State, ago time.Duration, pid int, refs ...int) fleet.Row {
		r := fleet.Row{Live: fleet.Live{SessionID: id, Name: name, PID: pid, Cwd: cwd, Kind: "interactive"}, State: st, Since: now.Add(-ago)}
		for _, n := range refs {
			r.Tasks = append(r.Tasks, fleet.TaskRef{Project: "overview", Dir: cwd, ID: n})
		}
		return r
	}
	asking := sess("s3", "reply on view.go note", ovRoot, fleet.Idle, 10*time.Minute, 13, 5)
	asking.LastReply, asking.LastReplyAt = "Which one?", now.Add(-2*time.Minute)
	rows := []fleet.Row{
		sess("s1", "list squeeze", ovRoot, fleet.Working, 3*time.Minute, 11, 6),
		sess("s2", "squeeze e2e", ovRoot, fleet.Working, time.Minute+5*time.Second, 12, 6),
		asking,
		sess("s4", "digest prompt tweaks", ovRoot, fleet.Idle, time.Hour, 14),
		sess("s5", "deploy blog post", webRoot, fleet.Blocked, 5*time.Minute, 15),
		sess("s6", "rss feed", webRoot, fleet.Idle, 20*time.Minute, 16),
	}
	m := newTestModel(t)
	m.user, m.session = "/u", "ov"
	m.in.Panes = []tmux.Pane{
		{ID: "%7", Session: "ov", Window: "@7", Command: "go", Path: ovRoot, Project: ovRoot, Task: ovRoot + "#6"},
		{ID: "%8", Session: "ov", Window: "@8", Command: "npm", Path: webRoot, Project: webRoot},
	}
	ttys := map[int]string{}
	for _, r := range rows {
		ttys[r.PID] = "ttys" + r.SessionID
	}
	ts := trackers{ovRoot: tracked("overview",
		issues.Issue{ID: 4, Ref: "overview#4", Title: "show session cost", Status: issues.Review, Priority: "p2"},
		issues.Issue{ID: 6, Ref: "overview#6", Title: "keep the list's width", Status: issues.InProgress, Priority: "p1"},
		issues.Issue{ID: 5, Ref: "overview#5", Title: "answer notes in threads", Status: issues.Ready, Priority: "p2"},
		issues.Issue{ID: 9, Ref: "overview#9", Title: "not started", Status: issues.Ready, Priority: "p0"},
	)}
	m = update(m, tea.WindowSizeMsg{Width: w, Height: h})
	m.trackers = ts
	m = m.setInputs(nil)
	return update(m, rowsMsg{rows: rows, ttys: ttys})
}

func screenText(m model) []string {
	return strings.Split(ansi.Strip(m.View()), "\n")
}

func TestTheListLooksLikeTheMockup(t *testing.T) {
	m := workModel(t, 44, 28)
	m.sel = "task:" + ovRoot + "#6"
	m.frame = 0
	got := screenText(m)
	// l is a line of the list: left and right in a 42-cell content
	// column, between the bar's column and a blank one.
	l := func(left, right string) string {
		return " " + left + strings.Repeat(" ", 42-ansi.StringWidth(left)-ansi.StringWidth(right)) + right + " "
	}
	rule := func(title, note string) string {
		return l(title+" "+strings.Repeat("─", 42-ansi.StringWidth(title)-ansi.StringWidth(note)-2), note)
	}
	// tmux draws the rule above, with the list's title in it.
	want := []string{
		rule("overview", "3 tasks"),
		l("#4 show session cost", "review p2"),
		l("   no sessions", ""),
		"▌" + l("#6 keep the list's width", "p1")[1:],
		l("   ⠋ squeeze e2e", "working 1m"),
		l("   ⠋ list squeeze", "working 3m"),
		l("   $ go", "overview"),
		l("#5 answer notes in threads", "ready p2"),
		l("   ? reply on view.go note", "asking 2m"),
		l("○ digest prompt tweaks", "idle 1h"),
		l("", ""),
		rule("website", "3"),
		l("! deploy blog post", "blocked 5m"),
		l("○ rss feed", "idle 20m"),
		l("$ npm", "website"),
	}
	// The agents panel sits at the bottom of the list's 27 lines, under a
	// rule of its own, drawn as tmux draws one.
	for len(want) < 20 {
		want = append(want, l("", ""))
	}
	want = append(want,
		"── agents "+strings.Repeat("─", 44-10-12)+" priority ──",
		l("? reply on view.go note", "#5"),
		l("! deploy blog post", "website"),
		l("⠋ squeeze e2e", "#6"),
		l("⠋ list squeeze", "#6"),
		l("○ rss feed", "website"),
		l("○ digest prompt tweaks", "overview"),
		l("orchestrator off  enter type • s task", ""),
	)
	if len(got) != len(want) {
		t.Errorf("%d lines, want %d", len(got), len(want))
	}
	for i := range got {
		if i >= len(want) || got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[min(i, len(want)-1)])
		}
	}
	if t.Failed() {
		t.Logf("screen:\n%s", strings.Join(got, "\n"))
	}
}

// The cursor's row keeps each part's own colour on its background, and
// its bar is the accent while the list has the keyboard.
func TestTheCursorRowKeepsItsColours(t *testing.T) {
	was := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
	m := workModel(t, 44, 28)
	left := []seg{{"! ", red}, {"name", fg(pal.Text)}}
	line := m.line(left, []seg{{"blocked", red}}, false, true)
	bg := lipgloss.NewStyle().Background(pal.CursorDimBg).Render("x")
	bgCode := bg[:strings.Index(bg, "x")]
	for _, part := range []string{
		red.Background(pal.CursorDimBg).Render("! "),
		fg(pal.Text).Background(pal.CursorDimBg).Render("name"),
		red.Background(pal.CursorDimBg).Render("blocked"),
		fg(pal.CursorBg).Render("▌"),
	} {
		if !strings.Contains(line, part) {
			t.Errorf("the row %q lacks %q", line, part)
		}
	}
	if !strings.Contains(line, bgCode) || ansi.StringWidth(line) != 44 {
		t.Errorf("row %q", line)
	}
	m.blurred = true
	if line := m.line(left, nil, false, true); !strings.Contains(line, fg(pal.Border).Render("▌")) {
		t.Errorf("blurred bar: %q", line)
	}
	if line := m.line(left, nil, false, false); strings.Contains(line, "▌") || strings.Contains(line, bgCode) {
		t.Errorf("a row without the cursor: %q", line)
	}
}

// The footer drops whole hints that do not fit, never half of one.
func TestTheFooterDropsWholeHints(t *testing.T) {
	hs := []hint{{"enter", "type"}, {"c", "claude"}, {"t", "term"}, {"?", "help"}}
	for w := 0; w < 50; w++ {
		got := ansi.Strip(hintLine(nil, hs, w))
		if ansi.StringWidth(got) > w && got != "" {
			t.Fatalf("width %d: %q", w, got)
		}
		for _, part := range strings.Split(got, " • ") {
			if part != "" && !slices.ContainsFunc(hs, func(h hint) bool { return part == h.key+" "+h.label }) {
				t.Fatalf("width %d: a cut hint %q in %q", w, part, got)
			}
		}
	}
	if got := ansi.Strip(hintLine(nil, hs, 50)); got != "enter type • c claude • t term • ? help" {
		t.Fatalf("%q", got)
	}
}

func TestTheAgentsListFollowsWhenTheListIsLong(t *testing.T) {
	m := workModel(t, 44, 12)
	col := m.listView(m.sections(), m.sel)
	lines := col.lines
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(ansi.Strip(l), "── agents ──") })
	if i != col.rule || col.agentsH < 1 || col.projH+1+col.agentsH != m.listHeight() || len(col.owners) != len(lines) {
		t.Fatalf("agents at line %d of\n%s", i, strings.Join(lines, "\n"))
	}
	// The cursor at the end scrolls the list so the agents list shows.
	m, _ = press(m, "G")
	if got := screenText(m); !strings.Contains(got[len(got)-2], "digest prompt tweaks") {
		t.Fatalf("after G:\n%s", strings.Join(got, "\n"))
	}
}

// The agents list names a workstream's project when its width fits, which
// for wide characters is less than its length in bytes.
func TestAgentsNoteMeasuresDisplayWidth(t *testing.T) {
	m := newTestModel(t)
	m.w, m.user = 22, "/u" // 20 columns of content
	st := &world.Stream{Key: world.Key{Root: "/u/code/projects/日本語日本", ID: 6}, Project: &world.Project{Name: "日本語日本"}}
	s := &world.Session{Row: row("s", "x", "", 1), Project: &world.Project{Name: "/w/x"}, Streams: []*world.Stream{st}}
	e := entry{kind: sessionEntry, sess: s, alias: agentsPrefix}
	// 6 used, 10 columns of name (15 bytes), 2 of ref and 2 of spaces.
	if got := m.agentsNote(e, "", false, 6); got != "日本語日本 #6" {
		t.Fatalf("note %q, want the project's name too", got)
	}
	if got := m.agentsNote(e, "", false, 7); got != "#6" {
		t.Fatalf("note %q with no room, want only the ref", got)
	}
}

func TestAnIdleTrackedProjectCountsItsOpenIssues(t *testing.T) {
	idle := section{Title: "backlog", Tracked: true, Open: 4, Entries: []entry{{kind: projectEntry}}}
	if got := sectionNote(idle); got != "4 open" {
		t.Errorf("idle tracked project: %q, want 4 open", got)
	}
	busy := section{Title: "app", Tracked: true, Open: 4, Entries: []entry{{kind: workstreamEntry}, {kind: sessionEntry}}}
	if got := sectionNote(busy); got != "1 task" {
		t.Errorf("project with a workstream: %q, want 1 task", got)
	}
}

// The list's title warns that the hook is not installed when sessions are
// listed and none reports through it, since then none can show blocked.
func TestTheTitleSaysWhenTheHookIsMissing(t *testing.T) {
	title := func(m model) string {
		var b strings.Builder
		for _, s := range m.listTitle() {
			b.WriteString(s.text)
		}
		return b.String()
	}
	if got := title(newTestModel(t)); strings.Contains(got, noHook) {
		t.Errorf("with no sessions: %q", got)
	}
	a, b := row("a", "red", "", 1), row("b", "blue", "", 2)
	if got := title(newTestModel(t, a, b)); !strings.Contains(got, noHook) {
		t.Errorf("with no hooked session: %q", got)
	}
	b.Hooked = true
	if got := title(newTestModel(t, a, b)); strings.Contains(got, noHook) {
		t.Errorf("with a hooked session: %q", got)
	}
	// Background sessions report blocked through Claude Code.
	bg := row("c", "green", "", 3)
	bg.Kind, bg.ID = "background", "bg1"
	if got := title(newTestModel(t, bg)); strings.Contains(got, noHook) {
		t.Errorf("with only a background session: %q", got)
	}
}
