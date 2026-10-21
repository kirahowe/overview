package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

// Store is a review's log file. A Store holds no state beyond the path, so
// copies are interchangeable; the file lock keeps concurrent writers from
// interleaving.
type Store struct{ Path string }

var sessionID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// PathFor is the log's path for a session in dir.
func PathFor(dir, id string) (string, error) {
	if !sessionID.MatchString(id) {
		return "", fmt.Errorf("review: bad session id %q", id)
	}
	return filepath.Join(dir, id+".jsonl"), nil
}

// Load reads every event. A missing file is an empty log.
func (s Store) Load() ([]Event, error) {
	f, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH); err != nil {
		return nil, err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return ParseLog(b), nil
}

// Append writes the events to the log in a single write under an
// exclusive lock. If a crash left the file ending mid-line, a newline
// goes first so the new events do not join the torn one.
func (s Store) Append(es ...Event) error {
	if len(es) == 0 {
		return nil
	}
	var buf []byte
	for _, e := range es {
		b, err := encode(e)
		if err != nil {
			return err
		}
		buf = append(buf, b...)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if torn(f) {
		buf = append([]byte{'\n'}, buf...)
	}
	n, err := f.Write(buf)
	if err == nil && n < len(buf) {
		err = io.ErrShortWrite
	}
	return err
}

// torn says the file is not empty and does not end in a newline.
func torn(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return false
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], st.Size()-1); err != nil {
		return false
	}
	return last[0] != '\n'
}

// ParseLog reads one event per line. It skips lines that do not parse,
// and a final line with no newline, which is a write that was cut short.
func ParseLog(b []byte) []Event {
	var out []Event
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return out
		}
		line := bytes.TrimSpace(b[:i])
		b = b[i+1:]
		if len(line) == 0 {
			continue
		}
		var e Event
		if json.Unmarshal(line, &e) == nil {
			out = append(out, e)
		}
	}
}

func encode(e Event) ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Encode is the event as a log line, ending in a newline. It is nil for
// an event JSON cannot carry, such as a time outside years 0 to 9999.
func Encode(e Event) []byte {
	b, err := encode(e)
	if err != nil {
		return nil
	}
	return b
}

const idAlphabet = "abcdefghijklmnopqrstuvwxyz234567"

// NewID draws a five character id from rnd, retrying while it is taken.
func NewID(taken func(string) bool, rnd io.Reader) (string, error) {
	for range 1000 {
		var b [5]byte
		if _, err := io.ReadFull(rnd, b[:]); err != nil {
			return "", err
		}
		for i, c := range b {
			b[i] = idAlphabet[c%32]
		}
		if id := string(b[:]); taken == nil || !taken(id) {
			return id, nil
		}
	}
	return "", errors.New("review: no free note id")
}
