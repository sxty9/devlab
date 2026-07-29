// Package it is the integration suite (Baustein Q): it composes the REAL packages across their
// seams and drives them through the REAL HTTP surface, where the per-package unit tests
// deliberately stop at their own boundary.
//
// What lives here, and why it cannot live in a single package:
//
//   - boot_test.go        the boot sequence end to end (ARCHITEKTUR §6.2) — ghost exorcism,
//     restart completion, the ready socket, the ONE live stream —
//     over statepath + execstate + runs + sched + executor + api together.
//   - resume_test.go      process death → interrupted → resume at the same spot with the same
//     identity, observed through the HTTP surface (K-2/REQ-018/REQ-039.1).
//   - race_test.go        restart request × start attempt are mutually exclusive and kill no run.
//   - load_test.go        ten slots carry ten concurrent executions (REQ-013.4).
//   - legacy_test.go      a pre-rebuild result document stays readable and renders a defined
//     state through the history route (REQ-027.3).
//   - guards_test.go      the guard matrix over the WHOLE route table (B-03).
//   - surface_test.go     every route has at least one caller and every caller a route (REQ-040.3).
//   - symmetry_test.go    automatic runs and todos reconcile function by function (REQ-005).
//   - vocabulary_test.go  one vocabulary across model, API, store, log and report (REQ-040.4).
//   - plumbing_test.go     the read-only git plumbing against a REAL repository, and the encrypted
//     token pool at rest (B-12, B-14) — both only provable outside their own
//     package, against real git and a real directory.
//
// The package has no production code: everything is in _test.go files, so nothing here can be
// imported by the daemon by accident.
package it
