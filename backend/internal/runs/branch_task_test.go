package runs

import "testing"

// One task, one branch — stable across firings, distinct between tasks. A name that changed per
// firing would open a parallel branch beside the task's own work; a name shared between tasks would
// bring back the very ambiguity the shared branch had.
func TestTaskBranchIsStablePerRunAndDistinctBetweenRuns(t *testing.T) {
	a1 := TaskBranch(false, "upload files", "run_abc")
	a2 := TaskBranch(false, "upload files", "run_abc")
	if a1 != a2 {
		t.Fatalf("not stable across firings: %q vs %q", a1, a2)
	}
	if b := TaskBranch(false, "upload files", "run_xyz"); b == a1 {
		t.Fatalf("two tasks share one branch: %q", b)
	}
	if !contains(a1, "run_abc") {
		t.Fatalf("the branch does not carry its owner: %q", a1)
	}
	if c := TaskBranch(true, "new service", "run_abc"); c[:len(BranchKindFeature)] != BranchKindFeature {
		t.Fatalf("a created service is not a feature branch: %q", c)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
