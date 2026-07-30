# 80 — Contract changes (log)

Every change to a file frozen after Welle 0 (BAUPLAN §0.2) is recorded here with its file, its
reason and where to look in the diff. Frozen were: `go.mod`/`go.sum`, `package.json`,
`backend/internal/model/*`, `backend/internal/statepath/*`, `backend/internal/live/topics`,
`backend/internal/executor/chain.go`, `backend/internal/api/api.go`, `backend/cmd/devlabd/main.go`,
`src/types.ts`, `src/data/source.ts`, `src/data/httpSource.ts`, `src/data/index.ts`,
`contract/fixtures/*`.

Baseline for every diff hint below: Welle 0 = `cbffed4`, Welle 2 = `4b54464`.

---

## 1. `backend/cmd/devlabd/main.go` — the boot sequence is built, not sketched

**Who:** Welle 3 (INT).
**Reason:** the file carried `TODO(B2/B3/B4)` anchors where ARCHITEKTUR §6.2 requires calls. With
those anchors the daemon opened no execution documents, exorcised no ghosts, drove no scheduler and
never invoked the chain motor — every downstream acceptance line was unreachable in production while
the integration suite stayed green (acceptance line K-2b).
**What changed:** the documented order 1–8 is now constructed: env contract + fail-closed SSO →
pools (incl. the settings pool with its env seed and the live broker) → `execstate.Open` →
`MarkInterruptedAtBoot` → `sched.New` with the injected preflight gate, the ExecFunc and
`MaintainDeliveries` → `SetExecution` → `CompleteBootRestart` → HTTP + ready socket + listener →
`SyncStartupTodos` → `scheduler.Start` → reporter + protection loop → SIGTERM:
`DrainAndPersist` before `httpServer.Shutdown`. The ExecFunc (`execute`) is the ONE place
`executor.Execute` is called; it composes the sink from `sched.DocSink` and the result recorder.
**Diff hint:** `git diff 4b54464 -- backend/cmd/devlabd/main.go`.
**Proof:** `backend/it/boot_wiring_test.go` (the order is read out of the shipped source, comments
stripped, so prose cannot satisfy it) plus the `K-2b` check in `tools/abnahme.sh`.

## 2. `backend/internal/api/api.go` — `POST /api/mcp` moves onto the CSRF-enforcing guard

**Who:** Welle 3 (INT).
**Reason:** ARCHITEKTUR §7 assigns the MCP endpoint the plain read guard, with the CSRF double
submit enforced per mutating TOOL inside the handler. That leaves one mutating route reachable
cross-site with the caller's cookies — the exception the B-03 acceptance check names. Bearer callers
(the actual MCP consumers) are unaffected, because the double submit is waived on the bearer path.
**What changed:** `s.guard(s.mcpEndpoint)` → `s.guardCSRF(s.mcpEndpoint)`. The per-tool check stays
in force as the finer rule (a reading tool now needs the header too, on the cookie path only).
**Consequence for operation:** a cookie-authenticated caller of `/api/mcp` must send
`X-CSRF-Token`. Agents authenticate by bearer and are unaffected. The route table now has NO
mutating route on a non-CSRF guard — no exception left to reason about.
**Diff hint:** `git diff 4b54464 -- backend/internal/api/api.go`, section "Δ MCP (REQ-043)".
**Proof:** `tools/abnahme.sh` check `B-03`; `backend/it/guards_test.go`
(`TestGuardMatrixCoversEveryRoute` reads the tier out of the table and enforces it).

## 3. `backend/internal/api/api.go` — pool accessors and the settings read for the composition root

**Who:** Welle 3 (INT).
**Reason:** the scheduler needs the run and result pools, and the ExecFunc needs the effective
settings. Re-opening those pools in `cmd/devlabd` would put a second mutex over one file — two
access paths to one entity (single source of truth, atomic access). So the pools' owner hands them
out instead.
**What changed:** added `RunStore()`, `ResultStore()` and `Settings()` on `*Server`.
**Diff hint:** same file, right after `SetSettings`.

## 4. `backend/internal/api/api.go` — bearer identity and the CSRF waiver (B11, verified here)

**Who:** Welle 2 (B11); reviewed and confirmed by Welle 3.
**Reason:** the frozen file carried the deliberate extension point for the bearer entry (D 34):
`resolveUser` was cookie-only and `checkCSRF` had no waiver.
**What changed:** `resolveUser` → `s.v.FromRequest(r)` (cookie OR bearer through the SAME verifier —
one validation path, not a second implementation); `checkCSRF` →
`auth.BearerPresented(r) || s.v.CheckCSRF(r)`.
**Assessment (INT):** substantively right and safe. Both decisions — "does CSRF apply" and "whose
identity is this" — read the same predicate, so the waiver cannot be borrowed: a page that adds a
forged `Authorization` header beside the victim's cookies waives the check and loses the session in
the same move, and the request is refused (401) instead of performed. A cross-site request cannot
present a real bearer at all (no CORS headers are sent, so a preflight for a custom header fails).
**Diff hint:** `git diff cbffed4 4b54464 -- backend/internal/api/api.go`.
**Proof added by INT:** `backend/it/guards_test.go`
(`TestCSRFWaiverBelongsToTheBearerPathAlone`) — the waiver was previously untested.

## 5. `src/data/source.ts` (+ `httpSource.ts`, `stubSource.ts`) — the session refresh joins the seam

**Who:** Welle 3 (INT).
**Reason:** `src/state/live.tsx` opened its own refresh call (`apiFetch('/api/auth/refresh', …)`).
That named an API path outside the data seam (acceptance line REQ-040a) and was a SECOND path to an
entity that already has one — `httpSource`'s single-flight `refreshSession`, which the live
provider's call bypassed, so a reconnect burst could stampede the refresh endpoint.
**What changed:** `DataSource` gains `refreshSession(): Promise<boolean>`; `httpSource` exposes its
existing single-flight function as that member; `stubSource` answers `false` (offline there is no
session to re-mint — an honest "not refreshed", never a pretended success). `httpSource` no longer
exports `apiFetch`/`apiJson`/`withCsrf` (they had exactly this one foreign caller).
`state/live.tsx` now calls `getDataSource().refreshSession()`.
**Parity:** the new operation is declared omitted in `contract/mcp-tools.json` and
`internal/api/mcp_tools.go` with its reason (an agent authenticates per request with its own bearer
token, so it has no cookie session to refresh) — otherwise the MCP parity test fails, by design.
**Diff hint:** `git diff 4b54464 -- src/data/ src/state/live.tsx contract/mcp-tools.json`.
**Proof:** `tools/abnahme.sh` check `REQ-040a`; `src/parity.test.ts`;
`backend/internal/api/handlers_mcp_test.go` (`TestToolTableMirrorsTheDataSource`,
`TestParityArtifactInStep`).

## 6. `contract/mcp-tools.json` — one omission entry

**Who:** Welle 3 (INT).
**Reason:** the parity artefact is a frozen fixture, and it must stay in step with the tool table
(the Go side regenerates it deliberately). The new `refreshSession` operation needs its stated
reason on both sides.
**What changed:** one entry appended to `omittedDataSourceOps` (identical wording on both sides).
**Diff hint:** `git diff 4b54464 -- contract/mcp-tools.json`.

---

## Not a contract change, recorded for completeness

These files are not on the frozen list; they are logged because they change a published shape.

- **`backend/internal/report/compose.go`** — `Item.Suspended` → `Item.Paused`. "Suspended" was a
  synonym for the one pause vocabulary (paused · skipped · blocked), which REQ-040.4 fixes across
  UI, API, store, log AND report. The field was additionally never filled: the reporter now reads
  the pause off the execution DOCUMENTS through a new seam (`report.PausedExecutions`, implemented
  by `execstate.Store.PausedIDs`), so an unfinished execution is named "paused" only when a document
  says so — never guessed from a missing end stamp.
- **`backend/internal/workbench/ops.go`** (new) — the neutral primitives the chain motor composes
  (`AheadOfDefault`, `ContainedInDefault`, `MergeBaseDefault`, `CommitsAhead`, `HasUncommitted`,
  `CommitAll`, `ReadFile`, `WriteFile`, `BranchAt`, `PushBranch`, `RepoDir`). They live in the
  workbench, next to the branch they operate on, so the motor's seam adapter is a one-to-one shim
  and no caller re-derives a git invocation of its own.
- **`backend/internal/api/exec_deps.go` / `exec_record.go`** (new) — the production
  `executor.Deps`, `preflight.Sources`, the result recorder and the composed `ExecutionSink`. They
  sit in `api` because `api.New` owns every passive pool. Two adapter mismatches the waves reported
  are resolved here: `workbench.Prepare` answers `(PrepareResult, error)` and is mapped onto the
  motor's `PrepareInfo`; `sched.RequestRestart` answers `(RestartState, error)` and is adapted to
  the motor's `error` by `api.SchedulerHooks`.
- **`backend/internal/api/handlers_mercury_deliveries.go`** — `runnerGitSide.RedeliverDev` no
  longer returns the named placeholder error. The re-delivery after a counter-booking (REQ-025.5)
  now rides the SAME delivery composition the chain rides, and reads its answer honestly: a
  repository that is not a service has nothing to re-deliver (success), an install without a
  running proof is a named failure.
- **`backend/internal/sched/sched_test.go` / `restart_test.go`** — four flakes fixed, found by running
  the suite uncached and repeatedly (the test cache had been hiding them). None was a production
  defect; all four were the harness observing the wrong instant.
  1. + 2. The scheduler writes the document's `running` phase BEFORE it launches the goroutine that
     hands the execution to the executor (the document is the truth, the goroutine follows it), so a
     test inspecting what the executor received could read the PREVIOUS handover or none at all. The
     fake now records every handover per id and the harness waits for the nth one
     (`waitStarted(id, nth)`) — a resume is the second handover of the same id, by definition.
  3. An execution goroutine outlived its test and wrote into the temporary state root while Go
     removed it ("directory not empty"). `newHarness` now releases every blocked fake and waits for
     the goroutines in `t.Cleanup`, and FAILS the test if they do not wind down. That release is a
     LATCH, so a start reaching the fake only after the teardown began finishes at once instead of
     blocking on a freshly created channel.
  4. `TestAutoRunClaimsOnlyItsActiveRepo` poked the scheduler's live-handle map without the admission
     mutex the scheduler reads it under. It now takes the mutex.
- **`backend/it/harness_test.go`** — the suite's `stop()` released every held agent BEFORE cancelling,
  so a "killed mid-work" test could silently become "finished just in time": the held agent completed
  its repository, the resume then had nothing left to do, and the kill/resume test failed
  intermittently on "the interrupted repository was worked 0 times". The order is now the one SIGTERM
  uses — cancel, drain, and only then release as the safety net against a leaked goroutine. Twenty
  consecutive uncached runs of the whole backend suite and two `-race` runs are green.
- **`backend/internal/chats/chats.go`, `backend/internal/workspace/workspace.go`** — the package doc
  comments named the template's default state directory. They now name it relative to the state root
  (`statepath.Chats`, `statepath.Workspaces`), which is both accurate and instance-neutral; the
  `info-1` note of the audit is thereby empty.
- **`tools/abnahme.sh`** — one check added: `B-46`, no `/opacity` modifier on a var()-valued theme
  token. Tailwind drops such a declaration silently, so the mark renders NOTHING. The forbidden
  token set is derived from `src/theme/tailwind-preset.ts` itself, so it cannot drift. The check
  found and the fix removed three uses of `border-border-default`, a token the theme never defined —
  three borders that emitted no CSS at all.
- **`src/lib/format.ts`** — the formatters no longer nail `de-DE` into an English, multilingual
  surface: they render in the CHOSEN UI language, read from the document's own `<html lang>`
  declaration (the one place a surface states its language, and the single attribute a language
  switch changes), with NEUTRAL as the fallback. The formatting API is unchanged — no caller passes
  a locale.
