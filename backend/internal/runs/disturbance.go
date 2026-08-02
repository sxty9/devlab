package runs

import "strings"

// A DISTURBANCE is a notice that reports a FAULT — something the service found wrong about itself
// that demands the operator's attention: a delivery that never reached the user, a blockade, a
// protection that drifted, an exhausted request budget, an abandoned execution. Such a finding is
// DELIVERED outward (via the notify service). Everything else the pool records is OPERATIONAL
// NOISE — a routine lifecycle transition that completed normally and asks nothing of anybody (a
// restart requested, a restart completed, a startup reconcile, an automatic axiom assignment). Noise
// stays in the pool for the record but is NOT delivered: a flood nobody reads is as useless as
// silence.
//
// The distinction is drawn from the KIND — the message's nature — never from a list of special-case
// texts. And it is drawn the SAFE way round: a kind is noise ONLY if it is one of the known routine
// transitions; anything else, including a kind this build has never seen, counts as a disturbance and
// is delivered. So a new fault kind added later speaks up by default (fail loud) rather than being
// swallowed silently — the axiom "Kein stummes Ausbleiben" in code form.
//
// This classification is an EVALUATION, and so it lives OUTSIDE the passive notice pool: the store
// (NoticeStore) never calls it. The dispatcher that turns a new record into a delivery does, and the
// client mirrors the same rule (isDisturbance = alarm tone) so the outward voice and the dashboard's
// attention signal never disagree.
var operationalKinds = map[string]bool{
	"restart-requested": true,
	"restart-completed": true,
	"startup-reconcile": true,
	NoticeAssigned:      true,
}

// IsDisturbance reports whether a notice of this kind is a fault to be delivered to the user, as
// opposed to routine operational noise kept only in the pool. See the operationalKinds comment for
// the principle (default to disturbance; only the named routine transitions are noise).
func IsDisturbance(kind string) bool {
	return !operationalKinds[strings.TrimSpace(kind)]
}
