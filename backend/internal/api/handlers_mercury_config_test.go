package api

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"devlab/backend/internal/runs"
)

// The config endpoint is the central config surface for the service default budget: a fresh GET reports
// the value in force (the built-in three hours, never a blank), a PUT persists a new default, and an
// explicit "0" (no cap) is a valid, preserved choice while garbage is rejected.
func TestMercuryConfigEndpoint(t *testing.T) {
	t.Setenv("DEVLAB_MERCURY_SETTINGS", filepath.Join(t.TempDir(), "settings.json"))
	t.Setenv("DEVLAB_RUNS_AGENT_TIMEOUT", "") // isolate from any ambient operator override
	s := &Server{settings: runs.NewSettings()}

	get := func() runs.Config {
		rec := httptest.NewRecorder()
		s.mercuryConfigGet(rec, httptest.NewRequest("GET", "/api/mercury/config", nil))
		if rec.Code != 200 {
			t.Fatalf("GET status %d: %s", rec.Code, rec.Body.String())
		}
		var c runs.Config
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
			t.Fatalf("decode GET: %v", err)
		}
		return c
	}
	put := func(body string) int {
		req := httptest.NewRequest("PUT", "/api/mercury/config", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.mercuryConfigSet(rec, req)
		return rec.Code
	}

	if got := get().DefaultTimeBudget; got != "3h" {
		t.Fatalf("fresh GET default = %q, want built-in 3h", got)
	}
	if code := put(`{"defaultTimeBudget":"5h"}`); code != 200 {
		t.Fatalf("PUT 5h status %d", code)
	}
	if got := get().DefaultTimeBudget; got != "5h" {
		t.Fatalf("after PUT, GET default = %q, want 5h", got)
	}
	if code := put(`{"defaultTimeBudget":"whenever"}`); code != 400 {
		t.Fatalf("PUT invalid status %d, want 400", code)
	}
	if code := put(`{"defaultTimeBudget":"0"}`); code != 200 {
		t.Fatalf("PUT 0 status %d", code)
	}
	if got := get().DefaultTimeBudget; got != "0" {
		t.Fatalf("after PUT 0, GET default = %q, want preserved 0 (no cap)", got)
	}
}
