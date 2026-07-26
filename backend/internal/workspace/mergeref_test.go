package workspace

import "testing"

// TestMergeRefAllowlist pins the security allowlist MergeRef enforces before it hands a ref to git. It
// must accept the refs the runner legitimately folds into a base — Mercury run branches on origin, which
// now follow the human <kind>/<description> convention (plus the legacy prefix, still in flight) — and
// reject anything malformed or hostile. MergeRef also refuses any ref containing "..", tested here too.
func TestMergeRefAllowlist(t *testing.T) {
	allowed := []string{
		"origin/fix/passive_comment_pool-7bq1zx",
		"origin/feature/dark_mode-k3f9a2",
		"origin/mercury-run/run_abc/2026-07-26T17-02-42.652Z", // legacy, still foldable
		"origin/main",
	}
	rejected := []string{
		"fix/no-origin-prefix", // must be reachable via origin/
		"origin/-leading-dash",
		"origin/",
		"origin/../../etc/passwd",
		"origin/a/../../b",
		"origin/branch name with spaces",
		"origin/semi;colon",
		"../escape",
		"",
	}
	for _, ref := range allowed {
		if !mergeRefRe.MatchString(ref) {
			t.Errorf("ref %q should be allowed by mergeRefRe", ref)
		}
	}
	for _, ref := range rejected {
		// The guard is (contains "..") OR (!mergeRefRe): a ref is refused if either holds.
		refused := containsDotDot(ref) || !mergeRefRe.MatchString(ref)
		if !refused {
			t.Errorf("ref %q should be rejected (matched=%v, dotdot=%v)", ref, mergeRefRe.MatchString(ref), containsDotDot(ref))
		}
	}
}

func containsDotDot(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '.' && s[i+1] == '.' {
			return true
		}
	}
	return false
}
