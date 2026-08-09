package runs

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"devlab/backend/internal/model"
)

// journal writes n agent lines into one execution's session and returns the store.
func journal(t *testing.T, execID string, n int) *ResultStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", t.TempDir())
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", t.TempDir())
	r := NewResultStore(nil)
	for i := 0; i < n; i++ {
		line, _ := json.Marshal(model.SessionLine{At: time.Unix(int64(i), 0).UTC(), Repo: "svc-a", Text: fmt.Sprintf("line %d", i)})
		if err := r.AppendTranscript(execID, line); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func texts(lines []model.SessionLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

// A viewer opens on the NEWEST portion, not on the beginning of time (portioned data): asking for
// nothing gives the last lines, and says that there are earlier ones.
func TestASessionOpensOnItsNewestPortion(t *testing.T) {
	r := journal(t, "exec_1", 10)

	got, err := r.ReadSession("exec_1", SessionWindow{Back: true, Max: 3})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"line 7", "line 8", "line 9"}; fmt.Sprint(texts(got.Lines)) != fmt.Sprint(want) {
		t.Fatalf("opened on %v, want the newest portion %v", texts(got.Lines), want)
	}
	if !got.Older {
		t.Error("the portion does not say that earlier lines exist — the viewer would think it has everything")
	}
	if got.From == 0 {
		t.Error("the newest portion starts at the beginning of the journal — it is not portioned at all")
	}
}

// Following a live session reads ONLY what came since: the anchor of the last portion is where
// the next read starts, so nothing is re-read and nothing is skipped.
func TestFollowingReadsOnlyWhatCameSince(t *testing.T) {
	r := journal(t, "exec_1", 4)

	first, err := r.ReadSession("exec_1", SessionWindow{Back: true, Max: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing new yet: following from the anchor yields an empty portion, not a repetition.
	same, err := r.ReadSession("exec_1", SessionWindow{At: first.Next, Max: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(same.Lines) != 0 {
		t.Fatalf("following an unchanged session returned %v — the viewer would see every line twice", texts(same.Lines))
	}

	line, _ := json.Marshal(model.SessionLine{At: time.Unix(9, 0).UTC(), Repo: "svc-a", Text: "and now this"})
	if err := r.AppendTranscript("exec_1", line); err != nil {
		t.Fatal(err)
	}
	next, err := r.ReadSession("exec_1", SessionWindow{At: first.Next, Max: 10})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"and now this"}; fmt.Sprint(texts(next.Lines)) != fmt.Sprint(want) {
		t.Fatalf("the follow-up read gave %v, want exactly the new line %v", texts(next.Lines), want)
	}
}

// Earlier lines are fetched on demand and STOP where the portion already held begins — an older
// portion that overlapped would show the viewer the same lines twice.
func TestEarlierLinesAreFetchedWithoutOverlapping(t *testing.T) {
	r := journal(t, "exec_1", 10)

	newest, err := r.ReadSession("exec_1", SessionWindow{Back: true, Max: 3})
	if err != nil {
		t.Fatal(err)
	}
	older, err := r.ReadSession("exec_1", SessionWindow{At: newest.From, Back: true, Max: 3})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"line 4", "line 5", "line 6"}; fmt.Sprint(texts(older.Lines)) != fmt.Sprint(want) {
		t.Fatalf("the earlier portion is %v, want %v (it must end where the held one begins)", texts(older.Lines), want)
	}
	if older.Next != newest.From {
		t.Errorf("the earlier portion ends at %d but the held one begins at %d — there is a gap or an overlap", older.Next, newest.From)
	}
	if !older.Older {
		t.Error("there are still earlier lines, and the portion does not say so")
	}
}

// Walking back to the start says so, so a viewer can stop asking.
func TestTheStartOfASessionIsReachable(t *testing.T) {
	r := journal(t, "exec_1", 2)

	got, err := r.ReadSession("exec_1", SessionWindow{Back: true, Max: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got.From != 0 || got.Older {
		t.Fatalf("a fully-read session claims earlier lines (from=%d older=%v)", got.From, got.Older)
	}
}

// A session nobody has written to is an EMPTY portion, never an error: an execution that has not
// spoken yet is a defined state.
func TestASilentSessionIsAnEmptyPortion(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", t.TempDir())
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", t.TempDir())
	r := NewResultStore(nil)

	got, err := r.ReadSession("exec_nothing", SessionWindow{Back: true})
	if err != nil {
		t.Fatalf("a session with nothing in it must read as empty, not fail: %v", err)
	}
	if len(got.Lines) != 0 || got.Older {
		t.Fatalf("empty session read as %+v", got)
	}
}

// The reader is tolerant: a record from before the journal carried its repository and its author
// still reads (with those fields empty), and a half-written last line is left for the next read
// instead of being shown torn or swallowed.
func TestOldAndHalfWrittenRecordsAreReadHonestly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_EXECUTIONS", dir)
	t.Setenv("DEVLAB_MERCURY_RUNS_RESULTS", t.TempDir())
	r := NewResultStore(nil)

	// The shape the journal had before it carried a repository and an author.
	if err := r.AppendTranscript("exec_1", []byte(`{"at":"2026-07-28T03:00:00Z","text":"from the old format"}`)); err != nil {
		t.Fatal(err)
	}
	// A line being written right now: no terminator yet.
	f, err := os.OpenFile(r.sessionPath("exec_1"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"at":"2026-07-28T03:00:01Z","text":"half writ`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	got, err := r.ReadSession("exec_1", SessionWindow{Back: true, Max: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Text != "from the old format" {
		t.Fatalf("read %v, want only the one complete old-format line", texts(got.Lines))
	}
	if got.Lines[0].Repo != "" || got.Lines[0].From != "" {
		t.Error("an old record was given a repository or an author it never had")
	}

	// The torn tail was not counted as read, so the rest of it arrives once it is complete.
	rest, err := os.OpenFile(r.sessionPath("exec_1"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rest.WriteString(`ten"}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = rest.Close()

	after, err := r.ReadSession("exec_1", SessionWindow{At: got.Next, Max: 10})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"half written"}; fmt.Sprint(texts(after.Lines)) != fmt.Sprint(want) {
		t.Fatalf("the completed line read as %v, want %v", texts(after.Lines), want)
	}
}

// One portion is capped, so no single request can pull the whole record.
func TestAPortionIsCapped(t *testing.T) {
	r := journal(t, "exec_1", maxSessionLines+50)

	got, err := r.ReadSession("exec_1", SessionWindow{Back: true, Max: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != maxSessionLines {
		t.Fatalf("a portion held %d lines, the cap is %d", len(got.Lines), maxSessionLines)
	}
}
