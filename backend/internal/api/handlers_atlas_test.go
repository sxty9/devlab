package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"devlab/backend/internal/model"
)

// The ports endpoint serves the ledger derived from the observed host state (fixture routes +
// fixture socket table here) — including the named double-booking — and never a stored list.
func TestAtlasPortsHandlerFromFixtures(t *testing.T) {
	routes := t.TempDir()
	for id, port := range map[string]int{"aigentic": 8780, "prizm": 8780} {
		route := "handle /api/services/" + id + "/* {\n\treverse_proxy 127.0.0.1:" + strconv.Itoa(port) + "\n}\n"
		if err := os.WriteFile(filepath.Join(routes, id+".caddy"), []byte(route), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proc := filepath.Join(t.TempDir(), "tcp")
	rows := "  sl  local_address rem_address   st\n" +
		"   0: 0100007F:224C 00000000:0000 0A 0 0 0 0 0 0 0\n" // 0x224C = 8780 LISTEN
	if err := os.WriteFile(proc, []byte(rows), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEVLAB_CADDY_CONF", routes)
	t.Setenv("DEVLAB_PROC_NET_TCP", proc)
	t.Setenv("DEVLAB_PROC_NET_TCP6", proc)

	s := &Server{}
	rec := httptest.NewRecorder()
	s.atlasPorts(rec, httptest.NewRequest("GET", "/api/atlas/ports", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var allocs []model.PortAllocation
	if err := json.Unmarshal(rec.Body.Bytes(), &allocs); err != nil {
		t.Fatalf("bad JSON: %v: %s", err, rec.Body.String())
	}
	if len(allocs) != 2 {
		t.Fatalf("want the two conflicting holders, got %+v", allocs)
	}
	for _, a := range allocs {
		if a.Port != 8780 || !a.Conflict || !a.Routed || !a.Bound {
			t.Errorf("each holder of the double-booked port is named + flagged: %+v", a)
		}
	}
}

// An unreadable route source is an honest upstream error — never an empty green ledger.
func TestAtlasPortsHandlerHonestFailure(t *testing.T) {
	t.Setenv("DEVLAB_CADDY_CONF", filepath.Join(t.TempDir(), "absent"))
	s := &Server{}
	rec := httptest.NewRecorder()
	s.atlasPorts(rec, httptest.NewRequest("GET", "/api/atlas/ports", nil))
	if rec.Code != 502 {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "port ledger unavailable") {
		t.Errorf("the error should say what is unavailable: %s", rec.Body.String())
	}
}
