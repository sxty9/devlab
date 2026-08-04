package deploy

// The four places that name the pinned root artifacts under /usr/local/sbin used to be kept in step by
// a COMMENT alone. That is not an assurance: when the daemon learned to guard (and offer for renewal)
// devlab-setup-lib.sh, the root renewal tool's RENEWABLE list stayed at four names — so a human could
// approve renewing the setup library and the root tool would refuse it, an approval that can do
// nothing. This test turns the comment into a check.
//
// The CANONICAL set is selfWrappers (this package). Three lists must name EXACTLY it, each with the
// install mode:
//   - selfWrappers                         (Go — the drift probe's set)
//   - RENEWABLE in deploy/devlab-install   (what the root renewal tool will act on)
//   - WRAPPERS in the `service` CLI        (what setup installs / uninstall removes)
// and the sudo pins in deploy/devlab.sudoers + deploy/devlab-runs.sudoers must be a SUBSET of it (the
// wrappers invoked via sudo — a grant may never name a wrapper the drift probe does not track). If any
// of these diverges, the test fails and names the difference.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// expectedWrapperMode is the mode each renewable artifact is installed with, keyed by name. It is the
// correctness anchor the three lists are held to: the executable wrappers are 0755, and the SOURCED
// setup library is 0644 (read-only — installing it executable, or the wrappers non-executable, is the
// drift this pins down). Every name in selfWrappers must appear here.
var expectedWrapperMode = map[string]string{
	"devlab-exec":              "0755",
	"devlab-install":           "0755",
	"devlab-mkworkspace":       "0755",
	"devlab-restart-when-free": "0755",
	"devlab-setup-lib.sh":      "0644",
}

func readDeployFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// parseNameModeTokens pulls every `name:mode` token out of a blob (the RENEWABLE string or the service
// WRAPPERS array body). Modes are 3–4 octal digits; names are the wrapper grammar plus an optional
// `.sh` for the sourced library.
func parseNameModeTokens(blob string) map[string]string {
	re := regexp.MustCompile(`([a-z][a-z0-9-]*(?:\.sh)?):([0-7]{3,4})`)
	out := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(blob, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// renewableFromInstall extracts RENEWABLE="…" from deploy/devlab-install and parses its name:mode set.
func renewableFromInstall(t *testing.T) map[string]string {
	t.Helper()
	src := readDeployFile(t, "deploy/devlab-install")
	m := regexp.MustCompile(`(?m)^RENEWABLE="([^"]*)"`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("deploy/devlab-install has no RENEWABLE=\"…\" line")
	}
	got := parseNameModeTokens(m[1])
	if len(got) == 0 {
		t.Fatalf("RENEWABLE parsed to an empty set from %q", m[1])
	}
	return got
}

// serviceWrappersFromCLI extracts the WRAPPERS=( … ) array body from the `service` CLI and parses its
// name:mode set. The array has no nested parens, so the first ')' after 'WRAPPERS=(' closes it.
func serviceWrappersFromCLI(t *testing.T) map[string]string {
	t.Helper()
	src := readDeployFile(t, "service")
	i := strings.Index(src, "WRAPPERS=(")
	if i < 0 {
		t.Fatal("the service CLI has no WRAPPERS=( … ) array")
	}
	rest := src[i:]
	end := strings.Index(rest, ")")
	if end < 0 {
		t.Fatal("the service CLI's WRAPPERS=( … ) array is not closed")
	}
	got := parseNameModeTokens(rest[:end])
	if len(got) == 0 {
		t.Fatalf("the service WRAPPERS array parsed to an empty set:\n%s", rest[:end])
	}
	return got
}

// sudoersPins collects every /usr/local/sbin/<name> referenced across the two sudoers files.
func sudoersPins(t *testing.T) map[string]bool {
	t.Helper()
	re := regexp.MustCompile(`/usr/local/sbin/([a-z][a-z0-9-]*(?:\.sh)?)`)
	pins := map[string]bool{}
	for _, f := range []string{"deploy/devlab.sudoers", "deploy/devlab-runs.sudoers"} {
		for _, m := range re.FindAllStringSubmatch(readDeployFile(t, f), -1) {
			pins[m[1]] = true
		}
	}
	if len(pins) == 0 {
		t.Fatal("no /usr/local/sbin pins found in the sudoers files")
	}
	return pins
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// TestRenewableWrapperListsAgree is task point (b): the drift-tracked set (selfWrappers), the root
// tool's RENEWABLE list and the `service` CLI's WRAPPERS array must name the SAME set of artifacts with
// the SAME install modes, and every sudo pin must be one of them. A divergence — the exact gap that let
// the daemon guard devlab-setup-lib.sh while the root tool refused to renew it — fails the test by name.
func TestRenewableWrapperListsAgree(t *testing.T) {
	// The canonical set: selfWrappers, with the modes this test anchors.
	canonical := map[string]string{}
	for _, name := range selfWrappers {
		mode, ok := expectedWrapperMode[name]
		if !ok {
			t.Fatalf("selfWrappers carries %q but expectedWrapperMode does not — add its install mode here", name)
		}
		canonical[name] = mode
	}
	for name := range expectedWrapperMode {
		if _, ok := canonical[name]; !ok {
			t.Errorf("expectedWrapperMode names %q but selfWrappers does not — the anchor and the drift set disagree", name)
		}
	}

	renewable := renewableFromInstall(t)
	service := serviceWrappersFromCLI(t)

	// The two shell lists must equal the canonical set, name for name and mode for mode.
	for label, got := range map[string]map[string]string{
		"RENEWABLE (deploy/devlab-install)": renewable,
		"WRAPPERS (service CLI)":            service,
	} {
		if !sameNameModeSet(canonical, got) {
			t.Errorf("%s diverges from selfWrappers.\n  selfWrappers: %s\n  %s: %s",
				label, fmtSet(canonical), label, fmtSet(got))
		}
	}

	// devlab-setup-lib.sh is the whole point: it must be PRESENT in every list and installed read-only
	// (0644), never as an executable. A regression here is exactly defect #1 returning.
	const lib = "devlab-setup-lib.sh"
	for label, got := range map[string]map[string]string{
		"RENEWABLE": renewable, "service WRAPPERS": service,
	} {
		if got[lib] != "0644" {
			t.Errorf("%s must renew/install %s at mode 0644 (sourced, read-only), got %q", label, lib, got[lib])
		}
	}

	// The sudo pins are a SUBSET of the canonical set — a grant may never name an untracked wrapper.
	for pin := range sudoersPins(t) {
		if _, ok := canonical[pin]; !ok {
			t.Errorf("sudoers pins /usr/local/sbin/%s, which is not a tracked root wrapper (%s) — a sudo grant must not name a wrapper the drift probe cannot see",
				pin, strings.Join(sortedKeys(canonical), ", "))
		}
	}
}

func sameNameModeSet(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func fmtSet(m map[string]string) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, k+":"+m[k])
	}
	return strings.Join(parts, " ")
}
