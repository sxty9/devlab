package runs

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func newTestNotices(t *testing.T) *NoticeStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(t.TempDir(), "notices.json"))
	return NewNoticeStore(nil)
}

// The feed stays portioned: only the newest noticeCap notices survive, newest first.
func TestNoticeStoreCapAndOrder(t *testing.T) {
	s := newTestNotices(t)
	for i := 0; i < noticeCap+5; i++ {
		if err := s.Add(Notice{Kind: NoticeFailed, Reason: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != noticeCap {
		t.Fatalf("want %d notices (capped), got %d", noticeCap, len(list))
	}
	if list[0].Reason != strconv.Itoa(noticeCap+4) {
		t.Errorf("newest first: front is %q, want %q", list[0].Reason, strconv.Itoa(noticeCap+4))
	}
	if list[len(list)-1].Reason != strconv.Itoa(5) {
		t.Errorf("oldest surviving is %q, want %q (0..4 dropped)", list[len(list)-1].Reason, "5")
	}
}

// Add stamps identity, a count of one and a period, and never leaves a nil slice a reader could
// hit as JSON null.
func TestNoticeStoreAddNormalizes(t *testing.T) {
	s := newTestNotices(t)
	if err := s.Add(Notice{Kind: NoticeAssigned, RunName: "X"}); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Fatalf("want 1, got %d", len(list))
	}
	n := list[0]
	if n.ID == "" || n.At.IsZero() {
		t.Errorf("Add must stamp id+time, got id=%q at=%v", n.ID, n.At)
	}
	if n.Count != 1 || n.FirstAt.IsZero() || n.LastAt.IsZero() {
		t.Errorf("a first occurrence is count=1 with a period, got %+v", n)
	}
	if n.Read {
		t.Error("a fresh notice is unread")
	}
	if n.AxiomIDs == nil || n.Axioms == nil {
		t.Errorf("nil slices must be normalized to empty, got ids=%v axioms=%v", n.AxiomIDs, n.Axioms)
	}
}

// REQ-032.5: an unchanged repetition is BUNDLED — one record carrying the count and the period,
// instead of twenty identical rows. A changed message is a different finding and gets its own
// record.
func TestNoticeStoreCoalescesUnchangedRepeats(t *testing.T) {
	s := newTestNotices(t)
	first := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)

	for i := 0; i < 20; i++ {
		at := first.Add(time.Duration(i) * 20 * time.Second)
		if _, err := s.Coalesce(Notice{
			Kind: "delivery-blocked", Repo: "o/x", Text: "pull request #7 is blocked: 502 from origin",
			NextStep: "resume the delivery", LastAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A DIFFERENT reason for the same repo is a separate finding.
	if _, err := s.Coalesce(Notice{
		Kind: "delivery-blocked", Repo: "o/x", Text: "pull request #7 is blocked: 403 from origin",
		LastAt: first.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	list, _ := s.List()
	if len(list) != 2 {
		t.Fatalf("want 2 records (one bundled + one distinct), got %d: %+v", len(list), list)
	}
	var bundled Notice
	for _, n := range list {
		if n.Count > 1 {
			bundled = n
		}
	}
	if bundled.Count != 20 {
		t.Errorf("count = %d, want 20", bundled.Count)
	}
	if !bundled.FirstAt.Equal(first) {
		t.Errorf("firstAt = %v, want %v", bundled.FirstAt, first)
	}
	if want := first.Add(19 * 20 * time.Second); !bundled.LastAt.Equal(want) {
		t.Errorf("lastAt = %v, want %v", bundled.LastAt, want)
	}
	if bundled.At != bundled.FirstAt {
		t.Errorf("at (%v) must stay the first occurrence (%v)", bundled.At, bundled.FirstAt)
	}
}

// REQ-031.2: the hint stays until it is read — it survives a fresh store handle over the same
// file (a restart), a dismissal marks it read, and a NEW occurrence of the same finding re-opens
// it so a problem that is still happening is still seen.
func TestNoticeStoreStaysUntilRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notices.json")
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", path)
	s := NewNoticeStore(nil)

	stored, err := s.Coalesce(Notice{
		Kind: "delivery-alarm", Repo: "o/x", Text: "implemented without dev delivery",
		NextStep: "fix the delivery path and resume the execution",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A restart: brand-new handle, same file — the hint is still there and still unread.
	restarted := NewNoticeStore(nil)
	list, _ := restarted.List()
	if len(list) != 1 || list[0].Read || list[0].ID != stored.ID {
		t.Fatalf("the hint must survive unread, got %+v", list)
	}

	if err := restarted.Dismiss(stored.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = restarted.List()
	if len(list) != 1 || !list[0].Read {
		t.Fatalf("dismiss must mark the record read and keep it, got %+v", list)
	}

	// The same finding happens again: the record re-opens and keeps its history.
	again, err := restarted.Coalesce(Notice{
		Kind: "delivery-alarm", Repo: "o/x", Text: "implemented without dev delivery",
		NextStep: "fix the delivery path and resume the execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.Read || again.Count != 2 || again.ID != stored.ID {
		t.Errorf("a fresh occurrence must re-open the same record, got %+v", again)
	}
}

// Message is the ONE way to read a notice's wording, whichever field the raising package filled.
func TestNoticeMessagePrefersTextFallsBackToReason(t *testing.T) {
	if got := (Notice{Text: "the finding", Reason: "older wording"}).Message(); got != "the finding" {
		t.Errorf("Message() = %q, want the text", got)
	}
	if got := (Notice{Reason: "older wording"}).Message(); got != "older wording" {
		t.Errorf("Message() = %q, want the reason", got)
	}
}

func TestNoticeStoreDismissAndClear(t *testing.T) {
	s := newTestNotices(t)
	_ = s.Add(Notice{Kind: NoticeFailed, Reason: "a"})
	_ = s.Add(Notice{Kind: NoticeFailed, Reason: "b"})
	_ = s.Add(Notice{Kind: NoticeFailed, Reason: "c"})
	list, _ := s.List()
	if len(list) != 3 {
		t.Fatalf("three distinct reasons are three records, got %d", len(list))
	}
	mid := list[1].ID
	if err := s.Dismiss(mid); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	unread := 0
	for _, n := range list {
		if !n.Read {
			unread++
		}
		if n.ID == mid && !n.Read {
			t.Errorf("dismissed notice %q is still unread", mid)
		}
	}
	if unread != 2 {
		t.Errorf("after one dismissal 2 of 3 stay unread, got %d", unread)
	}
	if err := s.Dismiss("ntc_unknown"); err != nil { // idempotent
		t.Errorf("dismiss of unknown id must be a no-op, got %v", err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.List(); len(list) != 0 {
		t.Fatalf("after clear want 0, got %d", len(list))
	}
}
