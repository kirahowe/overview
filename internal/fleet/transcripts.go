package fleet

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Facts is what a session's transcripts say about it.
type Facts struct {
	Title      string
	LastPrompt string
	// LastReply is the last thing the session said, empty while it works
	// on a prompt it has not answered yet.
	LastReply string
	ReplyAt   time.Time
	// Modified is when the transcript was last written.
	Modified time.Time
	// Links are the issues the session took on, oldest first.
	Links []TaskRef
}

// Transcripts is an index of transcripts. For each file it remembers how
// far it has read and what the lines up to there add up to, so a read
// costs only the bytes appended since the last one. It is safe for
// concurrent use.
type Transcripts struct {
	mu       sync.Mutex
	sessions map[string]*session // by the session transcript's path
	found    map[string]string   // transcript paths located by session id
}

// NewTranscripts returns an empty index.
func NewTranscripts() *Transcripts {
	return &Transcripts{sessions: map[string]*session{}, found: map[string]string{}}
}

// Retain forgets every session whose transcript is not in paths, and the
// transcript locations remembered for them.
func (t *Transcripts) Retain(paths []string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for p := range t.sessions {
		if !slices.Contains(paths, p) {
			delete(t.sessions, p)
		}
	}
	for id, p := range t.found {
		if !slices.Contains(paths, p) {
			delete(t.found, id)
		}
	}
}

// Find locates a session's transcript under h, searching the disk once and
// again only when the path found no longer exists. It returns "" when there
// is none.
func (t *Transcripts) Find(h Home, id string) string {
	if t == nil {
		return h.FindTranscript(id)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.found[id]; ok {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		delete(t.found, id)
	}
	p := h.FindTranscript(id)
	if p != "" {
		t.found[id] = p
	}
	return p
}

// session is one transcript and its subagents'.
type session struct {
	main *tfile
	subs map[string]*tfile // by path
}

// Facts reads whatever has been appended to the session transcript at
// path, and to its subagents' transcripts, since the last call, and
// returns what they say. It reports false when the transcript cannot be
// read.
func (t *Transcripts) Facts(path string) (Facts, bool) {
	if t == nil || path == "" {
		return Facts{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[path]
	if s == nil {
		s = &session{main: newTfile(false), subs: map[string]*tfile{}}
		t.sessions[path] = s
	}
	mod, err := s.main.refresh(path)
	if err != nil {
		return Facts{}, false
	}
	// Subagents keep their transcripts in <session>/subagents/.
	dir := strings.TrimSuffix(path, filepath.Ext(path))
	found, _ := filepath.Glob(filepath.Join(dir, "subagents", "agent-*.jsonl"))
	for p := range s.subs {
		if !slices.Contains(found, p) {
			delete(s.subs, p)
		}
	}
	links := slices.Clone(s.main.fold.links)
	for _, p := range found {
		sub := s.subs[p]
		if sub == nil {
			sub = newTfile(true)
			s.subs[p] = sub
		}
		if _, err := sub.refresh(p); err != nil {
			continue
		}
		links = append(links, sub.fold.links...)
	}
	// Each link is timed when its result arrived, so the files' events
	// interleave by time. Events of one file keep their order.
	slices.SortStableFunc(links, func(a, b TaskRef) int { return a.Linked.Compare(b.Linked) })
	f := s.main.fold
	return Facts{
		Title: f.title, LastPrompt: f.lastPrompt,
		LastReply: f.reply, ReplyAt: f.replyAt,
		Modified: mod, Links: FoldTasks(links),
	}, true
}

// tfile is the read state of one transcript file.
type tfile struct {
	fold folded
	off  int64  // bytes read
	tail []byte // the unterminated line at the end of them
	head []byte // the file's first bytes, to tell a rewrite from an append
	id   fileID
}

func newTfile(sub bool) *tfile { return &tfile{fold: newFolded(sub)} }

const headLen = 64

// refresh reads what was appended to the file at path. A file that shrank
// or was replaced is read again from the start. It returns the file's
// modification time.
func (f *tfile) refresh(path string) (time.Time, error) {
	file, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return time.Time{}, err
	}
	id := fileIDOf(info)
	if id != f.id || info.Size() < f.off || !f.sameHead(file) {
		*f = *newTfile(f.fold.sub)
		f.id = id
	}
	if _, err := file.Seek(f.off, io.SeekStart); err != nil {
		return time.Time{}, err
	}
	// Read in pieces, so a transcript of hundreds of megabytes is never
	// held whole.
	buf := make([]byte, 256<<10)
	for {
		n, err := file.Read(buf)
		f.feed(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			return time.Time{}, err
		}
	}
	return info.ModTime(), nil
}

// sameHead says whether the file still begins with the bytes it began with.
func (f *tfile) sameHead(file *os.File) bool {
	if len(f.head) == 0 {
		return true
	}
	got := make([]byte, len(f.head))
	n, _ := file.ReadAt(got, 0)
	return bytes.Equal(got[:n], f.head)
}

// feed adds the next bytes of the file. A line is folded once it ends,
// whatever the chunk boundaries, so the facts do not depend on where the
// reads split.
func (f *tfile) feed(b []byte) {
	f.off += int64(len(b))
	if n := headLen - len(f.head); n > 0 {
		f.head = append(f.head, b[:min(n, len(b))]...)
	}
	data := b
	if len(f.tail) > 0 {
		data = append(f.tail, b...)
	}
	consumed := 0
	for {
		i := bytes.IndexByte(data[consumed:], '\n')
		if i < 0 {
			break
		}
		f.fold.line(data[consumed : consumed+i])
		consumed += i + 1
	}
	f.tail = bytes.Clone(data[consumed:])
}

// folded is what a transcript's lines add up to so far. It sees one line
// at a time and is the whole of the transcript rules, with no IO.
type folded struct {
	sub        bool // a subagent's transcript: its prompts link nothing
	title      string
	lastPrompt string
	reply      string
	replyAt    time.Time
	links      []TaskRef // in the order the results arrived, timed by them
	pending    map[string][]TaskRef
}

func newFolded(sub bool) folded { return folded{sub: sub, pending: map[string][]TaskRef{}} }

var (
	typeAiTitle    = []byte(`"type":"ai-title"`)
	typeLastPrompt = []byte(`"type":"last-prompt"`)
	typeUser       = []byte(`"type":"user"`)
	typeAssistant  = []byte(`"type":"assistant"`)
)

// tline is a transcript line reduced to what the fold uses.
type tline struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Timestamp   string `json:"timestamp"`
	Cwd         string `json:"cwd"`
	AiTitle     string `json:"aiTitle"`
	LastPrompt  string `json:"lastPrompt"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type tblock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
	IsError   bool   `json:"is_error"`
	Input     struct {
		Command string `json:"command"`
	} `json:"input"`
}

// line folds one complete line. Lines of a kind the fold does not use are
// dropped on a byte search before any decoding, since they are most of a
// transcript and some are megabytes.
func (f *folded) line(b []byte) {
	switch {
	case bytes.Contains(b, typeUser), bytes.Contains(b, typeAssistant):
	case bytes.Contains(b, typeAiTitle), bytes.Contains(b, typeLastPrompt):
	default:
		return
	}
	var e tline
	if json.Unmarshal(b, &e) != nil {
		return
	}
	switch e.Type {
	case "ai-title":
		f.title = e.AiTitle
		return
	case "last-prompt":
		f.lastPrompt = clip(e.LastPrompt, 300)
		return
	case "user", "assistant":
	default:
		return
	}
	at, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
	var prose string
	var blocks []tblock
	if c := bytes.TrimSpace(e.Message.Content); len(c) > 0 && c[0] == '"' {
		_ = json.Unmarshal(c, &prose)
	} else if len(c) > 0 && c[0] == '[' {
		_ = json.Unmarshal(c, &blocks)
		var parts []string
		for _, bl := range blocks {
			if bl.Type == "text" && strings.TrimSpace(bl.Text) != "" {
				parts = append(parts, bl.Text)
			}
		}
		prose = strings.Join(parts, "\n")
	}
	switch e.Type {
	case "assistant":
		for _, bl := range blocks {
			if bl.Type == "tool_use" && bl.Name == "Bash" && bl.ID != "" {
				if refs := IssuesStarts(bl.Input.Command, e.Cwd); len(refs) > 0 {
					f.pending[bl.ID] = refs
				}
			}
		}
	case "user":
		for _, bl := range blocks {
			if bl.Type != "tool_result" {
				continue
			}
			refs, ok := f.pending[bl.ToolUseID]
			if !ok {
				continue
			}
			delete(f.pending, bl.ToolUseID)
			if bl.IsError {
				continue
			}
			for _, r := range refs {
				r.Linked = at
				f.links = append(f.links, r)
			}
		}
	}
	if e.IsSidechain || e.IsMeta {
		return
	}
	switch {
	case prose == "":
	case e.Type == "user":
		// A new prompt makes the reply before it stale.
		f.reply, f.replyAt = "", time.Time{}
		if !f.sub {
			if r, ok := OpeningTask(prose, e.Cwd); ok {
				r.Linked = at
				f.links = append(f.links, r)
			}
		}
	default:
		f.reply, f.replyAt = prose, at
	}
}
