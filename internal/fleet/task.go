package fleet

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// maxTasks is how many linked issues a session's tasks file keeps, and
// compactAt how many lines it may grow to before the hook rewrites it.
const (
	maxTasks  = 8
	compactAt = 64
)

// TaskRef names an issue a session took on, as the hook saw it. Project is
// the issues project id when the reference gave one; Dir is the directory the
// command named (made absolute against the event's cwd) or else the event's cwd.
// Linked is when the session first took it on, zero when that is unknown.
type TaskRef struct {
	Project string    `json:"project,omitempty"`
	Dir     string    `json:"dir,omitempty"`
	ID      int       `json:"id"`
	Linked  time.Time `json:"linked,omitzero"`
}

// Same says whether two refs name the same issue the same way, whenever
// each was linked.
func (r TaskRef) Same(o TaskRef) bool {
	return r.Project == o.Project && r.Dir == o.Dir && r.ID == o.ID
}

// EventTasks returns the issues an event shows the session taking on. The
// event's own cwd wins over fallbackCwd, the one the session record holds.
func EventTasks(ev HookEvent, fallbackCwd string) []TaskRef {
	cwd := ev.Cwd
	if cwd == "" {
		cwd = fallbackCwd
	}
	switch ev.Name {
	case "UserPromptSubmit":
		if r, ok := OpeningTask(ev.Prompt, cwd); ok {
			return []TaskRef{r}
		}
	case "PostToolUse":
		if ev.ToolName != "Bash" {
			return nil
		}
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(ev.ToolInput, &in) != nil {
			return nil
		}
		return IssuesStarts(in.Command, cwd)
	}
	return nil
}

// A session's tasks file is a log the hook only appends to, one ref as a
// JSON object per line. The session's own hook and those of its parallel
// subagents all link to it, and an append of one short line is atomic, so
// no link can drop another the way a read, merge and rewrite would.

// FoldTasks is the task list that lines, read in order, add up to: each
// ref once, as late as it was last linked, with the first time it was
// linked, and the newest maxTasks of them.
func FoldTasks(lines []TaskRef) []TaskRef {
	var out []TaskRef
	for _, r := range lines {
		if i := slices.IndexFunc(out, r.Same); i >= 0 {
			if !out[i].Linked.IsZero() {
				r.Linked = out[i].Linked
			}
			out = slices.Delete(out, i, i+1)
		}
		out = append(out, r)
	}
	if len(out) > maxTasks {
		out = out[len(out)-maxTasks:]
	}
	return out
}

// TasksToAppend is the lines the hook appends to link refs, at now, to a
// session whose tasks file folds to cur. A ref already the last linked
// adds nothing, so an event seen twice writes nothing the second time. A
// ref linked before keeps its first time.
func TasksToAppend(cur, refs []TaskRef, now time.Time) []TaskRef {
	var out []TaskRef
	for _, r := range refs {
		if len(cur) > 0 && cur[len(cur)-1].Same(r) {
			continue
		}
		r.Linked = now
		if i := slices.IndexFunc(cur, r.Same); i >= 0 && !cur[i].Linked.IsZero() {
			r.Linked = cur[i].Linked
		}
		out = append(out, r)
		cur = FoldTasks(append(slices.Clone(cur), r))
	}
	return out
}

// ParseTasks reads a tasks file's lines. A line that does not parse, such
// as one cut short by a full disk, is skipped.
func ParseTasks(b []byte) []TaskRef {
	var out []TaskRef
	for _, line := range bytes.Split(b, []byte("\n")) {
		var r TaskRef
		if len(bytes.TrimSpace(line)) > 0 && json.Unmarshal(line, &r) == nil && r.ID > 0 {
			out = append(out, r)
		}
	}
	return out
}

func (h Home) tasksFile(id string) string {
	return filepath.Join(h.sessionsDir(), filepath.Base(id)+".tasks")
}

// readTasks is the session's tasks file's lines, nil when it has none.
func (h Home) readTasks(id string) []TaskRef {
	b, err := os.ReadFile(h.tasksFile(id))
	if err != nil {
		return nil
	}
	return ParseTasks(b)
}

// loadTasks is the issues the session has taken on.
func (h Home) loadTasks(id string) []TaskRef { return FoldTasks(h.readTasks(id)) }

// linkTasks links what ev shows to its session. It appends to the
// session's tasks file, one write per line, only when a link is new, and
// rewrites the file folded once it has grown past compactAt lines.
func (h Home) linkTasks(ev HookEvent, fallbackCwd string, now time.Time) {
	refs := EventTasks(ev, fallbackCwd)
	if len(refs) == 0 {
		return
	}
	lines := h.readTasks(ev.SessionID)
	add := TasksToAppend(FoldTasks(lines), refs, now)
	if len(add) == 0 || os.MkdirAll(h.sessionsDir(), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(h.tasksFile(ev.SessionID), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	for _, r := range add {
		if b, err := json.Marshal(r); err == nil {
			_, _ = f.Write(append(b, '\n'))
		}
	}
	f.Close()
	if len(lines)+len(add) > compactAt {
		var b []byte
		for _, r := range FoldTasks(h.readTasks(ev.SessionID)) {
			line, _ := json.Marshal(r)
			b = append(append(b, line...), '\n')
		}
		_ = h.writeSessionsFile(h.tasksFile(ev.SessionID), b)
	}
}

// OpeningTask reports the issue named by the opening line overview writes
// when it starts a workstream, "Work on <project>#<n>". Any other mention of
// an issue links nothing, so a prompt that merely talks about one is safe.
func OpeningTask(prompt, cwd string) (TaskRef, bool) {
	rest, ok := strings.CutPrefix(strings.TrimLeftFunc(prompt, unicode.IsSpace), "Work on ")
	if !ok {
		return TaskRef{}, false
	}
	end := 0
	for i, c := range rest {
		alnum := c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9')
		if alnum || i > 0 && (c == '.' || c == '_' || c == '-') {
			end = i + 1
			continue
		}
		break
	}
	project := rest[:end]
	rest, ok = strings.CutPrefix(rest[end:], "#")
	if project == "" || !ok {
		return TaskRef{}, false
	}
	n := 0
	for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
		n++
	}
	if n == 0 {
		return TaskRef{}, false
	}
	if tail := rest[n:]; tail != "" {
		c := tail[0]
		if c != ':' && c != '.' && !unicode.IsSpace(rune(c)) {
			// A multi-byte space still counts, so decode the whole rune.
			r := []rune(tail)[0]
			if !unicode.IsSpace(r) {
				return TaskRef{}, false
			}
		}
	}
	id, err := strconv.Atoi(rest[:n])
	if err != nil || id <= 0 {
		return TaskRef{}, false
	}
	return TaskRef{Project: project, Dir: cwd, ID: id}, true
}

// IssuesStarts finds every `issues start <n>` in a shell command. It is a
// reader for the common shapes, not a shell: no expansion, and command
// substitutions and heredoc bodies are opaque. A `cd <dir>` or `pushd
// <dir>` earlier in the string moves the directory later commands use. A
// `cd` the hook cannot follow (home, `-`, a variable), a `popd`, and a
// group or subshell, whose `cd` may or may not last past it, leave the
// directory unknown, and a start after one links only when it names an
// absolute directory itself. A false link is worse than a missed one, so
// anything unclear links nothing.
func IssuesStarts(command, cwd string) []TaskRef {
	var out []TaskRef
	known := true
	for _, argv := range splitCommands(command) {
		if opensGroup(argv[0]) {
			known = false
			continue
		}
		for len(argv) > 0 && isAssignment(argv[0]) {
			argv = argv[1:]
		}
		if len(argv) == 0 {
			continue
		}
		switch argv[0] {
		case "cd", "pushd":
			if len(argv) == 2 && known && !strings.HasPrefix(argv[1], "-") && !strings.HasPrefix(argv[1], "+") {
				cwd, known = resolveDir(cwd, argv[1])
			} else {
				known = false
			}
			continue
		case "popd":
			known = false
			continue
		}
		if ref, ok := parseStart(argv, cwd, known); ok {
			out = append(out, ref)
		}
	}
	return out
}

// opensGroup says whether a command's first word starts or ends a
// subshell or a brace group, as in `(cd x; ...)` or `{ cd x; ...; }`.
func opensGroup(w string) bool {
	return strings.HasPrefix(w, "(") || strings.HasPrefix(w, "{") || w == "}" || w == ")"
}

func isAssignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for j := 0; j < i; j++ {
		c := w[j]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// opaqueWord reports whether the hook cannot know what a word expands to.
func opaqueWord(w string) bool {
	return strings.HasPrefix(w, "~") || strings.ContainsAny(w, "$`")
}

// resolveDir makes dir absolute against cwd. It reports false for a dir the
// hook cannot expand (~, $VAR, substitutions, empty) and for a relative dir
// when cwd is not an absolute path to resolve it against.
func resolveDir(cwd, dir string) (string, bool) {
	switch {
	case dir == "" || opaqueWord(dir):
		return "", false
	case filepath.IsAbs(dir):
		return filepath.Clean(dir), true
	case cwd == "" || !filepath.IsAbs(cwd):
		return "", false
	}
	return filepath.Join(cwd, dir), true
}

// flagValue reports whether a flag takes a value, and if it is written
// --name=value, that value.
func flagValue(w string) (takes bool, inline string, hasInline bool) {
	name := w
	if i := strings.IndexByte(w, '='); i > 0 && strings.HasPrefix(w, "--") {
		name, inline, hasInline = w[:i], w[i+1:], true
	}
	switch name {
	case "-C", "--dir", "--project", "--config":
		if hasInline && name == "-C" {
			return false, "", false
		}
		return true, inline, hasInline
	}
	return false, "", false
}

func parseStart(argv []string, cwd string, cwdKnown bool) (TaskRef, bool) {
	if argv[0] != "issues" && !strings.HasSuffix(argv[0], "/issues") {
		return TaskRef{}, false
	}
	for _, a := range argv[1:] {
		if a == "-h" || a == "--help" {
			return TaskRef{}, false
		}
	}
	var project, dir string
	var dirSet bool
	// apply consumes a flag at args[i] and returns the next index, or -1
	// when the word is not a known flag.
	apply := func(args []string, i int) int {
		w := args[i]
		switch w {
		case "--json", "--edn", "--all", "--force":
			return i + 1
		}
		takes, val, inline := flagValue(w)
		if !takes {
			return -1
		}
		next := i + 1
		if !inline {
			if next >= len(args) {
				return -1
			}
			val = args[next]
			next++
		}
		switch strings.SplitN(w, "=", 2)[0] {
		case "-C", "--dir":
			dir, dirSet = val, true
		case "--project":
			project = val
		}
		return next
	}
	args := argv[1:]
	i := 0
	for i < len(args) && args[i] != "start" {
		if i = apply(args, i); i < 0 {
			return TaskRef{}, false
		}
	}
	if i >= len(args) {
		return TaskRef{}, false
	}
	i++
	id := 0
	for i < len(args) {
		if next := apply(args, i); next >= 0 {
			i = next
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			// An unknown flag may change what the command does.
			return TaskRef{}, false
		}
		if id != 0 {
			// Anything extra after the id is the command's own business.
			i++
			continue
		}
		n, ok := positiveInt(args[i])
		if !ok {
			return TaskRef{}, false
		}
		id = n
		i++
	}
	if id == 0 || strings.ContainsAny(project, "$`") {
		return TaskRef{}, false
	}
	ref := TaskRef{Project: project, ID: id}
	switch {
	case dirSet:
		d, ok := resolveDir(cwd, dir)
		if !ok || !cwdKnown && !filepath.IsAbs(dir) {
			return TaskRef{}, false
		}
		ref.Dir = d
	case cwdKnown:
		ref.Dir = cwd
	default:
		return TaskRef{}, false
	}
	return ref, true
}

func positiveInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
}

// splitCommands cuts a shell string into simple commands at && || ; | & and
// newlines outside quotes, and each into words with basic quoting. Command
// substitutions become an opaque `$()` or backtick word, heredoc bodies
// are skipped, and so is a comment: an unquoted # that starts a word, to
// the end of its line.
func splitCommands(s string) [][]string {
	var cmds [][]string
	var words []string
	var word strings.Builder
	inWord := false
	endWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	type heredoc struct {
		delim string
		strip bool
	}
	var pending []heredoc
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\'':
			inWord = true
			for i++; i < len(r) && r[i] != '\''; i++ {
				word.WriteRune(r[i])
			}
		case c == '"':
			inWord = true
			for i++; i < len(r) && r[i] != '"'; i++ {
				switch {
				case r[i] == '\\' && i+1 < len(r) && (r[i+1] == '"' || r[i+1] == '\\'):
					i++
					word.WriteRune(r[i])
				case r[i] == '$' && i+1 < len(r) && r[i+1] == '(':
					i = skipSubst(r, i)
					word.WriteString("$()")
				case r[i] == '`':
					i = skipBackticks(r, i)
					word.WriteString("`")
				default:
					word.WriteRune(r[i])
				}
			}
		case c == '$' && i+1 < len(r) && r[i+1] == '(':
			inWord = true
			i = skipSubst(r, i)
			word.WriteString("$()")
		case c == '`':
			inWord = true
			i = skipBackticks(r, i)
			word.WriteString("`")
		case c == '\\':
			if i+1 < len(r) {
				i++
				if r[i] != '\n' {
					inWord = true
					word.WriteRune(r[i])
				}
			}
		case c == '<' && i+2 < len(r) && r[i+1] == '<' && r[i+2] == '<':
			// A here-string: its word is an ordinary argument.
			inWord = true
			word.WriteString("<<<")
			i += 2
		case c == '<' && i+1 < len(r) && r[i+1] == '<':
			j := i + 2
			strip := j < len(r) && r[j] == '-'
			if strip {
				j++
			}
			for j < len(r) && (r[j] == ' ' || r[j] == '\t') {
				j++
			}
			delim, end := heredocDelim(r, j)
			if delim == "" {
				endWord()
				i = j - 1
				break
			}
			endWord()
			pending = append(pending, heredoc{delim, strip})
			i = end - 1
		case c == '\n':
			endCmd()
			pos := i + 1
			for _, h := range pending {
				pos = skipHeredocBody(r, pos, h.delim, h.strip)
			}
			if len(pending) > 0 {
				i = pos - 1
				pending = nil
			}
		case c == '#' && !inWord:
			for i+1 < len(r) && r[i+1] != '\n' {
				i++
			}
		case c == '&' || c == '|' || c == ';':
			endCmd()
		case unicode.IsSpace(c):
			endWord()
		default:
			inWord = true
			word.WriteRune(c)
		}
	}
	endCmd()
	return cmds
}

// skipSubst returns the index of the ) closing the $( at r[i].
func skipSubst(r []rune, i int) int {
	depth := 0
	for ; i < len(r); i++ {
		switch r[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return len(r) - 1
}

// skipBackticks returns the index of the backtick closing the one at r[i].
func skipBackticks(r []rune, i int) int {
	for i++; i < len(r); i++ {
		if r[i] == '\\' {
			i++
		} else if r[i] == '`' {
			return i
		}
	}
	return len(r) - 1
}

// heredocDelim reads a heredoc delimiter word starting at r[j], unquoted, and
// returns it with the index after it.
func heredocDelim(r []rune, j int) (string, int) {
	var d strings.Builder
	for j < len(r) {
		c := r[j]
		switch {
		case c == '\'' || c == '"':
			k := j + 1
			for k < len(r) && r[k] != c {
				d.WriteRune(r[k])
				k++
			}
			j = k + 1
		case c == '\\' && j+1 < len(r):
			d.WriteRune(r[j+1])
			j += 2
		case unicode.IsSpace(c) || strings.ContainsRune("&|;<>()", c):
			return d.String(), j
		default:
			d.WriteRune(c)
			j++
		}
	}
	return d.String(), len(r)
}

// skipHeredocBody returns the index after the line that ends a heredoc body
// starting at pos, or len(r) when no line does.
func skipHeredocBody(r []rune, pos int, delim string, strip bool) int {
	for pos < len(r) {
		end := pos
		for end < len(r) && r[end] != '\n' {
			end++
		}
		line := string(r[pos:end])
		if strip {
			line = strings.TrimLeft(line, "\t")
		}
		pos = end
		if pos < len(r) {
			pos++
		}
		if line == delim {
			return pos
		}
	}
	return len(r)
}
