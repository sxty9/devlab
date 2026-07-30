package it

// The cutover runbook and the inspection document are DELIVERABLES: an operator runs the one and
// reports the acceptance out of the other. A claim in a document is worth no more than a claim in the
// code, so the claims that can be measured are measured here — against the shipped sources, never
// against a second copy of the same knowledge kept in this file.
//
// Every test names the defect it would have caught. All of them failed before repair wave 3: the
// backup did not carry the two state directories the cutover destroys, both sudoers files were
// installed before either was validated, the file the install wrapper requires was never provisioned,
// the health probe named a port instead of the configured address, `sudo $EDITOR` would have EXECUTED
// the drop-in, the drop-in was never treated line by line, and the acceptance row was bound to a wave
// name rather than to a commit.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	cutoverDoc    = "../../deploy/migration/00-cutover.md"
	inspectionDoc = "../../deploy/migration/20-sichtpruefung.md"
	unitTemplate  = "../../deploy/devlabd.service"
	installScript = "../../deploy/devlab-install"
	statepathSrc  = "../internal/statepath/statepath.go"
)

func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return string(b)
}

// ── the backup is complete (Befund: step 4 destroys what step 0 did not save) ──

// Every DIRECTORY the shipped state layout puts under the state root must be in the step-0 backup,
// workspaces excepted (git working trees, re-clonable at will). The set is DERIVED from
// internal/statepath, so a state directory added tomorrow is demanded here without anybody
// remembering to: what step 4's `rsync --delete` and the migration touch must be recoverable.
func TestCutoverBackupCoversEveryStateDirectory(t *testing.T) {
	src := readDoc(t, statepathSrc)
	// Only the ACCESSORS — a location the layout declares. The transient probe of CheckWritable joins
	// the same root and is nobody's state.
	joins := regexp.MustCompile(`func \(p \*Paths\) \w+\(\) string \{ return filepath\.Join\(p\.Root, "([^"]+)"\)`).
		FindAllStringSubmatch(src, -1)
	if len(joins) < 5 {
		t.Fatalf("statepath declares only %d children of the state root — the derivation would demand nothing", len(joins))
	}

	doc := readDoc(t, cutoverDoc)
	backup := lineWith(t, doc, "devlab-state-")
	if !strings.Contains(backup, "tar") {
		t.Fatalf("the backup step does not archive anything: %q", backup)
	}
	// The tar invocation spans several lines (the member list follows the -czf argument).
	backupBlock := blockAfter(doc, "# 0)", "# 1)")

	for _, m := range joins {
		name := m[1]
		if strings.Contains(name, ".") || name == "workspaces" {
			continue // a socket or a probe file, and the re-clonable working trees
		}
		if !regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(name) + `\b`).MatchString(backupBlock) {
			t.Errorf("the step-0 backup does not save %q, which statepath puts under the state root — the rollback of step 4/5 would be incomplete", name)
		}
	}

	// And the rollback must actually restore the two the cutover destroys.
	rollback := doc[strings.Index(doc, "## Rollback"):]
	for _, want := range []string{"www", "axioms", "tar"} {
		if !strings.Contains(rollback, want) {
			t.Errorf("the rollback never names %q — the way back is not written down", want)
		}
	}
}

// ── sudoers: validate each file BEFORE installing it ──────────────────────────

// A broken file in /etc/sudoers.d makes sudo unusable MACHINE-WIDE — for the neighbouring services and
// for the rollback itself. So each file is validated on its own (`visudo -c -f <file>`) before it is
// installed, not both afterwards.
func TestCutoverValidatesEachSudoersFileBeforeInstallingIt(t *testing.T) {
	doc := readDoc(t, cutoverDoc)
	for _, f := range []string{"deploy/devlab.sudoers", "deploy/devlab-runs.sudoers"} {
		check := strings.Index(doc, "visudo -c -f "+f)
		if check < 0 {
			t.Errorf("%s is installed without being validated first (`visudo -c -f %s` missing)", f, f)
			continue
		}
		install := indexOfLineWithAll(doc, "install", f, "/etc/sudoers.d/")
		if install < 0 {
			t.Errorf("%s is never installed — the step no longer does what it says", f)
			continue
		}
		if check > install {
			t.Errorf("%s is validated AFTER it is installed — a bad file has already taken sudo down by then", f)
		}
	}
}

// ── the operator-provisioned files the wrappers require ───────────────────────

// The install wrapper reads the managed organisation from a root-owned file and fails closed without it
// (exit 5), so the cutover must create it. The path is DERIVED from the wrapper's own default, so a
// renamed file cannot make this test pass on a path nobody uses.
func TestCutoverProvisionsTheManagedOrganisationFile(t *testing.T) {
	wrapper := readDoc(t, installScript)
	m := regexp.MustCompile(`OWNER_FILE="\$\{DEVLAB_GH_OWNER_FILE:-([^}"]+)\}"`).FindStringSubmatch(wrapper)
	if m == nil {
		t.Fatal("devlab-install no longer declares its organisation file — the derivation has nothing to check")
	}
	ownerFile := m[1]

	doc := readDoc(t, cutoverDoc)
	if !strings.Contains(doc, ownerFile) {
		t.Fatalf("the cutover never provisions %s, which devlab-install requires — every foreign delivery would fail closed", ownerFile)
	}
	if !indexOfLineWithAllOK(doc, ownerFile, "tee") && !indexOfLineWithAllOK(doc, ownerFile, "install") {
		t.Errorf("%s is mentioned but never written — a named requirement without the step that satisfies it", ownerFile)
	}
}

// ── the health probe reads the configured address, not a remembered port ──────

// Ports are runtime configuration (REQ-044). A port written into the runbook probes the wrong socket
// after the first reconfiguration — and reads as green when it happens to match.
func TestCutoverDerivesTheHealthProbeFromTheConfiguration(t *testing.T) {
	unit := readDoc(t, unitTemplate)
	m := regexp.MustCompile(`(?m)^Environment=DEVLAB_ADDR=\S*?:(\d+)`).FindStringSubmatch(unit)
	if m == nil {
		t.Fatal("the unit template no longer carries a default listen address — the derivation has nothing to check")
	}
	port := m[1]

	doc := readDoc(t, cutoverDoc)
	if strings.Contains(doc, port) {
		t.Errorf("the cutover names the port %s literally; it must read DEVLAB_ADDR from the running configuration instead", port)
	}
	if !strings.Contains(doc, "DEVLAB_ADDR=") {
		t.Error("the cutover never reads DEVLAB_ADDR — there is nothing the health probe could be derived from")
	}
	if !indexOfLineWithAllOK(doc, "curl", "$addr") {
		t.Error("the health probe does not use the derived address")
	}
}

// `sudo $EDITOR <file>` runs the FILE as a command when EDITOR is unset. sudoedit is the one form that
// cannot: it edits a copy as the calling user and puts it back as root.
func TestCutoverEditsPrivilegedFilesWithSudoedit(t *testing.T) {
	doc := readDoc(t, cutoverDoc)
	// Commands only: the runbook is allowed to EXPLAIN the form it refuses to use.
	bad := regexp.MustCompile(`sudo \$\{?(EDITOR|VISUAL)`)
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "|") || strings.HasPrefix(trimmed, "_") {
			continue
		}
		if bad.MatchString(trimmed) {
			t.Errorf("the cutover runs `sudo $EDITOR …` — with an empty EDITOR sudo EXECUTES the file it was meant to open: %q", trimmed)
		}
	}
	if !strings.Contains(doc, "sudoedit ") {
		t.Error("no sudoedit in the cutover — a privileged file is edited some other way")
	}
}

// ── the drop-in, line by line ─────────────────────────────────────────────────

// A drop-in WINS against the template, so every line left in it is a decision. The runbook's table is
// therefore held to the shipped code: a variable it tells the operator to keep must be one the code
// reads, and a variable it retires must be read by nothing — a retired name left behind does not fail,
// it silently does nothing. The renames are checked in both directions at once.
func TestCutoverDropInTableAgreesWithTheShippedEnvironmentContract(t *testing.T) {
	doc := readDoc(t, cutoverDoc)
	table := blockAfter(doc, "## Das Alt-Drop-in", "`DEVLAB_STATE_DIR`")
	if table == "" {
		t.Fatal("the cutover carries no line-by-line treatment of the drop-in")
	}
	read := shippedEnvNames(t)
	unit := readDoc(t, unitTemplate)

	rows := 0
	retired, kept := 0, 0
	for _, line := range strings.Split(table, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "| `") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cells[1]), "`")
		verdict := cells[2]
		if !strings.HasPrefix(name, "DEVLAB_") {
			// The one non-environment line: a unit directive. It must be named in the unit template
			// too, because that is where the decision it overrides is taken.
			if !strings.Contains(unit, name) {
				t.Errorf("the table treats %q, which the unit template never mentions — the decision it overrides is undocumented", name)
			}
			rows++
			continue
		}
		rows++

		switch {
		case strings.Contains(verdict, "entfernen"):
			retired++
			if read[name] {
				t.Errorf("the table retires %s, but the shipped code still reads it", name)
			}
			if !strings.Contains(unit, name) {
				t.Errorf("%s is retired by the runbook but carries no note in the unit template's environment contract", name)
			}
		case strings.Contains(verdict, "umbenennen"):
			retired++
			if read[name] {
				t.Errorf("the table renames %s away, but the shipped code still reads the old name", name)
			}
			if !strings.Contains(unit, name) {
				t.Errorf("%s is renamed by the runbook but carries no note in the unit template's environment contract", name)
			}
			nm := regexp.MustCompile(`→ ` + "`" + `(DEVLAB_[A-Z_]+)` + "`").FindStringSubmatch(verdict)
			if nm == nil {
				t.Errorf("%s is marked renamed without naming the new variable", name)
				continue
			}
			if !read[nm[1]] {
				t.Errorf("the table renames %s to %s, which the shipped code does not read either", name, nm[1])
			}
		case strings.Contains(verdict, "bleibt"), strings.Contains(verdict, "ergänzen"):
			kept++
			if !read[name] {
				t.Errorf("the table keeps %s, but nothing in the shipped code reads it — it would silently do nothing", name)
			}
		default:
			t.Errorf("%s carries no verdict (entfernen / umbenennen / bleibt / ergänzen): %q", name, strings.TrimSpace(verdict))
		}
	}
	if rows < 10 {
		t.Errorf("the table treats only %d lines — the old drop-in carried more than that", rows)
	}
	if retired == 0 || kept == 0 {
		t.Errorf("a line-by-line treatment that retires %d and keeps %d lines is not one", retired, kept)
	}
}

// shippedEnvNames is every DEVLAB_* variable the shipped (non-test) Go sources actually read.
func shippedEnvNames(t *testing.T) map[string]bool {
	t.Helper()
	out, err := exec.Command("grep", "-rhoE", `"DEVLAB_[A-Z_]+"`,
		"--include=*.go", "--exclude=*_test.go", "..").Output()
	if err != nil {
		t.Fatalf("cannot read the shipped environment contract: %v", err)
	}
	set := map[string]bool{}
	for _, m := range strings.Fields(string(out)) {
		set[strings.Trim(m, `"`)] = true
	}
	if len(set) < 20 {
		t.Fatalf("only %d environment names found — the comparison would pass on nothing", len(set))
	}
	return set
}

// ProtectSystem=strict makes /tmp read-only as well, and every per-user child the service starts writes
// there (the claude CLI's scratch, go build, npm). The unit must therefore keep /tmp writable one way
// or the other — otherwise the AI panel and every artifact build fail with EROFS the moment a drop-in
// stops papering over it.
func TestUnitKeepsTheScratchDirectoryWritable(t *testing.T) {
	unit := readDoc(t, unitTemplate)
	if !strings.Contains(unit, "ProtectSystem=strict") {
		t.Skip("the unit no longer hardens the file system — nothing to hold")
	}
	privateTmp := regexp.MustCompile(`(?m)^PrivateTmp=true`).MatchString(unit)
	writable := regexp.MustCompile(`(?m)^ReadWritePaths=.*(^|\s)/tmp(\s|$)`).MatchString(unit)
	if !privateTmp && !writable {
		t.Error("ProtectSystem=strict with neither PrivateTmp=true nor /tmp in ReadWritePaths: every per-user child gets a read-only /tmp")
	}
}

// ── the foreign effect is named in the runbook ────────────────────────────────

// The protection pass writes to repositories outside this cutover, so the runbook must say so and say
// what holds it. A cutover that does not mention it hands the operator a surprise.
func TestCutoverNamesTheProtectionHold(t *testing.T) {
	doc := readDoc(t, cutoverDoc)
	for _, want := range []string{
		"DEVLAB_RUNS_PROTECTION_ENFORCE",
		"DEVLAB_RUNS_PROTECTION_START_DELAY",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the cutover never names %s — the one effect that leaves DevLab is undocumented", want)
		}
	}
	if !strings.Contains(doc, "Fremdwirkung") {
		t.Error("the cutover has no section on what it touches beyond this host")
	}
}

// ── the inspection document's own claims ──────────────────────────────────────

// The acceptance row is a measurement OF A COMMIT. Bound to a wave name it cannot be re-checked, and
// two rows of the same wave can contradict each other.
func TestInspectionResultRowIsBoundToACommit(t *testing.T) {
	doc := readDoc(t, inspectionDoc)
	rows := measurementRows(t, doc)
	if len(rows) == 0 {
		t.Fatal("the inspection document carries no measurement row at all")
	}
	sha := regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	for _, first := range rows {
		if !sha.MatchString(first) {
			t.Errorf("the measurement row is taken at %q, which is not a commit — a row nobody can re-check", first)
			continue
		}
		if err := exec.Command("git", "-C", "../..", "rev-parse", "--verify", first+"^{commit}").Run(); err != nil {
			t.Errorf("the measurement row names commit %s, which this repository does not contain", first)
		}
	}
}

// measurementRows returns the first cell of every data row of the acceptance table.
func measurementRows(t *testing.T, doc string) []string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	var out []string
	inTable := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "| Commit"):
			inTable = true
		case inTable && strings.HasPrefix(line, "|---"):
			continue
		case inTable && strings.HasPrefix(line, "|"):
			cells := strings.Split(line, "|")
			if len(cells) > 1 {
				out = append(out, strings.TrimSpace(cells[1]))
			}
		case inTable:
			inTable = false
		}
	}
	return out
}

// A line whose acceptance cell asks for an end-to-end walk-through may never be reported as settled by
// its unit tests. The kinds are taken from the acceptance matrix itself where it is reachable (it is
// the input of this rebuild and lives beside the repository, not in it); the document's own enumeration
// is checked against its rows in either case.
func TestInspectionReportsE2ELinesAsE2E(t *testing.T) {
	doc := readDoc(t, inspectionDoc)
	rowKind := map[string]string{}
	for _, line := range strings.Split(doc, "\n") {
		m := regexp.MustCompile(`^\| ((?:K|REQ|B)-[0-9]+) \| (.*) \|$`).FindStringSubmatch(line)
		if m != nil {
			rowKind[m[1]] = m[2]
		}
	}
	if len(rowKind) < 40 {
		t.Fatalf("the status table holds only %d lines — the comparison would pass on nothing", len(rowKind))
	}

	// The document's own enumeration must match its rows exactly.
	enumerated := map[string]bool{}
	sentence := blockAfter(doc, "lines name an end-to-end", "### How the automated half")
	for _, id := range regexp.MustCompile(`(?:K|REQ|B)-[0-9]+`).FindAllString(sentence, -1) {
		enumerated[id] = true
	}
	for id, kind := range rowKind {
		if strings.Contains(kind, "E2E open") && !enumerated[id] {
			t.Errorf("%s carries E2E open but the enumeration does not list it", id)
		}
	}
	for id := range enumerated {
		if !strings.Contains(rowKind[id], "E2E open") {
			t.Errorf("%s is enumerated as an end-to-end line but its row reads %q", id, rowKind[id])
		}
	}

	matrix := filepath.Join("..", "..", "..", "spec", "ABNAHME.md")
	b, err := os.ReadFile(matrix)
	if err != nil {
		t.Skipf("the acceptance matrix is not reachable at %s — only the document's self-consistency was checked", matrix)
	}
	for _, line := range strings.Split(string(b), "\n") {
		m := regexp.MustCompile(`^\| ((?:K|REQ|B)-[0-9]+) \|`).FindStringSubmatch(line)
		if m == nil {
			continue
		}
		id := m[1]
		if !strings.Contains(line, "E2E") && !strings.Contains(line, "Klick") {
			continue
		}
		kind, ok := rowKind[id]
		if !ok {
			t.Errorf("the matrix names an end-to-end proof for %s, which the inspection document has no row for", id)
			continue
		}
		if !strings.Contains(kind, "E2E open") {
			t.Errorf("%s is an end-to-end line in the matrix, but the document reports %q — an acceptance claim wider than the evidence", id, kind)
		}
	}
}

// ── small text helpers (no knowledge of their own) ────────────────────────────

func lineWith(t *testing.T, doc, needle string) string {
	t.Helper()
	for _, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no line containing %q", needle)
	return ""
}

// blockAfter returns the text between the first occurrence of start and the next occurrence of end.
func blockAfter(doc, start, end string) string {
	i := strings.Index(doc, start)
	if i < 0 {
		return ""
	}
	rest := doc[i:]
	if j := strings.Index(rest, end); j > 0 {
		return rest[:j]
	}
	return rest
}

// indexOfLineWithAll returns the offset of the first line containing every needle, or -1.
func indexOfLineWithAll(doc string, needles ...string) int {
	at := 0
	for _, line := range strings.Split(doc, "\n") {
		hit := true
		for _, n := range needles {
			if !strings.Contains(line, n) {
				hit = false
				break
			}
		}
		if hit {
			return at
		}
		at += len(line) + 1
	}
	return -1
}

func indexOfLineWithAllOK(doc string, needles ...string) bool {
	return indexOfLineWithAll(doc, needles...) >= 0
}
