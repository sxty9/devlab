package api

import (
	"context"
	"os/exec"
	"path/filepath"

	"devlab/backend/internal/axiomrepo"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	authors := axiomauthors.NewStore(nil)
	authors.Mutate("ax_known", func(a axiomauthors.Author) axiomauthors.Author {
		a.CreatedBy, a.CreatedAt, a.UpdatedBy, a.UpdatedAt = "alice", time.Now().UTC(), "bob", time.Now().UTC()
		return a
	})
	// The constitution store is a real git repository — build a throwaway one holding the record this
	// test reads, rather than pretending the store away.
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, "", "git", "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed := filepath.Join(root, "seed")
	runGit(t, "", "git", "clone", "--quiet", remote, seed)
	runGit(t, seed, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "--allow-empty", "-m", "init")
	runGit(t, seed, "git", "push", "--quiet", "origin", "HEAD:main")
	axioms := axiomrepo.New(filepath.Join(root, "work"), remote, func() (string, error) { return "", nil })
	if err := axioms.Put(context.Background(), "axiome/x/y.md", rendered, "seed", "t", false); err != nil {
		t.Fatal(err)
	}
	s := &Server{axiomAuthors: authors, axioms: axioms}

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
	s.axiomAuthors = axiomauthors.NewStore(nil)
	if _, present := get()["author"]; present {
		t.Error("an axiom with no recorded author must not carry an author (shown as unknown)")
	}
}

// runGit runs one git command for the test fixture above.
func runGit(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, out)
	}
}
