// The session journal, read in PORTIONS.
//
// An execution's agent conversation is an append-only journal (executions/<id>/transcript.jsonl,
// written line by line while the agent works). A viewer must never be handed the whole record
// since the beginning of time: it opens on the NEWEST portion, follows forward while the session
// grows, and fetches an older portion only when it is asked for. All three are the SAME read with
// a different window, so there is one reader and no second path to the same file.
//
// The reader is tolerant by design: a line that cannot be decoded is skipped without failing the
// read (a half-written last line at the very end of a live journal is normal), and a journal that
// does not exist yet is an empty portion, not an error — an execution that has said nothing is a
// defined state.
package runs

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"devlab/backend/internal/model"
)

// defaultSessionLines is how much one portion holds when the caller names no size.
const defaultSessionLines = 200

// maxSessionLines caps one portion, so no request can ask for the whole journal at once.
const maxSessionLines = 500

// backScanChunk is how much of the journal the backward walk reads at a time while counting
// line boundaries.
const backScanChunk = 64 << 10

// SessionWindow names WHICH portion of a session journal to read.
//
//	{Back: true}                  the newest portion — where a viewer opens
//	{At: slice.Next}              the portion after one already read — following a live session
//	{At: slice.From, Back: true}  the portion before one already read — loading older lines
type SessionWindow struct {
	// At is the byte offset the window is anchored at. Forward reads start here; backward reads
	// end here. Zero (or out of range) means the end of the journal for a backward read and its
	// start for a forward one.
	At int64
	// Back reads the portion BEFORE At instead of the one after it.
	Back bool
	// Max is the number of lines the portion holds at most; 0 takes the default, and anything
	// above the cap is reduced to it.
	Max int
}

// SessionSlice is one portion of a session journal together with the offsets that let the caller
// continue in either direction without re-reading what it already has.
type SessionSlice struct {
	Lines []model.SessionLine `json:"lines"`
	// From is the byte offset of the first returned line, Next the offset just after the last —
	// the anchors for the older portion and for the next follow-up read.
	From int64 `json:"from"`
	Next int64 `json:"next"`
	// Older reports that the journal continues BEFORE From, so a viewer knows whether there is
	// anything left to load.
	Older bool `json:"older"`
}

// sessionPath is the ONE place the journal's file name is formed — the append and every read
// derive their path from it, so they can never drift apart.
func (r *ResultStore) sessionPath(execID string) string {
	return filepath.Join(r.execDir, execID, "transcript.jsonl")
}

// ReadSession returns one portion of an execution's session journal.
func (r *ResultStore) ReadSession(execID string, w SessionWindow) (SessionSlice, error) {
	if execID == "" {
		return SessionSlice{}, errors.New("session without execution id")
	}
	max := w.Max
	if max <= 0 {
		max = defaultSessionLines
	}
	if max > maxSessionLines {
		max = maxSessionLines
	}

	f, err := os.Open(r.sessionPath(execID))
	if err != nil {
		if os.IsNotExist(err) {
			return SessionSlice{Lines: []model.SessionLine{}}, nil // nothing said yet
		}
		return SessionSlice{}, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return SessionSlice{}, err
	}
	size := fi.Size()

	// Both directions end up as ONE forward read between two line boundaries; only the way the
	// window is placed differs.
	from, until := clamp(w.At, size), size
	if w.Back {
		if w.At > 0 {
			until = clamp(w.At, size) // the portion BEFORE one already read
		}
		if from, err = lineStartBefore(f, until, max); err != nil {
			return SessionSlice{}, err
		}
	}
	return readForward(f, from, until, max)
}

// clamp keeps an offset inside the journal.
func clamp(at, size int64) int64 {
	if at < 0 {
		return 0
	}
	if at > size {
		return size
	}
	return at
}

// lineStartBefore walks BACKWARD from end and returns the offset at which `max` complete lines
// begin — the start of the newest (or of the requested older) portion. It never returns a torn
// offset: the result is always a line boundary or the start of the journal.
func lineStartBefore(f *os.File, end int64, max int) (int64, error) {
	if end <= 0 || max <= 0 {
		return 0, nil
	}
	// The line the window ends on has its own terminator just before `end`; that terminator does
	// not open a line and must not be counted.
	seen := 0
	pos := end
	buf := make([]byte, backScanChunk)
	for pos > 0 {
		n := int64(backScanChunk)
		if pos < n {
			n = pos
		}
		start := pos - n
		if _, err := f.ReadAt(buf[:n], start); err != nil && err != io.EOF {
			return 0, err
		}
		for i := int(n) - 1; i >= 0; i-- {
			if buf[i] != '\n' {
				continue
			}
			at := start + int64(i)
			if at == end-1 {
				continue // the terminator of the last line inside the window
			}
			seen++
			if seen >= max {
				return at + 1, nil
			}
		}
		pos = start
	}
	return 0, nil
}

// readForward reads at most max complete lines from `from` up to `until`. The upper bound is what
// keeps an older portion from spilling into the portion the caller already holds. A trailing line
// without its terminator — the tail of a journal being written right now — is left for the next
// read, so a half-written record is never shown and never skipped.
func readForward(f *os.File, from, until int64, max int) (SessionSlice, error) {
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return SessionSlice{}, err
	}
	out := SessionSlice{Lines: []model.SessionLine{}, From: from, Next: from, Older: from > 0}
	// ReadBytes, not a Scanner: one very large line (a big tool result) must not trip a token cap.
	br := bufio.NewReaderSize(f, backScanChunk)
	for len(out.Lines) < max && out.Next < until {
		raw, err := br.ReadBytes('\n')
		if len(raw) > 0 && raw[len(raw)-1] == '\n' {
			out.Next += int64(len(raw))
			if l, ok := decodeSessionLine(bytes.TrimSpace(raw)); ok {
				out.Lines = append(out.Lines, l)
			}
		}
		if err != nil {
			if err != io.EOF {
				return SessionSlice{}, err
			}
			break
		}
	}
	return out, nil
}

// decodeSessionLine parses one journal record. An undecodable line is dropped rather than failing
// the whole portion — and a record from before the journal carried its repository and its author
// simply arrives with those fields empty, which is exactly what it was.
func decodeSessionLine(raw []byte) (model.SessionLine, bool) {
	if len(raw) == 0 || raw[0] != '{' {
		return model.SessionLine{}, false
	}
	var l model.SessionLine
	if json.Unmarshal(raw, &l) != nil {
		return model.SessionLine{}, false
	}
	return l, true
}
