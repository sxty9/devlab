package it

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// The PRODUCTION composition — the adapter that binds the motor to the real GitHub, the real host and
// the real pools (internal/api/exec_deps.go). It is the one layer this suite cannot drive
// behaviourally: its GitHub calls go to the API host through a compile-time constant base URL, and
// its installer is root. So the two decisions it makes ALONE are asserted on the shipped source, per
// function body, with comments carried by the parser and never read as code.
//
// Both decisions are invisible everywhere else, which is exactly why they need a test of their own:
//
//   - the opened pull request must be REGISTERED for the auto-merge window. Without the registration
//     the chain still delivers, every stage still ends green, every existing test still passes — and
//     nothing ever merges. The whole pipeline would stop at "pull request opened", forever.
//   - the SELF repository must be recognised, so its delivery installs now and hands the restart over
//     (K-2). Without the recognition the self repo takes the ordinary host path, whose restart kills
//     the very execution that is running.
//
// The test reads the adapter's AST, so it survives reformatting and cannot be satisfied by prose.

const adapterFile = "../internal/api/exec_deps.go"

// TestDeliveryAdapterRegistersTheOpenedPullRequestForAutoMerge is REQ-024/F14: opening a pull request
// and putting it into the auto-merge window are ONE act in the production adapter.
func TestDeliveryAdapterRegistersTheOpenedPullRequestForAutoMerge(t *testing.T) {
	body := methodBody(t, adapterFile, "chainDeliver", "OpenOrAdoptPR")

	for _, want := range []struct{ call, why string }{
		{"deliver.OpenOrAdoptPR", "the ONE pull-request path (K-6) — the adapter must not open one itself"},
		{"runPRs.Add", "the opened pull request must enter the auto-merge window, or nothing is ever merged"},
		{"automergeWindow", "the window is a runtime setting, not a constant baked into the adapter"},
		{"MergeBy", "the record must carry WHEN it becomes mergeable"},
		{"DeliveryID", "the tracked record must point back at its ledger delivery"},
	} {
		if !strings.Contains(body, want.call) {
			t.Errorf("chainDeliver.OpenOrAdoptPR never mentions %s — %s", want.call, want.why)
		}
	}

	// The registration must sit on the SUCCESS path: a failed delivery must not be queued for merge.
	regAt, errAt := strings.Index(body, "runPRs.Add"), strings.Index(body, "err == nil")
	if errAt < 0 || regAt < errAt {
		t.Errorf("the auto-merge registration is not guarded by the success of the delivery (body:\n%s\n)", body)
	}
}

// TestDeployAdapterRecognisesTheSelfRepositoryAndHandsTheRestartOver is K-2/B-2: the self repo's
// delivery installs immediately and hands the restart to the root wrapper; it never restarts inline,
// and it reports Self so the motor requests the handover.
func TestDeployAdapterRecognisesTheSelfRepositoryAndHandsTheRestartOver(t *testing.T) {
	body := methodBody(t, adapterFile, "chainDeploy", "DeliverDev")

	for _, want := range []struct{ call, why string }{
		{"selfRepo()", "the self repository must be RECOGNISED — otherwise its deploy kills the running execution"},
		{"SelfInstallAndHandover", "the self repo installs now and hands the restart over (K-2)"},
		{"Self: true", "the outcome must say it was the self repo, or the motor never requests the handover"},
		{"deploy.DeliverDev", "every other repository takes the ordinary, honest delivery path"},
	} {
		if !strings.Contains(body, want.call) {
			t.Errorf("chainDeploy.DeliverDev never mentions %s — %s", want.call, want.why)
		}
	}
	for _, forbidden := range []string{"systemctl", "systemd-run", "Restart("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("chainDeploy.DeliverDev reaches for %q — the restart is a HANDOVER, never inline (K-2)", forbidden)
		}
	}

	// The self decision must be derived from the repository, and its default must be a name, not an
	// instance: selfRepo() reads the environment and falls back to the product's own service id.
	sel := funcBody(t, adapterFile, "selfRepo")
	if !strings.Contains(sel, "Getenv") {
		t.Error("selfRepo does not read the environment — the self repository would be hard-wired")
	}
}

// ── AST helpers ──────────────────────────────────────────────────────────────────────────────

// methodBody returns the source text of one method body of one receiver type.
func methodBody(t *testing.T, file, recv, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Recv == nil || len(fn.Recv.List) == 0 || fn.Body == nil {
			continue
		}
		if receiverName(fn.Recv.List[0].Type) != recv {
			continue
		}
		return sourceOf(t, file, fset, fn.Body.Pos(), fn.Body.End())
	}
	t.Fatalf("%s has no method %s.%s — the production composition changed shape", file, recv, name)
	return ""
}

// funcBody returns the source text of one plain function body.
func funcBody(t *testing.T, file, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Recv == nil && fn.Body != nil {
			return sourceOf(t, file, fset, fn.Body.Pos(), fn.Body.End())
		}
	}
	t.Fatalf("%s has no function %s", file, name)
	return ""
}

func receiverName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.StarExpr:
		return receiverName(v.X)
	case *ast.Ident:
		return v.Name
	}
	return ""
}

// sourceOf cuts the byte range out of the file and strips comments, so the assertions read the CODE
// and never the prose that documents it.
func sourceOf(t *testing.T, file string, fset *token.FileSet, from, to token.Pos) string {
	t.Helper()
	src := readFileForAST(t, file)
	start := fset.Position(from).Offset
	end := fset.Position(to).Offset
	if start < 0 || end > len(src) || start >= end {
		t.Fatalf("%s: unusable source range %d..%d", file, start, end)
	}
	return stripComments(string(src[start:end]))
}

func readFileForAST(t *testing.T, file string) []byte {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return b
}
