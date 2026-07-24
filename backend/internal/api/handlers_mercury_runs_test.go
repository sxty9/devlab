package api

import (
	"net/http"
	"testing"

	"devlab/backend/internal/runs"
)

// normalizeTargets is the single gate a ToDo's destinations pass through: it must accept several
// existing/new repos, collapse duplicates, and reject a malformed or empty set.
func TestNormalizeTargets(t *testing.T) {
	ok := func(in []runs.Target, wantN int) {
		t.Helper()
		out, code, msg := normalizeTargets(in)
		if code != 0 {
			t.Fatalf("normalizeTargets(%v) rejected: %d %q", in, code, msg)
		}
		if len(out) != wantN {
			t.Fatalf("normalizeTargets(%v) len=%d want %d (%v)", in, len(out), wantN, out)
		}
	}
	bad := func(in []runs.Target) {
		t.Helper()
		if _, code, _ := normalizeTargets(in); code != http.StatusBadRequest {
			t.Fatalf("normalizeTargets(%v) should be rejected, got code %d", in, code)
		}
	}

	// Several targets, mixing an existing repo and a new one, trimmed.
	ok([]runs.Target{{Repo: "devlab"}, {NewRepo: "brand-new"}, {Repo: " icaly "}}, 3)
	// Duplicates (same existing repo, same new repo) collapse to one each.
	ok([]runs.Target{{Repo: "a"}, {Repo: "a"}, {NewRepo: "b"}, {NewRepo: "b"}}, 2)
	// A single target still works (parity with the old one-target ToDo).
	ok([]runs.Target{{Repo: "a"}}, 1)

	bad(nil)                                        // no target at all
	bad([]runs.Target{})                            // empty
	bad([]runs.Target{{}})                          // neither repo nor newRepo
	bad([]runs.Target{{Repo: "a", NewRepo: "b"}})   // both set on one target
	bad([]runs.Target{{NewRepo: "in valid/name"}})  // illegal new-repo name
	bad([]runs.Target{{Repo: "ok"}, {}})            // one good, one empty → whole set rejected
}
