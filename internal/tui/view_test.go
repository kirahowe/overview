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
	"overview/internal/review"
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
		r := fleet.Row{Live: fleet.Live{SessionID: id, Name: name, PID: pid, Cwd: cwd, Kind: "interactive", StartedAt: now.Add(-ago).UnixMilli()}, State: st, Since: now.Add(-ago)}
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
		issues.Issue{ID: 5, Ref: "overview#5", Title: "answer notes in threads", Status: issues.Review, Priority: "p2"},
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
	m.sel = "s1"
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
		rule("overview", "2 waiting issues"),
		l("⠋ squeeze e2e", "#6  working 1m"),
		"▌" + l("⠋ list squeeze", "#6  working 3m")[1:],
		l("? reply on view.go note", "asking 2m"),
		l("○ digest prompt tweaks", "idle 1h"),
		l("$ go", "#6  overview"),
		l("#4 show session cost", "review p2"),
		l("#5 answer notes in threads", "review p2"),
		l("", ""),
		rule("website", "2 sessions · 1 terminal"),
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
		l("? reply on view.go note", "overview"),
		l("! deploy blog post", "website"),
		l("⠋ squeeze e2e", "overview #6"),
		"▎"+l("⠋ list squeeze", "overview #6")[1:],
		l("○ rss feed", "website"),
		l("○ digest prompt tweaks", "overview"),
		l("orchestrator off  s task • c claude", ""), // no tmux, so enter only says so
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
	line := m.line(left, []seg{{"blocked", red}}, false, cursorRow)
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
	if line := m.line(left, nil, false, cursorRow); !strings.Contains(line, fg(pal.Border).Render("▌")) {
		t.Errorf("blurred bar: %q", line)
	}
	if line := m.line(left, nil, false, unmarked); strings.Contains(line, "▌") || strings.Contains(line, bgCode) {
		t.Errorf("a row without the cursor: %q", line)
	}
}

// A row that echoes the cursor has the cursor's background behind a
// thinner bar, which is never the accent's: that says where the keys are.
// It keeps its parts' colours as the cursor's row does, and loses its
// background with it in the panels mode.
func TestAnEchoRowIsQuieterThanTheCursors(t *testing.T) {
	was := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
	m := workModel(t, 44, 28)
	left := []seg{{"! ", red}, {"name", fg(pal.Text)}}
	bg := lipgloss.NewStyle().Background(pal.CursorDimBg).Render("x")
	bgCode := bg[:strings.Index(bg, "x")]
	accent := fg(pal.CursorBg).Render("x")
	accentCode := accent[:strings.Index(accent, "x")]
	for _, c := range []struct {
		what          string
		set           func(*model)
		band, lit, on bool // the rows' background, the cursor's accent, and whether the keys move the cursor
	}{
		{"with the keys in this pane", func(*model) {}, true, true, true},
		{"with the keys in the view's other pane", func(m *model) { m.other = true }, true, false, true},
		{"with the keys in a session's pane", func(m *model) { m.blurred = true }, true, false, false},
		{"in the panels mode", func(m *model) { m.panels = true }, false, false, false},
	} {
		m := m
		c.set(&m)
		cursor, echo := m.line(left, []seg{{"blocked", red}}, false, cursorRow), m.line(left, []seg{{"blocked", red}}, false, echoRow)
		if got := m.cursorHasKeys(); got != c.on {
			t.Errorf("%s: the keys move the cursor %v, want %v", c.what, got, c.on)
		}
		if !strings.HasPrefix(echo, fg(pal.Border).Render("▎")) || strings.Contains(echo, "▌") || strings.Contains(echo, accentCode) {
			t.Errorf("%s: the echo's bar: %q", c.what, echo)
		}
		if strings.Contains(cursor, "▎") || strings.Contains(cursor, accentCode) != c.lit {
			t.Errorf("%s: the cursor's bar, lit %v: %q", c.what, c.lit, cursor)
		}
		for _, row := range []string{cursor, echo} {
			if got := strings.Contains(row, bgCode); got != c.band || ansi.StringWidth(row) != 44 {
				t.Errorf("%s: a row with a background %v, want %v: %q", c.what, got, c.band, row)
			}
		}
		if c.band && !strings.Contains(echo, red.Background(pal.CursorDimBg).Render("! ")) {
			t.Errorf("%s: the echo lost a part's colour: %q", c.what, echo)
		}
	}
}

// A rule the view draws sets the panel's name on a block of colour, as
// tmux does for its panes: the accent while the panel is lit and the
// status bar's colour otherwise. What follows the name keeps its own
// colours, on no block.
func TestAPanelRuleSetsItsNameOnABlock(t *testing.T) {
	was := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(was) })
	m := workModel(t, 44, 28)
	title := m.titleText([]titleSeg{{text: "agents"}, {"  ! 2", pal.Blocked}})
	block := func(fg, bg lipgloss.Color) string {
		return lipgloss.NewStyle().Bold(true).Foreground(fg).Background(bg).Render(" agents ")
	}
	for _, c := range []struct {
		lit         bool
		block, edge string
	}{
		{true, block(pal.PillFg, pal.Accent), fg(pal.Accent).Render("──")},
		{false, block(pal.StatusFg, pal.StatusBg), fg(pal.Border).Render("──")},
	} {
		got := panelRule(title, "", 30, c.lit)
		if !strings.HasPrefix(got, c.edge+c.block+fg(pal.Blocked).Render(" ! 2")+" ") {
			t.Errorf("lit %v: the rule %q", c.lit, got)
		}
		if text := ansi.Strip(got); text != "── agents  ! 2 "+strings.Repeat("─", 15) {
			t.Errorf("lit %v: the rule reads %q", c.lit, text)
		}
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
	col := m.listView(m.sections(), m.marked(m.sections()))
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

// A row of the agents says where the list has its session: its project,
// or the app that runs it, and the tasks it holds, by number when they
// are its project's and by ref when they are another's.
func TestAnAgentsRowNamesItsProject(t *testing.T) {
	app, site := &world.Project{Name: "app"}, &world.Project{Name: "website"}
	st := &world.Task{Key: world.Key{Root: "/u/app", ID: 6}, Project: app, IssuesID: "app"}
	st2 := &world.Task{Key: world.Key{Root: "/u/app", ID: 7}, Project: app, IssuesID: "app"}
	st3 := &world.Task{Key: world.Key{Root: "/u/app", ID: 8}, Project: app, IssuesID: "app"}
	zed := &world.Host{Name: "Zed"}
	for _, c := range []struct {
		what       string
		sess       world.Session
		place, ref string
	}{
		{"on no task", world.Session{Project: site}, "website", ""},
		{"on its project's task", world.Session{Project: app, Holds: []*world.Task{st}}, "app", "#6"},
		{"on another project's task", world.Session{Project: site, Holds: []*world.Task{st}}, "website", "app#6"},
		{"on three tasks", world.Session{Project: app, Holds: []*world.Task{st, st2, st3}}, "app", "#6 #7 +1"},
		{"another app's", world.Session{Project: site, Host: zed}, "Zed", ""},
		{"another app's, on a task", world.Session{Project: site, Host: zed, Holds: []*world.Task{st}}, "Zed", "app#6"},
	} {
		e := entry{kind: sessionEntry, sess: &c.sess, alias: agentsPrefix}
		if place, ref := agentsNote(e, zed.Label()); place != c.place || ref != c.ref {
			t.Errorf("a session %s is at %q %q, want %q %q", c.what, place, ref, c.place, c.ref)
		}
	}
}

// The project in a row of the agents gives way by its width on the
// screen, which for wide characters is less than its length in bytes. It
// shows whole while ten cells of the session's name fit beside it, then
// by its end, and the task's number is the last of the note to go.
func TestAnAgentsRowFitsItsProjectByDisplayWidth(t *testing.T) {
	m := newTestModel(t)
	st := &world.Task{Key: world.Key{Root: "/u/code/projects/日本語日本", ID: 6}, Project: &world.Project{Name: "日本語日本"}}
	s := &world.Session{Row: row("s", "fix-parser-overflow", "", 1), Project: st.Project, Holds: []*world.Task{st}}
	e := entry{kind: sessionEntry, sess: s, alias: agentsPrefix}
	// The mark and ten cells of the name take 12 columns and the note 13:
	// ten of project, which are 15 bytes, a space and the number.
	for _, c := range []struct {
		w    int
		want string
	}{
		{36, "○ fix-parser-overflow 日本語日本 #6"},
		{26, "○ fix-parse… 日本語日本 #6"},
		{25, "○ fix-parse… …本語日本 #6"},
		{21, "○ fix-parser-over… #6"},
		{14, "○ fix-parser-…"},
	} {
		m.w = c.w + 2
		if got := strings.Join(strings.Fields(ansi.Strip(m.entryLine(e, unmarked))), " "); got != c.want {
			t.Errorf("in %d columns %q, want %q", c.w, got, c.want)
		}
	}
}

func TestAnIdleTrackedProjectCountsItsOpenIssues(t *testing.T) {
	idle := section{Title: "backlog", Tracked: true, Open: 4, Entries: []entry{{kind: projectEntry}}}
	if got := sectionNote(idle); got != "4 open issues" {
		t.Errorf("idle tracked project: %q, want 4 open issues", got)
	}
	busy := section{Title: "app", Tracked: true, Open: 4, Waiting: 1, Sessions: 1, Entries: []entry{{kind: sessionEntry}, {kind: issueEntry}}}
	if got := sectionNote(busy); got != "1 waiting issue · 1 session" {
		t.Errorf("project with an issue that waits: %q, want 1 waiting issue · 1 session", got)
	}
}

// Every heading names what it counts, a pinned project with nothing in it
// included, and a search keeps the parts: it says how many of each kind
// the project has it shows, zero included, and a kind the project has
// none of is left out. The search's own count names each session
// once, though the agents list shows it again.
func TestHeadingCountsKeepTheirUnit(t *testing.T) {
	cases := []struct {
		sec  section
		want string
	}{
		{section{Title: "pinned", Entries: []entry{{kind: projectEntry}}}, "no sessions"},
		{section{Title: "web", Sessions: 1, Terminals: 2, Entries: []entry{{kind: sessionEntry}, {kind: terminalEntry}, {kind: terminalEntry}}}, "1 session · 2 terminals"},
		{section{Title: "web", Sessions: 2, Terminals: 1, Entries: []entry{{kind: sessionEntry}}}, "1 of 2 sessions · 0 of 1 terminal"},
		// "/digest" and "/fix" on one project say the same parts.
		{section{Title: "app", Tracked: true, Waiting: 3, Sessions: 1, Entries: []entry{{kind: issueEntry}}}, "1 of 3 waiting issues · 0 of 1 session"},
		{section{Title: "app", Tracked: true, Waiting: 3, Sessions: 1, Entries: []entry{{kind: sessionEntry}}}, "0 of 3 waiting issues · 1 session"},
		{section{Title: "app", Tracked: true, Sessions: 1, Entries: []entry{{kind: sessionEntry}}}, "1 session"},
		{section{Title: "app", Tracked: true, Waiting: 3, Sessions: 2, Entries: []entry{{kind: sessionEntry}}}, "0 of 3 waiting issues · 1 of 2 sessions"},
		{section{Title: "away", Away: true, Sessions: 2, Entries: []entry{{kind: sessionEntry}, {kind: sessionEntry}}}, "2 sessions"},
		{section{Title: "app", Tracked: true, Open: 6, Waiting: 3, Entries: []entry{{kind: issueEntry}}}, "1 of 3 waiting issues"},
		{section{Title: "app", Tracked: true, Open: 6, Waiting: 3}, "0 of 3 waiting issues"},
		{section{Title: "app", Tracked: true}, "no open issues"},
	}
	for _, c := range cases {
		if got := sectionNote(c.sec); got != c.want {
			t.Errorf("%+v: %q, want %q", c.sec, got, c.want)
		}
	}
	m := workModel(t, 120, 40)
	m = update(m, keyMsg("/"))
	for _, k := range typed("squeeze e2e") {
		m = update(m, keyMsg(k))
	}
	text := strings.Join(screenText(m), "\n")
	if !strings.Contains(text, "0 of 2 waiting issues · 1 of 4 sessions") || !strings.Contains(text, "· 1 match ") {
		t.Errorf("a search for one session:\n%s", text)
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

// A note or a status that runs long gives way in the footer: the search
// keeps its input, the keys their room, and the task picker's long path
// is cut from the left. A note shows alone, not after an older one.
func TestTheFooterSharesItsLine(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m = update(m, tea.WindowSizeMsg{Width: 60, Height: 20})
	m.digesting = true
	for _, c := range []struct {
		name string
		set  func(model) model
		want []string
		not  []string
	}{
		{"a note beside the digest", func(m model) model {
			return m.say("only background sessions can be stopped here, not this one")
		}, []string{"only background sess"}, []string{"writing a digest…  writing"}},
		{"a search", func(m model) model {
			m.filtering, m.filter = true, "a long query that goes on and on"
			return m
		}, []string{"on and on▏", "↑↓ move", "esc clear"}, []string{"writing a digest"}},
		{"the task picker", func(m model) model {
			m.taskPick.open, m.taskPick.root = true, "/private/tmp/somewhere/deep/below/project"
			return m
		}, []string{"start a task in …", "below/project", "enter start", "↑↓ move"}, nil},
	} {
		foot := ansi.Strip(c.set(m).footer())
		if ansi.StringWidth(foot) > 60 {
			t.Errorf("%s: %q is wider than the view", c.name, foot)
		}
		for _, w := range c.want {
			if !strings.Contains(foot, w) {
				t.Errorf("%s: %q lacks %q", c.name, foot, w)
			}
		}
		for _, w := range c.not {
			if strings.Contains(foot, w) {
				t.Errorf("%s: %q has %q", c.name, foot, w)
			}
		}
	}
}

// In a 46-cell list pane the task picker's footer names the project as
// the list's headings do, by its World name, so the name and the keys
// both fit. A name too long for the room, such as the path a root
// outside the home is named by, there as in the headings, is cut from
// the left and keeps its last directory.
func TestTheTaskPickersFooterFitsANarrowPane(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	m = update(m, tea.WindowSizeMsg{Width: 48, Height: 20})
	m.user = "/Users/k"
	m = m.setInputs(nil)
	m.taskPick.open = true
	for root, want := range map[string]string{
		"/Users/k/code/projects/gamma":          "start a task in gamma  enter start • ↑↓ move",
		"/Users/k/src/gamma":                    "start a task in …/gamma  enter start • ↑↓ move",
		"/private/tmp/ovqa-1234/projects/gamma": "start a task in …/gamma  enter start • ↑↓ move",
	} {
		m.taskPick.root = root
		foot := ansi.Strip(m.footer())
		if !strings.Contains(foot, want) || ansi.StringWidth(foot) > 48 {
			t.Errorf("%s: footer %q, want %q", root, foot, want)
		}
		if heading := m.world().Name(root); !strings.HasSuffix(heading, "gamma") {
			t.Errorf("%s: the headings name it %q", root, heading)
		}
	}
}

// The footer says how the orchestrator is failing: off, silent since it
// last looked, or looking on time and publishing nothing since the last
// tick that finished.
func TestTheFooterSaysHowTheOrchestratorFails(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11))
	for want, h := range map[string]fleet.Health{
		"orchestrator off":               {},
		"orchestrator silent 3h":         {Running: true, Stale: true, LastCheck: m.now.Add(-3 * time.Hour)},
		"orchestrator not publishing 3d": {Running: true, Behind: true, LastDigest: m.now.Add(-72 * time.Hour)},
		"orchestrator not publishing  ":  {Running: true, Behind: true},
	} {
		m := update(m, rowsMsg{rows: m.in.Rows, ttys: m.in.TTYs, health: h})
		lines := screenText(m)
		if foot := lines[len(lines)-1]; !strings.Contains(foot, want) {
			t.Errorf("the footer is %q, want %q in it", foot, want)
		}
	}
	m = update(m, rowsMsg{rows: m.in.Rows, ttys: m.in.TTYs, health: fleet.Health{Running: true}})
	lines := screenText(m)
	if foot := lines[len(lines)-1]; strings.Contains(foot, "orchestrator") {
		t.Errorf("the footer of a healthy orchestrator is %q", foot)
	}
}

// A note on the digests screen shows alone: the list's search and the
// panels mode belong to the sessions screen.
func TestADigestNoteLeavesTheListsSearchOut(t *testing.T) {
	m := newTestModel(t, row("s1", "red", "auth", 11), row("s2", "red two", "auth", 12))
	m.filter, m.panels = "red", true
	m.screen = digestScreen
	m = m.say("digest failed: boom")
	foot := ansi.Strip(m.footer())
	if !strings.Contains(foot, "digest failed: boom") || strings.Contains(foot, "/red") || strings.Contains(foot, "panels") {
		t.Fatalf("footer %q", foot)
	}
	m.screen = sessionsScreen
	if foot := ansi.Strip(m.footer()); !strings.Contains(foot, "/red") || !strings.Contains(foot, "panels") {
		t.Fatalf("on the sessions screen: footer %q", foot)
	}
}

// A heading's note gives way before its project's name: the whole note,
// then its first part, then a bare count, then nothing.
func TestAHeadingNoteGivesWayToTheName(t *testing.T) {
	sec := section{Title: "website-admin", Sessions: 2, Terminals: 1, Entries: []entry{{kind: sessionEntry}, {kind: terminalEntry}}}
	for _, c := range []struct {
		w    int
		want string
	}{
		{48, "website-admin ─── 1 of 2 sessions · 1 terminal"},
		{40, "website-admin ──────── 1 of 2 sessions"},
		{22, "website-admin ── 1/2"},
		{17, "website-admin ─"},
		{10, "…admin ─"},
	} {
		m := newTestModel(t)
		m = update(m, tea.WindowSizeMsg{Width: c.w, Height: 5})
		if got := strings.TrimSpace(ansi.Strip(m.rule(sec.Title, heading, sectionNotes(sec)...))); got != c.want {
			t.Errorf("at %d: %q, want %q", c.w, got, c.want)
		}
	}
}

// In two columns every help entry is drawn whole, and the second column
// starts one cell after the first's widest line.
func TestTheHelpColumnFitsItsWidestLine(t *testing.T) {
	m := newTestModel(t)
	m = update(m, tea.WindowSizeMsg{Width: 2*helpCol + 10, Height: 80})
	if !m.helpWide() {
		t.Fatalf("one column at %d", m.w)
	}
	body := ansi.Strip(strings.Join(m.helpBody(), "\n"))
	left, right := helpSections()
	for _, s := range append(left, right...) {
		for _, e := range s.entries {
			if !strings.Contains(body, e.what) {
				t.Errorf("%q is cut from the help", e.what)
			}
		}
	}
	m = update(m, tea.WindowSizeMsg{Width: 2*helpCol + 5, Height: 80})
	if m.helpWide() {
		t.Fatalf("two columns at %d, under 2*helpCol+6 inside the box", m.w)
	}
}

// wrap breaks at spaces and after hyphens and keeps a word that fits
// whole, breaks a path wider than the line after a slash, never mid-name
// when a slash fits, and measures a tab as the terminal draws it.
func TestWrapKeepsWordsWholeAndBreaksPathsAtSlashes(t *testing.T) {
	for _, c := range []struct {
		s    string
		w    int
		want []string
	}{
		{"No sessions in …/qa2/work/alpha. c starts one.", 16, []string{"No sessions in", "…/qa2/work/", "alpha. c starts", "one."}},
		{"enter adds ~/long/nonexistent if it is a directory.", 20, []string{"enter adds", "~/long/nonexistent", "if it is a", "directory."}},
		{"no project at /tmp/long/nonexistent", 8, []string{"no", "project", "at /tmp/", "long/", "nonexist", "ent"}},
		// A tab draws to the next stop of four, and is measured so.
		{"a\tb\tccc dddd", 6, []string{"a   b", "ccc", "dddd"}},
		// A hyphen is a break point, after it.
		{"keep the list-width", 15, []string{"keep the list-", "width"}},
		// A glyph wider than the line draws as one ellipsis per word.
		{"日本語", 1, []string{"…"}},
		{"  indented text", 20, []string{"  indented text"}},
	} {
		got := wrap(c.s, c.w)
		for i := range got {
			got[i] = strings.TrimRight(got[i], " ")
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("wrap(%q, %d) = %q, want %q", c.s, c.w, got, c.want)
		}
	}
}

// In the 24-column list pane the help keeps every description: each
// goes under its key, wrapped, rather than cut to three letters.
func TestTheNarrowHelpWrapsDescriptionsUnderTheirKeys(t *testing.T) {
	m := newTestModel(t)
	m = update(m, tea.WindowSizeMsg{Width: 24, Height: 200})
	m, _ = press(m, "?")
	view := ansi.Strip(strings.Join(m.helpBox(), "\n"))
	for _, want := range []string{"│   esc            │", "│     from the     │", "│     list, move   │", "│     focus        │", "│   j k       move │"} {
		if !strings.Contains(view, want) {
			t.Errorf("no %q in\n%s", want, view)
		}
	}
	if strings.Contains(view, "…") {
		t.Errorf("the help is cut:\n%s", view)
	}
}

// One turn is "last turn", not "last 1 turns".
func TestTheDetailsCountTurnsInTheSingular(t *testing.T) {
	m := newTestModel(t)
	r := &world.Session{Row: fleet.Row{SessionID: "s1", Transcript: "/t.jsonl"}}
	for _, c := range []struct {
		n    int
		want string
	}{{1, "── last turn ──"}, {2, "── last 2 turns ──"}} {
		m.turns = recent{key: turnsKey{path: "/t.jsonl"}}
		for i := 0; i < c.n; i++ {
			m.turns.turns = append(m.turns.turns, fleet.Turn{Prompt: "go", Reply: "done"})
		}
		got := ansi.Strip(strings.Join(m.detailLines(r, 40), "\n"))
		if !strings.Contains(got, c.want) {
			t.Errorf("%d turns: no %q in\n%s", c.n, c.want, got)
		}
	}
}

// The task picker's rule keeps its name whole: the counts go first, then
// the home's name is cut.
func TestTagTitleKeepsTheTag(t *testing.T) {
	segs := []titleSeg{{text: "◆ overview"}, {text: "  ! 1"}, {text: "  ○ 3"}}
	tag := titleSeg{text: "  · start a task"}
	text := func(ss []titleSeg) (s string) {
		for _, x := range ss {
			s += x.text
		}
		return s
	}
	for _, c := range []struct {
		w    int
		want string
	}{
		{60, "◆ overview  ! 1  ○ 3  · start a task"},
		{31, "◆ overview  ! 1  · start a task"},
		{19, "◆ …  · start a task"},
		{18, "◆ overview"},
		{6, "◆ ove…"},
	} {
		if got := text(tagTitle(segs, tag, c.w)); got != c.want {
			t.Errorf("at %d: %q, want %q", c.w, got, c.want)
		}
	}
	if len(segs) != 3 || segs[0].text != "◆ overview" {
		t.Fatalf("tagTitle changed its argument: %v", segs)
	}
	for w := 1; w <= 60; w++ {
		if got := text(tagTitle(segs, tag, w)); ansi.StringWidth(got) > w {
			t.Errorf("at %d: %q is wider", w, got)
		}
	}
}

// In the 24-column list a row keeps ten cells of its name: a session
// drops its review badge's words before the name is cut further, an
// issue its priority, and the agents show a long project by its end
// rather than not at all.
func TestNarrowRowsKeepTheirNames(t *testing.T) {
	m := newTestModel(t)
	m.w = 24
	r := row("s", "fix-parser-overflow", "", 1)
	r.LastReply = "ok?"
	s := &world.Session{Row: r, Project: &world.Project{Name: "/private/tmp/qa2/work/alpha"}, Review: review.Summary{Open: 1, Waiting: 1}}
	st := &world.Task{Key: world.Key{Root: "/a", ID: 7}, Issue: issues.Issue{ID: 7, Title: "Digest scheduling", Status: issues.Review, Priority: "p0"}}
	for _, c := range []struct {
		e    entry
		want string
	}{
		{entry{kind: sessionEntry, sess: s}, "? fix-parse… ✎1 asking"},
		{entry{kind: sessionEntry, sess: s, alias: agentsPrefix}, "? fix-parse… …rk/alpha"},
		{entry{kind: issueEntry, task: st}, "#7 Digest sche… review"},
	} {
		if got := strings.TrimSpace(ansi.Strip(m.entryLine(c.e, unmarked))); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
}

func TestASessionRowShowsItsTitle(t *testing.T) {
	m := newTestModel(t)
	m.w = 60
	for _, c := range []struct{ title, want string }{
		{"Fix the parser's overflow", "Fix the parser's overflow"},
		{"", "home-opds-4c"},
	} {
		r := row("s", "home-opds-4c", "", 1)
		r.Title = c.title
		s := &world.Session{Row: r, Project: &world.Project{Name: "home-opds"}}
		got := ansi.Strip(m.entryLine(entry{kind: sessionEntry, sess: s}, unmarked))
		if !strings.Contains(got, c.want) || c.title != "" && strings.Contains(got, "home-opds-4c") {
			t.Errorf("title %q: %q, want %q in place of the name", c.title, got, c.want)
		}
	}
}
