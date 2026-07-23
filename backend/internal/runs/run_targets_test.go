package runs

import (
	"encoding/json"
	"testing"
)

// A ToDo now reaches several repos, but records written before that carried a single Repo/NewRepo.
// TodoTargets is the one bridge between the two shapes; both must resolve identically.
func TestTodoTargetsNormalizesLegacyAndMulti(t *testing.T) {
	cases := []struct {
		name string
		run  Run
		want []Target
	}{
		{"multi", Run{Type: TypeTodo, Targets: []Target{{Repo: "a"}, {NewRepo: "b"}}}, []Target{{Repo: "a"}, {NewRepo: "b"}}},
		{"legacy-existing", Run{Type: TypeTodo, Repo: "a"}, []Target{{Repo: "a"}}},
		{"legacy-new", Run{Type: TypeTodo, NewRepo: "b"}, []Target{{NewRepo: "b"}}},
		{"targets-win-over-legacy", Run{Type: TypeTodo, Repo: "old", Targets: []Target{{Repo: "a"}}}, []Target{{Repo: "a"}}},
		{"none", Run{Type: TypeTodo}, nil},
	}
	for _, c := range cases {
		got := c.run.TodoTargets()
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s[%d]: got %v, want %v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// A legacy record on disk (single repo, no targets array) must still round-trip and resolve.
func TestTodoTargetsFromLegacyJSON(t *testing.T) {
	var r Run
	if err := json.Unmarshal([]byte(`{"type":"todo","repo":"devlab"}`), &r); err != nil {
		t.Fatal(err)
	}
	ts := r.TodoTargets()
	if len(ts) != 1 || ts[0].Repo != "devlab" {
		t.Fatalf("legacy record did not fold into one target: %v", ts)
	}
}
