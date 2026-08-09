package it

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"devlab/backend/internal/live"
	"devlab/backend/internal/model"
)

// REQ-040.4 — ONE vocabulary everywhere. The wire contract (package model) fixes each word once;
// the UI, the API, the stores, the log and the report must use THAT word and no synonym for it.
//
// Three checks make the statement complete:
//  1. mirror      every vocabulary literal of model exists verbatim in src/types.ts, and the
//     frontend invents none of its own.
//  2. synonyms    no file uses a word the contract has already fixed differently.
//  3. legacy      the retired step names of the old system exist ONLY on the read-only legacy
//     path — they are displayable, never produced.
func TestVocabularyMirrorsBetweenServerAndClient(t *testing.T) {
	frontend := readFile(t, filepath.Join("..", "..", "src", "types.ts"))

	// The vocabulary, taken from the constants themselves — a word added to model is covered here
	// without anyone remembering to extend this test.
	groups := map[string][]string{
		"Stage":       stringsOf(model.ChainStages()),
		"StepState":   {string(model.StepPending), string(model.StepRunning), string(model.StepExecuted), string(model.StepFailed), string(model.StepNotApplicable), string(model.StepNotExecuted)},
		"TaskState":   {string(model.TaskNotImplemented), string(model.TaskImplementedUndelivered), string(model.TaskDelivered), string(model.TaskUnknown)},
		"ExecPhase":   {string(model.PhaseCreated), string(model.PhaseQueued), string(model.PhaseRunning), string(model.PhasePaused), string(model.PhaseBlocked), string(model.PhaseInterrupted), string(model.PhaseCompleted), string(model.PhaseFailed), string(model.PhaseDiscarded)},
		"PauseReason": {string(model.PauseDeferredByUser), string(model.PauseUsageLimit)},
		"RunKind":     {string(model.KindAuto), string(model.KindTodo)},
		"LiveTopic":   stringsOf(live.Topics()),
	}

	for name, words := range groups {
		for _, w := range words {
			if !strings.Contains(frontend, "'"+w+"'") {
				t.Errorf("%s literal %q is missing from src/types.ts — the two sides do not share one vocabulary", name, w)
			}
		}
	}

	// The other direction: the frontend union types must not carry a word the server never sends.
	for _, union := range []struct {
		typeName string
		words    []string
	}{
		{"Stage", groups["Stage"]},
		{"StepState", groups["StepState"]},
		{"TaskState", groups["TaskState"]},
		{"ExecPhase", groups["ExecPhase"]},
		{"PauseReason", groups["PauseReason"]},
		{"RunKind", groups["RunKind"]},
	} {
		declared := unionMembers(t, frontend, union.typeName)
		if len(declared) == 0 {
			t.Errorf("src/types.ts declares no %s union at all", union.typeName)
			continue
		}
		known := map[string]bool{}
		for _, w := range union.words {
			known[w] = true
		}
		for _, d := range declared {
			if !known[d] {
				t.Errorf("src/types.ts %s carries %q, which the server never sends", union.typeName, d)
			}
		}
		if len(declared) != len(union.words) {
			t.Errorf("src/types.ts %s has %d members, the contract has %d (%v vs %v)",
				union.typeName, len(declared), len(union.words), declared, union.words)
		}
	}
}

// REQ-040.4, second half: no synonym for a word the contract already fixed.
func TestNoSynonymForAFixedWord(t *testing.T) {
	// canonical → the synonyms that must not appear as a wire value or a field name. Only clear-cut
	// cases: each left-hand word IS the contract's word for the thing on the right.
	synonyms := map[string][]string{
		string(model.PhasePaused):       {`"suspended"`, `json:"suspended"`, `'suspended'`},
		string(model.StepNotApplicable): {`"skipped"`, `json:"skipped"`, `'skipped'`},
		string(model.StepNotExecuted):   {`"unreached"`, `"omitted"`},
		string(model.PhaseInterrupted):  {`"crashed"`, `"stranded"`, `"orphaned"`},
		string(model.PhaseDiscarded):    {`"abandoned"`, `"dropped"`},
		string(model.StageDeliverDev):   {`"dev-deploy"`, `'dev-deploy'`},
		string(model.StagePullRequest):  {`"pr-stage"`, `"open-pr"`},
		string(model.TaskDelivered):     {`"shipped"`, `"released"`},
		string(model.PauseUsageLimit):   {`"rate-limit"`, `"quota"`},
		string(model.PhaseQueued):       {`"waiting"`, `"pending-start"`},
	}

	files := sourceFiles(t)
	for canonical, alts := range synonyms {
		for _, alt := range alts {
			for _, f := range files {
				if isLegacyPath(f) {
					continue // the read-only legacy bridge: see the retired-names test
				}
				for i, line := range strings.Split(readFile(t, f), "\n") {
					if !strings.Contains(line, alt) || readsAlongsideCanonical(line, canonical) {
						continue
					}
					t.Errorf("%s:%d uses %s where the contract says %q — one vocabulary means one word",
						rel(f), i+1, alt, canonical)
				}
			}
		}
	}
}

// readsAlongsideCanonical reports whether the line READS a retired word next to the word that
// replaced it (a tolerant reader, e.g. `case "deliver-dev", "dev-deploy":`). Producing the retired
// word alone is what the vocabulary rule forbids.
func readsAlongsideCanonical(line, canonical string) bool {
	return strings.Contains(line, `"`+canonical+`"`) || strings.Contains(line, `'`+canonical+`'`)
}

// REQ-027.3 / REQ-040.4: the retired step names of the old system are readable but never produced.
func TestRetiredStepNamesLiveOnlyOnTheLegacyPath(t *testing.T) {
	// Each retired name with the stage that replaced it: naming the retired one is allowed only on
	// the same line as its replacement (a tolerant reader over archived documents).
	retired := map[string]string{
		`"analyze"`:    string(model.StagePreflight),
		`"dev-deploy"`: string(model.StageDeliverDev),
		`"pr"`:         string(model.StagePullRequest),
	}
	for _, f := range sourceFiles(t) {
		if isLegacyPath(f) {
			continue
		}
		for i, line := range strings.Split(readFile(t, f), "\n") {
			for word, replacement := range retired {
				if strings.Contains(line, word) && !readsAlongsideCanonical(line, replacement) {
					t.Errorf("%s:%d produces the retired step name %s — the legacy names are display-only",
						rel(f), i+1, word)
				}
			}
		}
	}
}

// ── REQ-027.3, the reading half ──────────────────────────────────────────────────────────────
//
// The two checks above police PRODUCING a retired name. Neither says anything about READING one,
// and that is the gap a delivery blocker fell through: the delivery self-check compared stages
// STRICTLY against the new vocabulary over a result list that mixes in the archive, where the
// delivery stage is still called `dev-deploy`. No archived record ever matched, so every one of
// them read as "implemented, nothing delivered" and the first start raised a finding about a
// closed past. readsAlongsideCanonical could not see it: the file names no retired word at all —
// it simply never asks whether the document in front of it is an archived one.
//
// So the third check is about the COMPARISON, not the word: every place that compares a stage must
// say what it does about the retired vocabulary, and a place that says nothing is a failure.
//
// stageCompareRe matches the comparison itself in either spelling — `x.Stage == …`, `stage != …`,
// `case model.StageX:` — and deliberately not the CONSTRUCTION of a stage (`Stage: model.StageX`),
// which states a vocabulary rather than reading one.
var stageCompareRe = regexp.MustCompile(
	`\.Stage[[:space:]]*[=!]=|\bstage[[:space:]]*[=!]=|[=!]=[[:space:]]*model\.Stage[A-Z]|\bcase[[:space:]]+model\.Stage[A-Z]`)

// legacyAware names the two ways a comparing function may satisfy the rule. Either it CONSULTS the
// provenance (the Legacy flag the tolerant reader sets on every archived document), or it states in
// one marked line why no archived document can reach it. Both are explicit; silence is not.
const stageVocabularyMarker = "stage-vocabulary:"

// sourceText is a file as the audit sees it — path plus content, so the same checker can be run
// over the tree AND over a canary that is not in the tree.
type sourceText struct {
	path string
	src  string
}

// TestEveryStageComparisonHandlesTheLegacyVocabulary is that third check, plus a canary proving it
// has teeth: fed the pre-fix self-check, it must report exactly that file.
func TestEveryStageComparisonHandlesTheLegacyVocabulary(t *testing.T) {
	files := []sourceText{}
	for _, f := range sourceFiles(t) {
		files = append(files, sourceText{path: f, src: readFile(t, f)})
	}
	sites, faults := stageComparisons(files)
	if sites < 4 {
		t.Fatalf("only %d stage comparisons found — the check is not seeing the tree", sites)
	}
	for _, f := range faults {
		t.Error(f)
	}

	// The canary: the self-check EXACTLY as it stood when the blocker was raised — it compares
	// stages, names no retired word, and consults no provenance. The extended check must report it;
	// if it does not, the check would pass the very defect it was written for.
	canary := []sourceText{{
		path: filepath.Join("..", "internal", "report", "selfcheck.go"),
		src: `package report

// resultDelivered reports whether anything of an execution reached the user: a dev delivery that
// ran, or deliveries that have since been merged.
func resultDelivered(res runs.Result) bool {
	if res.MergedAt != nil {
		return true
	}
	return anyStageExecuted(res, model.StageDeliverDev)
}

func anyStageExecuted(res runs.Result, stage model.Stage) bool {
	for _, rp := range res.Repos {
		for _, sv := range rp.Stages {
			if sv.Stage == stage && sv.State == model.StepExecuted {
				return true
			}
		}
	}
	return false
}
`,
	}}
	canarySites, canaryFaults := stageComparisons(canary)
	if canarySites != 1 {
		t.Fatalf("the canary must present exactly one comparison, the checker saw %d", canarySites)
	}
	if len(canaryFaults) != 1 || !strings.Contains(canaryFaults[0], "selfcheck.go") {
		t.Fatalf("the canary must be reported as a fault naming selfcheck.go, got %v", canaryFaults)
	}
}

// stageComparisons counts the stage comparisons in the given sources and returns the ones whose
// enclosing function neither consults the archived provenance nor carries the marker. The legacy
// bridge itself is exempt: it IS the old path, and the retired-names test already fences it.
//
// Go only, and deliberately: the client never compares a CHAIN stage — it renders the server's
// stage array as it arrives (B-17), which is precisely why an archived record displays its retired
// names correctly. The one `.stage ===` in the frontend belongs to the delivery LIFECYCLE
// (open/merged/closed/reverted), a different vocabulary the archive does not touch. If a chain-stage
// comparison ever appears on the client, this scan is where it has to be extended.
func stageComparisons(files []sourceText) (sites int, faults []string) {
	for _, f := range files {
		if isLegacyPath(f.path) || !strings.HasSuffix(f.path, ".go") {
			continue
		}
		for _, fn := range goFuncs(f.src) {
			hits := []int{}
			for i, line := range fn.lines {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") || !stageCompareRe.MatchString(line) {
					continue
				}
				hits = append(hits, fn.start+i)
			}
			if len(hits) == 0 {
				continue
			}
			sites += len(hits)
			body := strings.Join(fn.lines, "\n")
			if strings.Contains(body, "Legacy") || strings.Contains(body, stageVocabularyMarker) {
				continue
			}
			for _, ln := range hits {
				faults = append(faults, fmt.Sprintf(
					"%s:%d compares a stage, and %s neither consults the archived provenance (Result.Legacy) "+
						"nor carries a `%s` line saying why no archived document reaches it — an archived "+
						"document carries the RETIRED stage names, so a strict comparison silently reads it as "+
						"\"this stage never ran\"", rel(f.path), ln, fn.name, stageVocabularyMarker))
			}
		}
	}
	return sites, faults
}

// goFunc is one top-level function with its doc comment: gofmt puts `func` at column 0 and its
// closing brace at column 0, which is all the boundary this audit needs.
type goFunc struct {
	name  string
	start int // 1-based line of the doc comment or the func keyword, whichever comes first
	lines []string
}

var funcNameRe = regexp.MustCompile(`^func(?:[[:space:]]+\([^)]*\))?[[:space:]]+([A-Za-z0-9_]+)`)

func goFuncs(src string) []goFunc {
	lines := strings.Split(src, "\n")
	out := []goFunc{}
	for i := 0; i < len(lines); i++ {
		m := funcNameRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		// Walk back over the doc comment so a reason stated ABOVE the function counts as stated.
		from := i
		for from > 0 && strings.HasPrefix(strings.TrimSpace(lines[from-1]), "//") {
			from--
		}
		end := i
		for end < len(lines) && lines[end] != "}" {
			end++
		}
		out = append(out, goFunc{name: m[1], start: from + 1, lines: lines[from:min(end+1, len(lines))]})
		i = end
	}
	return out
}

// REQ-030 / W-B: the contract's own shape must hold — five stages, four terminal states, and the
// success formula reading exactly "no failure and nothing skipped as not-executed".
func TestChainVocabularyIsClosed(t *testing.T) {
	if len(model.ChainStages()) != 5 {
		t.Fatalf("the chain has %d stages, the contract names 5", len(model.ChainStages()))
	}
	terminal := 0
	for _, s := range []model.StepState{
		model.StepPending, model.StepRunning, model.StepExecuted,
		model.StepFailed, model.StepNotApplicable, model.StepNotExecuted,
	} {
		if s.Terminal() {
			terminal++
		}
	}
	if terminal != 4 {
		t.Errorf("%d terminal step states, the contract names 4", terminal)
	}
	if len(live.Topics()) != 10 {
		t.Errorf("%d live topics, the contract names exactly 10", len(live.Topics()))
	}
	// The success formula, stated as a table so a change to it is visible here.
	for _, tc := range []struct {
		name   string
		states []model.StepState
		done   bool
		ok     bool
	}{
		{"all executed", []model.StepState{model.StepExecuted, model.StepExecuted}, true, true},
		{"a skip with evidence keeps the chain succeeding", []model.StepState{model.StepExecuted, model.StepNotApplicable}, true, true},
		{"a failure fails the repo", []model.StepState{model.StepFailed, model.StepNotExecuted}, true, false},
		{"an unreached stage fails the repo", []model.StepState{model.StepExecuted, model.StepNotExecuted}, true, false},
		{"a running stage is not done", []model.StepState{model.StepExecuted, model.StepRunning}, false, false},
		{"no stage at all is neither", nil, false, false},
	} {
		stages := make([]model.StageView, 0, len(tc.states))
		for i, st := range tc.states {
			stages = append(stages, model.StageView{Stage: model.ChainStages()[i], State: st})
		}
		done, ok := model.PipelineSucceeded(stages)
		if done != tc.done || ok != tc.ok {
			t.Errorf("%s: done=%v succeeded=%v, want done=%v succeeded=%v", tc.name, done, ok, tc.done, tc.ok)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────────────────────

func stringsOf[T ~string](in []T) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, string(v))
	}
	sort.Strings(out)
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var unionMemberRe = regexp.MustCompile(`'([^']+)'`)

// unionMembers reads the members of a TypeScript string-union type declaration.
func unionMembers(t *testing.T, src, typeName string) []string {
	t.Helper()
	marker := "export type " + typeName + " ="
	i := strings.Index(src, marker)
	if i < 0 {
		return nil
	}
	rest := src[i+len(marker):]
	// The declaration ends at the first semicolon.
	if j := strings.Index(rest, ";"); j >= 0 {
		rest = rest[:j]
	}
	out := []string{}
	for _, m := range unionMemberRe.FindAllStringSubmatch(rest, -1) {
		out = append(out, m[1])
	}
	sort.Strings(out)
	return out
}

// sourceFiles is the Go and TypeScript source of the rebuild (tests and fixtures excluded: a test
// legitimately names a retired word in order to prove it is only read).
func sourceFiles(t *testing.T) []string {
	t.Helper()
	roots := []string{filepath.Join("..", "internal"), filepath.Join("..", "cmd"), filepath.Join("..", "..", "src")}
	out := []string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			name := d.Name()
			switch {
			case strings.HasSuffix(name, "_test.go"), strings.HasSuffix(name, ".test.ts"), strings.HasSuffix(name, ".test.tsx"):
				return nil
			case strings.HasSuffix(name, ".go"), strings.HasSuffix(name, ".ts"), strings.HasSuffix(name, ".tsx"):
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) < 100 {
		t.Fatalf("only %d source files walked — the audit is not covering the tree", len(out))
	}
	return out
}

// isLegacyPath reports whether a file is the read-only legacy bridge, which is the ONE place a
// retired word may be named.
func isLegacyPath(path string) bool {
	base := filepath.Base(path)
	return base == "results.go" || strings.Contains(path, "legacy")
}

func rel(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if i := strings.Index(clean, "/backend/"); i >= 0 {
		return clean[i+1:]
	}
	return fmt.Sprint(clean)
}
