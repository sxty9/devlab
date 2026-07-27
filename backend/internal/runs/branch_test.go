package runs

import (
	"regexp"
	"strings"
	"testing"
)

// branchRe mirrors workspace.branchRe — the load-bearing validator every generated branch must pass, so
// the naming helpers are checked against the exact rule git creation enforces.
var branchRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func assertValidBranch(t *testing.T, b string) {
	t.Helper()
	if !branchRe.MatchString(b) {
		t.Fatalf("branch %q violates [A-Za-z0-9._/-]", b)
	}
	if strings.Contains(b, "..") {
		t.Fatalf("branch %q contains %q", b, "..")
	}
	for _, seg := range strings.Split(b, "/") {
		if seg == "" {
			t.Fatalf("branch %q has an empty segment", b)
		}
		if strings.HasPrefix(seg, "-") {
			t.Fatalf("branch %q segment %q starts with '-'", b, seg)
		}
	}
}

func TestBranchSlug(t *testing.T) {
	cases := map[string]string{
		"Dark Mode":                    "dark_mode",
		"comments store as a pool":     "comments_store_as_a_pool",
		"single-source-of-truth":       "single_source_of_truth",
		"  trailing / leading  ":       "trailing_leading",
		"Account-Löschung":             "account_loeschung",
		"Mehrsprachigkeit & Umlaute ü": "mehrsprachigkeit_umlaute_ue",
		"MiXeD CaSe 123":               "mixed_case_123",
		"!!!":                          "",
		"":                             "",
		"a":                            "a",
	}
	for in, want := range cases {
		if got := BranchSlug(in); got != want {
			t.Errorf("BranchSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBranchSlugTruncatesOnWordBoundary(t *testing.T) {
	long := "please fix the button that is broken on mobile devices sometimes when scrolling"
	got := BranchSlug(long)
	if len(got) > branchDescMax {
		t.Fatalf("slug %q longer than cap %d", got, branchDescMax)
	}
	if strings.HasSuffix(got, "_") || strings.HasPrefix(got, "_") {
		t.Fatalf("slug %q has a dangling separator", got)
	}
	if !strings.HasPrefix(got, "please_fix_the_button") {
		t.Fatalf("slug %q lost the leading words", got)
	}
}

func TestBranchDescGoodEnough(t *testing.T) {
	good := []string{"Dark Mode", "fix login", "a1b"}
	bad := []string{"", "!!!", "a", "x-", "ü"} // "ü" -> "ue" is 2 chars, still too short
	for _, n := range good {
		if !BranchDescGoodEnough(n) {
			t.Errorf("BranchDescGoodEnough(%q) = false, want true (slug %q)", n, BranchSlug(n))
		}
	}
	for _, n := range bad {
		if BranchDescGoodEnough(n) {
			t.Errorf("BranchDescGoodEnough(%q) = true, want false (slug %q)", n, BranchSlug(n))
		}
	}
}

func TestBranchKindFor(t *testing.T) {
	if BranchKindFor(true) != BranchKindFeature {
		t.Errorf("new service should be %q", BranchKindFeature)
	}
	if BranchKindFor(false) != BranchKindFix {
		t.Errorf("existing repo should be %q", BranchKindFix)
	}
}

func TestBranchName(t *testing.T) {
	cases := []struct {
		kind, desc, uniq, want string
	}{
		{BranchKindFeature, "Dark Mode", "k3f9a2", "feature/dark_mode-k3f9a2"},
		{BranchKindFix, "passive comment pool", "7bq1zx", "fix/passive_comment_pool-7bq1zx"},
		{BranchKindFix, "", "abc123", "fix/change-abc123"},        // empty desc -> "change"
		{BranchKindFix, "!!!", "abc123", "fix/change-abc123"},     // unslugifiable -> "change"
		{BranchKindFeature, "Dark Mode", "", "feature/dark_mode"}, // no token
	}
	for _, c := range cases {
		got := BranchName(c.kind, c.desc, c.uniq)
		if got != c.want {
			t.Errorf("BranchName(%q,%q,%q) = %q, want %q", c.kind, c.desc, c.uniq, got, c.want)
		}
		assertValidBranch(t, got)
	}
}

// A hostile run name must never escape the description segment or forge a ref.
func TestBranchNameSanitizesHostileInput(t *testing.T) {
	for _, evil := range []string{"../../etc", "a/../../b", "--force", "name;rm -rf", "a b\tc\nd"} {
		got := BranchName(BranchKindFix, evil, NewBranchToken())
		assertValidBranch(t, got)
		if !strings.HasPrefix(got, "fix/") {
			t.Fatalf("branch %q lost its kind prefix", got)
		}
	}
}

func TestNewBranchToken(t *testing.T) {
	seen := map[string]bool{}
	tokenRe := regexp.MustCompile(`^[a-z0-9]{6}$`)
	for i := 0; i < 200; i++ {
		tok := NewBranchToken()
		if !tokenRe.MatchString(tok) {
			t.Fatalf("token %q is not 6 lowercase base32 chars", tok)
		}
		if seen[tok] {
			t.Fatalf("token %q repeated within 200 draws", tok)
		}
		seen[tok] = true
	}
}
