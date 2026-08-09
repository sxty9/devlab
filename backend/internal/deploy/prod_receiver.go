package deploy

// The production-receiver drift check. Two root scripts live on the PRODUCTION host as the forced
// command behind the deploy key — the receiver itself (devlab-deploy-recv) and the library it sources
// (devlab-setup-lib.sh). The delivery chain deliberately CANNOT install them: the deploy key is a
// forced command that can only rsync into staging and trigger an install, so it can never overwrite its
// own gatekeeper — a delivery that could replace its own receiver would be exactly the self-escalation
// the design forbids (see devlab-deploy-recv's header and selfWrappers in wrappers.go, which excludes
// the receiver). They reach production only through an operator with root running devlab-install-recv on
// the target.
//
// So a devlab (self) delivery whose merged content CHANGES one of these scripts ships a change the chain
// cannot deliver. Before this check the send still proved the SERVICE running and the delivery settled
// "live", while the receiver-carried part (the edge-address declaration, the serve-root rule) never
// arrived — the ledger claimed live for a step that never happened ("Kein stummes Ausbleiben"). This
// check closes that: it MEASURES what the host actually carries (from the receiver's own self-report,
// read-only, widening the forced command's rights by nothing) and reports drift, so the sending side can
// refuse to settle live and surface the outstanding step as a concrete approval instead.

import (
	"sort"
	"strings"

	"devlab/backend/internal/git"
)

// ReceiverScripts are the two root scripts on the production host the chain cannot deliver to itself.
// This is the ONE place they are named for the drift check (Keine Redundanz); the operator installer
// devlab-install-recv names the same two independently (it runs on the target, not in this process).
var ReceiverScripts = []string{"devlab-deploy-recv", "devlab-setup-lib.sh"}

// ReceiverSelfSHAPrefix is the machine-readable token the receiver prints for each of its own scripts
// during an install trigger — READ-ONLY self-reporting that lets the sending side MEASURE what the
// production host carries without widening the forced command's rights by a single byte: it adds no
// operation, argument or writable path, only more output on a run the chain already triggers. The line
// is "RECV-SELF-SHA <name> <sha256hex>". devlab-deploy-recv must print exactly this token.
const ReceiverSelfSHAPrefix = "RECV-SELF-SHA"

// ParseReceiverSelfSHA reads the receiver's self-reported checksums out of the install-trigger output
// (stdout and stderr combined). A host running an OLDER receiver that does not report yet yields an
// empty entry for that name — which ReceiverDrift reads as "not proven current", so an unreported
// receiver is treated exactly like a stale one (absence of proof is not proof).
func ParseReceiverSelfSHA(triggerOut string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(triggerOut, "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) == 3 && f[0] == ReceiverSelfSHAPrefix {
			out[f[1]] = f[2]
		}
	}
	return out
}

// ReceiverDrift compares what the production host reports carrying for each receiver script against the
// MERGED content the delivery ships (read from the committed ref, byte-for-byte, so the sha256 equals
// that of a file installed verbatim). It returns one WrapperDrift per script whose host copy is
// absent-or-different, and whether any drifted. reported is ParseReceiverSelfSHA of the trigger output;
// an empty or missing entry counts as drift (an old, silent receiver is not proven current). ref names
// the merged stand (e.g. "origin/main"); a script the merged content does not carry is skipped (this
// delivery could not have changed it). The result is sorted by name so the report is stable.
func ReceiverDrift(wt, ref string, reported map[string]string) ([]WrapperDrift, bool) {
	var drifts []WrapperDrift
	for _, name := range ReceiverScripts {
		want, ok := git.FileAtRefBytes(wt, ref, "deploy/"+name)
		if !ok {
			continue // the merged content does not carry it — nothing this delivery could change
		}
		wantSHA := sha256hex(want)
		got := reported[name]
		if got == wantSHA {
			continue // the host already carries exactly the merged script
		}
		have := got
		if have == "" {
			have = "not reported (the receiver predates self-reporting, or the script is absent)"
		}
		drifts = append(drifts, WrapperDrift{
			Name:    name,
			WantSHA: wantSHA,
			Reason:  "the production host carries a different version than the merged delivery ships",
			Summary: "host carries " + have + "; the merged delivery needs sha256 " + wantSHA,
		})
	}
	sort.Slice(drifts, func(i, j int) bool { return drifts[i].Name < drifts[j].Name })
	return drifts, len(drifts) > 0
}
