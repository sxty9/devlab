// Package it is the integration suite (Baustein Q): it composes the REAL packages across their
// seams and drives them through the REAL HTTP surface, where the per-package unit tests
// deliberately stop at their own boundary.
//
// The suite's standard: a fixture may only stand at an I/O EDGE (no GitHub, no agent process, no
// root). It may never re-implement an invariant the acceptance matrix asks about — an invariant
// proven by a fixture is not proven at all, because the shipped code could lose it while this suite
// stayed green. fixtures_test.go states that rule and holds itself to it.
//
// What lives here, and why it cannot live in a single package:
//
//   - harness_test.go     the composed system minus main(): one state root, the real stores, the real
//     scheduler over the real motor, the real route table, the real delivery
//     maintenance — on a real HTTP listener.
//   - fixtures_test.go    the three substituted edges (GitHub REST, agent process, root install) over
//     REAL git repositories driven by the SHIPPED workbench and the SHIPPED
//     delivery path.
//   - boot_test.go        the boot sequence end to end (ARCHITEKTUR §6.2) — ghost exorcism,
//     restart completion, the ready socket, the ONE live stream —
//     over statepath + execstate + runs + sched + executor + api together.
//   - boot_wiring_test.go the shipped daemon constructs that same boot sequence, in that order.
//   - invariants_test.go  the construction faults at the code that must hold them: work is folded in
//     and never reset (K-1), an open pull request is adopted and never duplicated
//     (K-6), a permanent fault is attempted exactly once (K-5), an
//     evidence-free skip is refused (K-4), and the delivery loop really closes
//     (merge, prune, observable in the default branch).
//   - composition_test.go the two decisions the PRODUCTION adapter (api/exec_deps.go) makes alone and
//     that no behavioural test can reach: registering an opened pull request for
//     the auto-merge window, and recognising the self repository so its restart is
//     a handover (K-2).
//   - resume_test.go      process death → interrupted → resume at the same spot with the same
//     identity, observed through the HTTP surface (K-2/REQ-018/REQ-039.1).
//   - race_test.go        restart request × start attempt are mutually exclusive and kill no run;
//     ten slots carry ten concurrent executions (REQ-013.4); repository
//     exclusivity queues instead of skipping (REQ-014).
//   - legacy_test.go      a pre-rebuild result document stays readable and renders a defined
//     state through the history route (REQ-027.3).
//   - guards_test.go      the guard matrix over the WHOLE route table (B-03).
//   - surface_test.go     every route has at least one caller and every caller a route (REQ-040.3).
//   - symmetry_test.go    automatic runs and todos reconcile function by function (REQ-005).
//   - vocabulary_test.go  one vocabulary across model, API, store, log and report (REQ-040.4).
//   - plumbing_test.go    the read-only git plumbing against a REAL repository, and the encrypted
//     token pool at rest (B-12, B-14) — both only provable outside their own
//     package, against real git and a real directory.
//
// The package has no production code: everything is in _test.go files, so nothing here can be
// imported by the daemon by accident.
package it
