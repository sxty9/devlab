package comments

import (
	"errors"
	"testing"
	"time"

	"devlab/backend/internal/model"
)

// newTestStore builds a Store backed by a temp dir.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("DEVLAB_COMMENTS", t.TempDir())
	s, err := NewStore(nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// add is a small helper that stores one comment and returns its stamped id.
func add(t *testing.T, s *Store, repo, path, parent, author, body string) string {
	t.Helper()
	c, err := s.Add(repo, model.Comment{Path: path, ParentID: parent, Author: author, Body: body}, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("Add(%q): %v", body, err)
	}
	return c.ID
}

// count returns how many comments the repo holds on a path.
func count(t *testing.T, s *Store, repo, path string) int {
	t.Helper()
	list, err := s.List(repo, path)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return len(list)
}

// TestDeleteAuthorizeRejectWritesNothing pins the passive contract: the store does not decide WHO may
// delete — it asks the caller's authorize closure, and a rejection must abort before any write, so the
// comment (and its replies) survive untouched. This is the behaviour that used to be the store's own
// isAdmin/owner check; the policy now lives outside the pool while staying atomic with the delete.
func TestDeleteAuthorizeRejectWritesNothing(t *testing.T) {
	s := newTestStore(t)
	const repo, path = "demo", "vision/a.md"
	id := add(t, s, repo, path, "", "alice", "top")
	add(t, s, repo, path, id, "bob", "reply")

	denied := errors.New("nope")
	got := make([]model.Comment, 0, 1)
	err := s.Delete(repo, id, func(c model.Comment) error {
		got = append(got, c) // the store must hand the located target to the policy
		return denied
	})
	if !errors.Is(err, denied) {
		t.Fatalf("Delete: want the closure's error, got %v", err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].Author != "alice" {
		t.Fatalf("authorize saw %+v, want the target (id=%s author=alice)", got, id)
	}
	if n := count(t, s, repo, path); n != 2 {
		t.Fatalf("a rejected delete must write nothing: want 2 comments, got %d", n)
	}
}

// TestDeleteCascadesToReplies confirms a delete removes the whole subtree (the target and its
// transitive replies) — a structural storage operation the passive pool still owns.
func TestDeleteCascadesToReplies(t *testing.T) {
	s := newTestStore(t)
	const repo, path = "demo", "vision/a.md"
	root := add(t, s, repo, path, "", "alice", "root")
	child := add(t, s, repo, path, root, "bob", "child")
	add(t, s, repo, path, child, "carol", "grandchild")
	sibling := add(t, s, repo, path, "", "dave", "sibling") // unrelated thread, must survive

	if err := s.Delete(repo, root, func(model.Comment) error { return nil }); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err := s.List(repo, path)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != sibling {
		t.Fatalf("cascade should leave only the sibling, got %+v", list)
	}
}

// TestDeleteAbsentIsIdempotentAndSkipsAuthorize checks that deleting a missing id is a no-op that never
// consults the policy — there is nothing to authorize when there is nothing to delete.
func TestDeleteAbsentIsIdempotentAndSkipsAuthorize(t *testing.T) {
	s := newTestStore(t)
	const repo = "demo"
	called := false
	if err := s.Delete(repo, "does-not-exist", func(model.Comment) error { called = true; return nil }); err != nil {
		t.Fatalf("Delete absent: want nil, got %v", err)
	}
	if called {
		t.Fatal("authorize must not be called when the id is absent")
	}
}

// TestAddRejectsMissingParent pins the store's referential-integrity guard: a reply must point at an
// existing comment on the same path, or it is refused (nothing is written).
func TestAddRejectsMissingParent(t *testing.T) {
	s := newTestStore(t)
	const repo, path = "demo", "vision/a.md"
	if _, err := s.Add(repo, model.Comment{Path: path, ParentID: "ghost", Author: "alice", Body: "orphan"}, time.Unix(0, 0)); err == nil {
		t.Fatal("Add with a non-existent parent must fail")
	}
	if n := count(t, s, repo, path); n != 0 {
		t.Fatalf("a rejected Add must write nothing: got %d comments", n)
	}
}
