package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// fixtureRepo is a minimal stateful GitHub for the protection round-trip: repo settings,
// branch protection, posted statuses.
type fixtureRepo struct {
	t           *testing.T
	patchedRepo map[string]any
	protection  map[string]any
	statuses    []map[string]any
	unprotected bool // GET protection answers 404 until a PUT arrives
	failProtect bool // PUT protection answers 403 (the "setting fails" case)
}

func (f *fixtureRepo) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/x":
			resp := map[string]any{"default_branch": "main"}
			if f.patchedRepo != nil {
				for k, v := range f.patchedRepo {
					resp[k] = v
				}
			} else {
				// GitHub's defaults: every merge method on — the deviation to fix.
				resp["allow_merge_commit"] = true
				resp["allow_squash_merge"] = true
				resp["allow_rebase_merge"] = true
			}
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/x":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.patchedRepo = body
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/x/branches/main/protection":
			if f.failProtect {
				http.Error(w, `{"message":"Resource not accessible"}`, http.StatusForbidden)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.protection = body
			f.unprotected = false
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/x/branches/main/protection":
			if f.unprotected || f.protection == nil {
				http.Error(w, `{"message":"Branch not protected"}`, http.StatusNotFound)
				return
			}
			rsc, _ := f.protection["required_status_checks"].(map[string]any)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"required_status_checks":        rsc,
				"required_pull_request_reviews": map[string]any{},
				"allow_force_pushes":            map[string]any{"enabled": false},
				"allow_deletions":               map[string]any{"enabled": false},
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/o/x/statuses/"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["sha"] = strings.TrimPrefix(r.URL.Path, "/repos/o/x/statuses/")
			f.statuses = append(f.statuses, body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			f.t.Errorf("unexpected fixture call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
}

// TestProtectDefaultBranch pins REQ-033.5/.6: ONE pass writes both halves — merge commits on,
// squash and rebase OFF, and the branch protection with PR requirement, the required origin
// status, no force-pushes, no deletions. GetProtection reads it back faithfully.
func TestProtectDefaultBranch(t *testing.T) {
	f := &fixtureRepo{t: t, unprotected: true}
	withFixture(t, f.handler())

	if err := ProtectDefaultBranch(context.Background(), "tok", "o/x", "devlab/delivery-origin"); err != nil {
		t.Fatalf("ProtectDefaultBranch: %v", err)
	}
	// Half 1: exactly one merge method.
	if f.patchedRepo == nil {
		t.Fatal("repo merge methods were not written")
	}
	if f.patchedRepo["allow_merge_commit"] != true || f.patchedRepo["allow_squash_merge"] != false || f.patchedRepo["allow_rebase_merge"] != false {
		t.Errorf("merge methods = %+v, want merge on, squash+rebase off", f.patchedRepo)
	}
	// Half 2: the protection payload.
	if f.protection == nil {
		t.Fatal("branch protection was not written")
	}
	rsc, _ := f.protection["required_status_checks"].(map[string]any)
	ctxs, _ := rsc["contexts"].([]any)
	if len(ctxs) != 1 || ctxs[0] != "devlab/delivery-origin" {
		t.Errorf("required status contexts = %v, want the origin context", ctxs)
	}
	if f.protection["allow_force_pushes"] != false || f.protection["allow_deletions"] != false {
		t.Errorf("history rewrite/deletion must be off: %+v", f.protection)
	}
	if _, hasPR := f.protection["required_pull_request_reviews"]; !hasPR {
		t.Error("the PR requirement is missing")
	}

	got, err := GetProtection(context.Background(), "tok", "o/x")
	if err != nil {
		t.Fatalf("GetProtection: %v", err)
	}
	if !got.RequirePR || len(got.RequiredStatus) != 1 || got.RequiredStatus[0] != "devlab/delivery-origin" {
		t.Errorf("read-back protection = %+v", got)
	}
	if got.AllowForcePush || got.AllowDeletion {
		t.Errorf("read-back must show no force-push/deletion: %+v", got)
	}
	if len(got.MergeMethods) != 1 || got.MergeMethods[0] != "merge" {
		t.Errorf("read-back merge methods = %v, want exactly [merge]", got.MergeMethods)
	}
}

// TestGetProtectionUnprotected: an unprotected branch is a zero state to restore, not an error.
func TestGetProtectionUnprotected(t *testing.T) {
	f := &fixtureRepo{t: t, unprotected: true}
	withFixture(t, f.handler())

	got, err := GetProtection(context.Background(), "tok", "o/x")
	if err != nil {
		t.Fatalf("GetProtection on unprotected: %v", err)
	}
	if got.RequirePR || len(got.RequiredStatus) != 0 {
		t.Errorf("unprotected branch must read as the zero branch half, got %+v", got)
	}
}

// TestProtectFailureSurfaces: a refused protection write is an ERROR — the caller treats it as
// the creation having failed (REQ-033.6), never a silent success.
func TestProtectFailureSurfaces(t *testing.T) {
	f := &fixtureRepo{t: t, unprotected: true, failProtect: true}
	withFixture(t, f.handler())
	if err := ProtectDefaultBranch(context.Background(), "tok", "o/x", "devlab/delivery-origin"); err == nil {
		t.Fatal("a refused protection write must surface as an error")
	}
}

// TestPostCommitStatus pins the status route, the payload and the 140-character cap.
func TestPostCommitStatus(t *testing.T) {
	f := &fixtureRepo{t: t}
	withFixture(t, f.handler())

	long := strings.Repeat("x", 200)
	if err := PostCommitStatus(context.Background(), "tok", "o/x", "cafe1", "devlab/delivery-origin", "failure", long); err != nil {
		t.Fatalf("PostCommitStatus: %v", err)
	}
	if len(f.statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(f.statuses))
	}
	st := f.statuses[0]
	if st["sha"] != "cafe1" || st["state"] != "failure" || st["context"] != "devlab/delivery-origin" {
		t.Errorf("status = %+v", st)
	}
	if desc, _ := st["description"].(string); len(desc) > 140 {
		t.Errorf("description length = %d, want ≤ 140", len(desc))
	}
}
