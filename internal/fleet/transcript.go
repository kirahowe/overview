package fleet

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// entry is one line of a Claude Code transcript, reduced to the fields used
// here.
type entry struct {
	Type        string    `json:"type"`
	IsSidechain bool      `json:"isSidechain"`
	IsMeta      bool      `json:"isMeta"`
	Timestamp   time.Time `json:"timestamp"`
	Cwd         string    `json:"cwd"`
	AiTitle     string    `json:"aiTitle"`
	LastPrompt  string    `json:"lastPrompt"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// text returns the entry's prose. Content is either a plain string or a list
// of blocks, of which only the text blocks count: tool calls, tool results,
// thinking and images are skipped.
func (e entry) text() string {
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// eachEntry calls fn for every line of a transcript. Lines can run to
// megabytes when they hold a pasted image, so they are read without a length
// limit.
func eachEntry(r io.Reader, fn func(entry)) {
	br := bufio.NewReaderSize(r, 1<<16)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var e entry
			if json.Unmarshal(line, &e) == nil {
				fn(e)
			}
		}
		if err != nil {
			return
		}
	}
}

// FindTranscript locates a session's transcript by id. Transcripts sit at
// <home>/projects/<project>/<session>.jsonl.
func (h Home) FindTranscript(sessionID string) string {
	m, _ := filepath.Glob(filepath.Join(string(h), "projects", "*", filepath.Base(sessionID)+".jsonl"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

type meta struct {
	Title      string
	LastPrompt string
	Modified   time.Time
}

var metaCache sync.Map // path -> meta

// transcriptMeta reads the session title and last prompt that Claude Code
// keeps appending to the transcript. Only the tail is read, and the result is
// reused until the file changes, since the view asks every couple of seconds.
func transcriptMeta(path string) (meta, bool) {
	if path == "" {
		return meta{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return meta{}, false
	}
	if c, ok := metaCache.Load(path); ok && c.(meta).Modified.Equal(info.ModTime()) {
		return c.(meta), true
	}
	f, err := os.Open(path)
	if err != nil {
		return meta{}, false
	}
	defer f.Close()
	const tail = 512 << 10
	if info.Size() > tail {
		// The first line read after the seek is a fragment and fails to parse.
		f.Seek(info.Size()-tail, io.SeekStart)
	}
	m := meta{Modified: info.ModTime()}
	eachEntry(f, func(e entry) {
		switch e.Type {
		case "ai-title":
			m.Title = e.AiTitle
		case "last-prompt":
			m.LastPrompt = clip(e.LastPrompt, 300)
		}
	})
	metaCache.Store(path, m)
	return m, true
}

// Turn is one prompt and the last thing the session said in answer to it.
type Turn struct {
	At      time.Time
	Prompt  string
	Reply   string
	ReplyAt time.Time
}

// Thread is what one transcript says happened since some moment.
type Thread struct {
	SessionID string
	Cwd       string
	Title     string
	Turns     []Turn
}

// readThread collects the turns of a transcript that were still active after
// since. A turn opens at each prompt in the main conversation. Subagent
// traffic and the harness's own injected notes are left out.
func readThread(path string, since time.Time) (Thread, error) {
	f, err := os.Open(path)
	if err != nil {
		return Thread{}, err
	}
	defer f.Close()
	th := Thread{SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	var turns []Turn
	eachEntry(f, func(e entry) {
		if e.Cwd != "" {
			th.Cwd = e.Cwd
		}
		switch {
		case e.Type == "ai-title":
			th.Title = e.AiTitle
		case e.IsSidechain || e.IsMeta:
		case e.Type == "user":
			if t := e.text(); t != "" {
				turns = append(turns, Turn{At: e.Timestamp, Prompt: t})
			}
		case e.Type == "assistant" && len(turns) > 0:
			if t := e.text(); t != "" {
				last := &turns[len(turns)-1]
				last.Reply, last.ReplyAt = t, e.Timestamp
			}
		}
	})
	for _, t := range turns {
		if t.At.After(since) || t.ReplyAt.After(since) {
			th.Turns = append(th.Turns, t)
		}
	}
	return th, nil
}

// Threads returns every session under this home with activity after since,
// most recently active first. Only top-level transcripts are read: subagents
// keep theirs in subdirectories.
func (h Home) Threads(since time.Time) []Thread {
	files, _ := filepath.Glob(filepath.Join(string(h), "projects", "*", "*.jsonl"))
	var out []Thread
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil || !info.ModTime().After(since) {
			continue
		}
		if th, err := readThread(f, since); err == nil && len(th.Turns) > 0 {
			out = append(out, th)
		}
	}
	sortThreads(out)
	return out
}
