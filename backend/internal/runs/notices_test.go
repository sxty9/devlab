package runs

import (
	"path/filepath"
	"strconv"
	"testing"
)

func newTestNotices(t *testing.T) *NoticeStore {
	t.Helper()
	t.Setenv("DEVLAB_MERCURY_RUNS_NOTICES", filepath.Join(t.TempDir(), "notices.json"))
	return NewNoticeStore()
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

// Add stamps identity + time and never leaves a nil slice a reader could hit as JSON null.
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
	if n.AxiomIDs == nil || n.Axioms == nil {
		t.Errorf("nil slices must be normalized to empty, got ids=%v axioms=%v", n.AxiomIDs, n.Axioms)
	}
}

func TestNoticeStoreDismissAndClear(t *testing.T) {
	s := newTestNotices(t)
	_ = s.Add(Notice{Kind: NoticeFailed, Reason: "a"})
	_ = s.Add(Notice{Kind: NoticeFailed, Reason: "b"})
	_ = s.Add(Notice{Kind: NoticeFailed, Reason: "c"})
	list, _ := s.List()
	mid := list[1].ID
	if err := s.Dismiss(mid); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	if len(list) != 2 {
		t.Fatalf("after dismiss want 2, got %d", len(list))
	}
	for _, n := range list {
		if n.ID == mid {
			t.Errorf("dismissed notice %q still present", mid)
		}
	}
	if err := s.Dismiss(mid); err != nil { // idempotent
		t.Errorf("dismiss of unknown id must be a no-op, got %v", err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.List(); len(list) != 0 {
		t.Fatalf("after clear want 0, got %d", len(list))
	}
}
