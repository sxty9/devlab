package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devlab/backend/internal/axiomauthors"
	"devlab/backend/internal/mercury"
)

// mercuryItem joins the local authorship pool onto an axiom by its stable id: a recorded author is
// handed to the client; an axiom with none carries no author (the surface shows unknown). This is the
// constitution half of "a record without an author appears as unknown, never guessed".
func TestMercuryItemSurfacesAuthorship(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEVLAB_MERCURY_AXIOM_AUTHORS", filepath.Join(dir, "axiom-authors.json"))

	// Serve one rendered axiom (id ax_known) from a stubbed aigentic graveyard.
	rendered := mercury.Render(mercury.Axiom{ID: "ax_known", Titel: "Single Source of Truth", Body: "Reuse the one access point."})
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/grave/get") {
			_ = json.NewEncoder(w).Encode(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(rendered))})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer stub.Close()
	t.Setenv("DEVLAB_AIGENTIC_URL", stub.URL)

	authors := axiomauthors.NewStore()
	authors.Mutate("ax_known", func(a axiomauthors.Author) axiomauthors.Author {
		a.CreatedBy, a.CreatedAt, a.UpdatedBy, a.UpdatedAt = "alice", time.Now().UTC(), "bob", time.Now().UTC()
		return a
	})
	s := &Server{axiomAuthors: authors}

	get := func() map[string]any {
		rec := httptest.NewRecorder()
		s.mercuryItem(rec, httptest.NewRequest(http.MethodGet, "/api/mercury/item?path=axiome/x/y.md", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	body := get()
	author, ok := body["author"].(map[string]any)
	if !ok {
		t.Fatalf("expected an author on the recorded axiom, got %v", body["author"])
	}
	if author["createdBy"] != "alice" || author["updatedBy"] != "bob" {
		t.Errorf("author must carry creator and editor: %v", author)
	}

	// An axiom with no recorded authorship carries no author key — the surface shows unknown. Point the
	// env at an empty pool BEFORE constructing the store (NewStore captures the path at construction).
	t.Setenv("DEVLAB_MERCURY_AXIOM_AUTHORS", filepath.Join(dir, "empty.json"))
	s.axiomAuthors = axiomauthors.NewStore()
	if _, present := get()["author"]; present {
		t.Error("an axiom with no recorded author must not carry an author (shown as unknown)")
	}
}
