# 80 — Contract changes (log)

Every change to a file frozen after Welle 0 (BAUPLAN §0.2) is recorded here with its file, its
reason and where to look in the diff.

Frozen were: `backend/go.mod`, `backend/go.sum`, `package.json`, `backend/internal/model/*`,
`backend/internal/statepath/*`, `backend/internal/executor/chain.go`,
`backend/internal/api/api.go`, `backend/cmd/devlabd/main.go`, `src/types.ts`, `src/data/source.ts`,
`src/data/httpSource.ts`, `src/data/index.ts`, `contract/fixtures/*`.

Of `backend/internal/live/live.go` only the TOPIC CONSTANTS are frozen; the broker beneath them was a
Welle-0 stub and was deliberately filled in Welle 1.

That list is not prose. `tools/abnahme.sh` READS it — checks `contract-a` (a frozen file changed
since the baseline and no entry below names it) and `contract-b` (the live topic constants moved) —
so the rule now fails an audit instead of relying on discipline: it was written down here first and
then broken five times in one commit. The paths are therefore spelled exactly as the repository
spells them, and the paragraph beginning "Frozen were:" is the one the check parses.

Baseline for every diff hint below: Welle 0 = `cbffed4`, Welle 2 = `4b54464`, Welle 3 = `3e2019b`,
repair wave 1 = `0a9f8fe`, repair wave 4 = `2e6a1ab`.

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

---

## Recorded after the fact — the contract changes of repair wave 1 (`3e2019b` → `0a9f8fe`)

Repair wave 1 changed five contract artefacts and wrote none of them down. The entries below close
that gap; each one names the file, the reason and where to look, exactly as it should have been
written when the change was made. Since this wave the omission is no longer possible unnoticed:
`tools/abnahme.sh` check `contract-a` compares the frozen list in this document's header against git
and FAILS on a change no entry names.

### 7. `backend/internal/api/api.go` — the report status access point gains its writing verb

**Who:** repair wave 1.
**Reason:** a report delivery that exhausted its retries ends as `blocked` and, by definition, never
resumes itself (K-5). The resumption therefore needs an access point — and the READING one already
existed (`GET /api/mercury/runs/report-status`), so the write joins it as the same access point's
second verb instead of standing beside it as a second, similar sibling.
**What changed:** one route registered — `mux.HandleFunc("POST /api/mercury/runs/report-status",
s.guardCSRF(s.runsReportStatus))`. On the CSRF-enforcing guard, like every other mutating route; the
handler branches on the method.
**Diff hint:** `git diff 3e2019b 0a9f8fe -- backend/internal/api/api.go`.
**Proof:** `tools/abnahme.sh` check `B-03` (no mutating route on a non-CSRF guard);
`backend/it/guards_test.go` reads the tier out of the table itself; the behaviour is pinned by
`backend/internal/api/handlers_mercury_report_test.go`
(`TestReportStatusRouteAcceptsTheResume`, `TestRunsReportStatusResumeIsHonestAndIdempotent`).

### 8. `src/types.ts` — `blocked` becomes a report state, and a delivery names its execution

**Who:** repair wave 1.
**Reason:** two honesty gaps in the wire vocabulary. A report delivery could only be `sent` or
`failed`, so the END of the retries had no name of its own and a blocked send read as just another
failure (K-5 wants reason, attempts and times, plus an explicit resumption). And a delivery record
could not be attributed to the execution it arose from, so a surface had to GUESS which executions
still carry an open delivery from a chain stage name — the derivation B-35 retires.
**What changed:** `ReportDelivery.status` gains `'blocked'` and an optional `backoff: Backoff`;
`Delivery` gains an optional `executionId`. Both are additive — no existing field changed shape, so
no reader breaks.
**Diff hint:** `git diff 3e2019b 0a9f8fe -- src/types.ts`.
**Proof:** `backend/it/vocabulary_test.go` (one vocabulary across UI, API, store, log and report);
`src/views/mercury/deliveries/deliveries.test.ts`; `src/views/mercury/exec/logic.test.ts`.

### 9. `src/data/source.ts` (+ `src/data/httpSource.ts`, `src/data/stubSource.ts`) — the resumption enters through the seam

**Who:** repair wave 1.
**Reason:** the surface must be able to resume a blocked report, and the ONE place a surface may name
an API path is the data seam (REQ-040a). A call from a view would have been a second data path to an
entity that now has one.
**What changed:** `DataSource` gains `mercuryResumeReportDelivery(day?): Promise<{ resumed: string[] }>`;
`httpSource` posts it to the existing access point; `stubSource` answers `offline` — no pretended
success where there is no server. The answer is the list of days actually resumed, never a bare `ok`.
**Diff hint:** `git diff 3e2019b 0a9f8fe -- src/data/`.
**Proof:** `src/parity.test.ts` (every seam operation is mirrored by an MCP tool or declared omitted);
`backend/internal/api/handlers_mcp_test.go` (`TestToolTableMirrorsTheDataSource`).

### 10. `contract/mcp-tools.json` + `backend/internal/api/mcp_tools.go` — the tool beside the new operation

**Who:** repair wave 1.
**Reason:** the parity artefact is generated from the tool table and read by the parity test, so a new
seam operation without a tool (or without a stated omission) fails that test by design — an agent
would otherwise be unable to do what a person can (REQ-043).
**What changed:** one tool `report_resume` (`mercuryResumeReportDelivery`, POST, tier `csrf`, one
optional `day` argument) added on both sides in identical wording.
**Diff hint:** `git diff 3e2019b 0a9f8fe -- contract/mcp-tools.json backend/internal/api/mcp_tools.go`.
**Proof:** `src/parity.test.ts`; `backend/internal/api/handlers_mcp_test.go`
(`TestParityArtifactInStep`).

---

## Repair wave 2

### 11. `backend/internal/api/api.go` — no configured namespace, no constitution repository

**Who:** repair wave 2 (S4). Touched outside its own ownership because the file only has to PASS THE
ERROR THROUGH; recorded here for exactly that reason.
**Reason:** `axiomsRepo()` composed the constitution's repository name as `discover.Owner() +
"/axioms"`. The accessor answered `string` alone, so an instance that had configured no owner got the
name `"/axioms"` — and a LEADING SLASH is what `axiomrepo` reads as a filesystem remote. The store
would then have cloned whatever directory of that name exists on the host and served it as this
instance's constitution: a foreign namespace admitted by an unset configuration, reported as either
"unreachable" or, worse, as a perfectly readable set of axioms belonging to somebody else. REQ-001
requires the opposite — one store, no second data path, and a read error that is never "no axioms".
**What changed:** `axiomsRepo()` now answers `(string, error)`; `api.New` builds the store only when a
namespace exists and logs the named reason otherwise. `*axiomrepo.Store` is nil-safe by construction,
so every constitution operation then answers `ErrNoStore` ("constitution store not configured") —
distinguishable from "no axioms" and from "unreachable".
**Diff hint:** `git diff 0a9f8fe -- backend/internal/api/api.go backend/internal/api/axioms_config.go`.
**Proof:** `backend/internal/api/owner_scope_test.go`
(`TestAxiomsRepoRefusesWithoutANamespace`, `TestServerWithoutANamespaceHasNoConstitutionStore`), plus
`backend/internal/discover/discover_test.go`
(`TestEveryOwnerCallSiteHandlesTheNamedError`), which reads the shipped sources and holds every caller
of `discover.Owner` to the two-answer contract.

### 12. `deploy/devlab-install`, `deploy/devlabd.service` — a new operator-provisioned file, and two unit modes

**Who:** repair wave 2 (S1). Neither file is on the §0.2 freeze list, so this entry is not owed as a
frozen-contract change — it is recorded here because it adds a REQUIREMENT the cutover must satisfy,
and a cutover that misses it makes every foreign dev-delivery fail closed.
**Reason:** the install wrapper's name grammar `^[a-z][a-z0-9-]{2,30}$` is a shape, not a namespace:
`root`, `caddy`, `sshd` and `postgres` all satisfy it, the generated unit carries `User=<repo>` and
`ExecStart=/opt/<repo>/bin/<repo>d`, and "first time" was decided by the ABSENCE of
`/etc/systemd/system/<repo>.service` — which cannot see a vendor unit under `/lib`. A repository
named `root` therefore had root install that repository's own binary as a unit running as uid 0, and
a repository named after any packaged service could shadow it.
**What changed (namespace):** a foreign repository must now (a) not be a reserved system or
landscape name (`RESERVED_REPOS` in the wrapper, plus the `systemd-*` prefix), (b) BE the checkout
the artifact was built in and belong to the configured organisation — read from the checkout's origin
remote as text, never by running git as root, (c) own its service account: an existing account must
be the nologin system account under `/var/lib/<repo>` this wrapper creates, and (d) be unknown to
systemd or known only through `/etc/systemd/system/<repo>.service` — existence is asked of
`systemctl show -p FragmentPath`, so a vendor, `/run` or masked fragment is seen and refused. The
rights manifest is resolved inside the checkout, so a symlink out of it can no longer make root
publish a foreign file in `/etc/holistic/permissions.d`.
**Consequence for operation — the new file:** the configured organisation is an instance value, so it
lives in root-owned runtime configuration and not in the template:

```bash
printf '%s\n' '<github-owner>' | sudo tee /etc/devlab/gh-owner >/dev/null   # same value as DEVLAB_GH_OWNER
sudo chmod 0644 /etc/devlab/gh-owner
```

Without it the wrapper fails closed (exit 5, "no managed organisation configured"). It must hold the
same owner as the daemon's `DEVLAB_GH_OWNER`; the wrapper may not read the caller's environment for
it, because a caller that chooses its own namespace is not measured against one.
**What changed (unit):** `devlabd.service` gains `StateDirectoryMode=0711` (systemd's default 0755
let every local account LIST the state root, which holds the link store and the transcripts; 0711
keeps the traverse bit the per-user workspaces need, which 0750 would cut) and `UMask=0027`, matching
devlab-exec. Security-relevant modes stay explicit at their site — the readiness socket now sets
`api.SocketMode` itself rather than inheriting the umask.
**Diff hint:** `git diff 0a9f8fe -- deploy/devlab-install deploy/devlab-exec deploy/devlab.sudoers deploy/devlabd.service backend/internal/api/ready.go`.
**Proof:** `backend/internal/deploy/wrapper_namespace_test.go` (one dry run per attack path:
reserved names, foreign checkout name, foreign organisation, decoy origin, unconfigured owner,
vendor/masked unit, unaskable systemd, escaping rights manifest, and the derived workspace root),
`backend/internal/api/ready_test.go` (`TestReadySocketModeIsOwnerAndGroupOnly`).

---

## Repair wave 3

### The recorded state of every changed frozen file (`contract-a` now compares against it)

**Who:** repair wave 3 (T3).
**Reason:** the rule above was measured by asking whether the protocol's body NAMES the changed path.
That is satisfied by any entry, however old: a second change to a file an earlier entry already named
passed the audit unseen. It was demonstrated by appending an exported type to the frozen wire
contract `src/types.ts` — the whole file set stayed green, because entry 5's paragraph had named the
path once and nothing bound that paragraph to the bytes in the tree.
**What changed:** the check reads the file's CONTENT fingerprint — git's own blob hash of the working
tree, shortened to 12 hex digits, so it is recomputed with `git hash-object <path>` and needs no
second tool — and requires an entry to carry `<path>@<fingerprint>`. Describing a state is what
records it; any further change changes the fingerprint and the audit asks for a description again.
**State of record.** Each line below is the state THIS protocol describes, with the entry that
describes it. Recompute a line with `git hash-object <path> | cut -c1-12` after changing the file, and
add the new state to the entry that explains the change:

- `backend/cmd/devlabd/main.go@dafe6314cfb0` — described in entry 1 (and the wave-1/2 sections).
- `backend/internal/api/api.go@1c42fc335521` — described in entries 2, 3, 4 and 11.
- `src/data/source.ts@e24f90dcbd26` — described in entry 5.
- `src/data/httpSource.ts@a68bd59da743` — described in entry 5.
- `src/types.ts@a089fa595a4f` — described in entry 5 (the session shape on the seam).

**Diff hint:** `git diff c98d4a9 -- tools/abnahme.sh`, section "the contract-state fingerprint".
**Proof:** `tools/abnahme.sh` check `harness-contract` — it runs the matcher against a body with a
stale fingerprint (must fail), the current one (must pass) and no mention at all (must fail), so the
hole cannot reopen silently.

### `backend/internal/executor/executor.go` — the examined stand joins the ONE Deps seam

**Who:** repair wave 3 (T3). `executor.go` is not on the §0.2 freeze list (`chain.go` is), and neither
are `stages.go`, `prompt.go` or `mercury/compose.go`; this entry is recorded because it widens a SEAM
that every implementation of `executor.Deps` must now satisfy.
**Reason:** `runs.AxiomChecks` (the pool that holds WHICH COMMIT a repository was last examined
against, per axiom) and `mercury.RepoScopeSection` (the renderer that names it) both existed and both
had unit tests — and neither had a caller outside its own test. `executor.AssemblePrompt` never
appended the section, so every prompt fell back to "never examined ⇒ examine the whole repository",
every night, at maximum reasoning effort. The examined stand is what the owner asked for by name; it
is the reason the word "Checkpoint" was rejected for it.
**What changed:** `executor.Deps` gains `AxiomScope(ctx, repo, run) string` and
`RecordAxiomScope(repo, run, commit, at) error`. `AssemblePrompt` takes the section as a fourth
argument and appends it after the snapshot; the implement stage reads it before the agent starts and
records the workbench head afterwards, on the success path only. The production join lives in
`backend/internal/api/exec_axiomscope.go`; the integration fixture delegates to it, because the join
is not an I/O edge. `mercury.RepoScopeSection` gains a third argument, the NAMED reason the pool could
not be read, so an unreadable record is a stated gap and never the claim "never examined here" — the
same distinction REQ-001.3 draws for an unread corpus. `mercury.LastCheck.Titel`, which no renderer
ever read, is gone.
**Consequence for operation:** the pool is keyed by the run target's repo id
(`<state root>/mercury/axiom-checks.json`). A damaged file is set aside with its timestamp and named
in the prompt; nothing has to be migrated.
**Diff hint:** `git diff c98d4a9 -- backend/internal/executor backend/internal/mercury/compose.go backend/internal/api/exec_axiomscope.go`.
**Proof:** `backend/internal/executor/axiomscope_test.go` (the motor asks per repository, the prompt
carries it, the stand is recorded, a failed write is named and not fatal),
`backend/internal/api/exec_axiomscope_test.go` (first run ⇒ full pass; second run ⇒ scoped to the
recorded commit; damaged pool ⇒ named gap), `backend/internal/mercury/compose_test.go`
(`TestRepoScopeSectionNamesAnUnreadableRecord`).

### Two acceptance criteria become measurable (`sudo-a`, `REQ-040h`)

**Who:** repair wave 3 (T3). Neither is a frozen-file change; both are recorded because they add a
REQUIREMENT the delivery is measured against from now on.
**Reason:** two invariants existed only as prose. `deploy/devlab-exec` states that
`DEVLAB_STATE_DIR` and `DEVLAB_WORKSPACES` may NEVER be added to the `env_keep` list — a service that
could set them would choose the very boundary the wrapper exists to hold — and adding both to
`deploy/devlab.sudoers` passed every check in the audit. And a hand-rolled Basic-auth encoder in
`backend/internal/axiomrepo/store.go` was dead shipped code whose only caller was the test proving
the credential is NOT on argv.
**What changed:** `sudo-a` reads every `deploy/*.sudoers` template, folds its continuations and holds
each `env_keep` name against an ALLOW-LIST (`DEVLAB_GH_TOKEN GIT_TERMINAL_PROMPT`) — "nothing else may
join this list" is what the file itself says, and a deny-list would need extending for every new seam.
`REQ-040h` extends REQ-040.5 one level below the package: an unexported function the shipped sources
mention exactly once is reached from nowhere, so the binary carries code that exists to be asserted
about. The encoder was removed and its test now builds the forbidden encoding itself.
**Diff hint:** `git diff c98d4a9 -- tools/abnahme.sh backend/internal/axiomrepo/store.go`.
**Proof:** `tools/abnahme.sh` checks `sudo-a`, `REQ-040h`, `harness-keep` and `harness-dead` (both
instruments are run against samples with a known answer, so neither can pass on air).

### B-13 and B-43 are measured in BOTH languages

**Who:** repair wave 3 (T3).
**Reason:** the two token criteria had opposite blind spots. "No token on a command line" searched
shell files only, so the Go clone that spliced the credential into the remote URL was invisible to
it; "no token in a log line" searched Go files only, so a shell echoing the variable was invisible to
that one. Each check was green because it could not see the language its own leak lived in.
**What changed:** both criteria run over the Go and the shell inventory, with the pattern each
language needs — the same letters mean opposite things in the two (`"$DEVLAB_GH_TOKEN"` inside a Go
string is the tokenless credential HELPER; in a shell script it is the value). `two_absent` measures
one criterion across both sets and reports once, so passing one half can never read as green.
**Diff hint:** `git diff c98d4a9 -- tools/abnahme.sh`, section "the token-exposure matchers".
**Proof:** `tools/abnahme.sh` check `harness-token`: five known exposures (three Go, two shell) must
be caught and the deliberate constructions — the value in the environment, the inline credential
helper, the Authorization header, the redaction — must not be.

### `backend/internal/axiomrepo/store.go` — the clone disarms symlinks, the resolution excludes `.git`

**Who:** repair wave 3 (T3). Not a frozen file; recorded because it changes how the constitution
clone is made, which every existing clone on a host is measured against after the update.
**Reason:** the confinement had two halves and they drew different lines. The string half rejects a
`.git` path segment; the resolution half only required the resolved path to stay UNDER the clone —
and the clone's own `.git` is under the clone. A committed link `axiome/x -> .git/config` therefore
resolved "inside" and was served as a record (remote URL included), and a write through it replaced
the config. The enabler was the clone itself: it was made without `core.symlinks=false`, while the
per-user git primitives one package over set exactly that, with the reason written next to it.
**What changed:** every git invocation of the store — the clone and each later command — carries
`-c core.symlinks=false`, so a committed symlink is checked out as a plain file holding the link text
(the flag persists into a new clone's config; passing it per invocation is what covers a clone made
before this change, whose refresh must not re-materialise a link the remote still holds).
`resolveInClone` additionally refuses any path that resolves to the clone's `.git` or into it.
**Consequence for operation:** none to perform. A clone made earlier keeps its materialised links on
disk; the resolution half refuses them, and the next `git checkout` of an affected path replaces them.
**Diff hint:** `git diff c98d4a9 -- backend/internal/axiomrepo/store.go`.
**Proof:** `backend/internal/axiomrepo/store_confinement_test.go`
(`TestCommittedSymlinkNeverMaterialises` — a link on `.git/config` and one out of the repository,
neither materialised, neither read, neither written through;
`TestResolutionRefusesTheClonesGitDirectory` — the same boundary held by resolution alone, in the
state an older clone is in).

### `deploy/devlab-install` — the namespace measures every name, and the reservation list stops blocking the organisation's own services

**Who:** repair wave 3 (T2). The wrapper is not on the §0.2 freeze list; this entry is recorded because
it changes WHICH deliveries are accepted — in both directions.
**Reason (the false positive):** the reservation list held names that belong to the operating system's
passwd database but are also perfectly good service names. `mail` is one, and the deployment this cutover targets runs a
mail service under exactly that name (`mail.service`, `ExecStart=/opt/mail/bin/maild`), so the list made
an existing service permanently undeliverable — and it did so on the SPELLING of the name, before the check that asks whose
repository this actually is ever ran. The service-account judgment made it worse: it refused any name
whose account exists with a home outside `/var/lib/<repo>`, which is true of every service whose account
predates DevLab, on every install rather than only where an account is about to be created.
**Reason (the hole):** the whole cascade was wrapped in `if [ "$REPO" != "$SELF_REPO" ]`, so the SELF
name — the most privileged one, whose install writes `/usr/local/bin/<unit>` and `rsync --delete`s the
served web root — was exempt from all six narrowings. Any staged artifact anywhere under the workspace
root could be installed as this service's own binary; the dry run of the old wrapper installs `svc-a`'s
artifact as `devlabd` without a word.
**What changed:**
* The reservation list carries a stated RULE: an identity of the operating system, of a third-party
  package, or of the landscape as a whole — something that can appear on the host without anybody's
  decision. Names the organisation can legitimately give a service (`mail`, `news`, `list`, `backup`,
  `proxy`, `sync`, `admin`, `operator`, …) are gone from it; what decides there is the origin check, the
  unit check, and the account check at first-time setup.
* The account judgment now runs only where it DECIDES something: at first-time setup, which runs
  `useradd` and writes `User=<repo>`. On an update the unit is root-owned and its `User=` was settled
  when it was written, so nothing is adopted.
* Name (5a), staged-artifact identity (5b) and organisation (5c) apply to EVERY repository including the
  self repo. 5b additionally requires the artifact directory to BE the runner's `.mercury-artifact` (the
  fixed name `deploy.ArtifactDirName` produces), so an arbitrary directory in the user-writable
  workspace is no longer an artifact.
* The ONE named limit: where the self checkout presents no git config at all (a first bootstrap from a
  directory that is not yet a clone) the origin cannot be read, and the identity rests on 5b. Where it
  DOES present one, the self repo is held to it exactly like a foreign one.
**Consequence for operation:** a service of the organisation whose account or unit predates DevLab is
deliverable again — as an UPDATE, i.e. once its unit exists in `/etc/systemd/system`. Its first-time
setup is still refused while a foreign account holds the name; the operator creates account and unit,
and delivery takes over from there.
**Diff hint:** `git diff c98d4a9 -- deploy/devlab-install`.
**Proof:** `backend/internal/deploy/wrapper_selfname_test.go` — `mail` is deliverable as an update and
never for its spelling, `root`/`sshd`/`caddy` stay refused, a foreign artifact under the self name is
refused, a self checkout of another organisation is refused, a directory that is not the staged artifact
is refused, and the self path fails closed without a configured organisation. The existing
`wrapper_namespace_test.go` keeps the first-time account refusal.

### `deploy/devlab-install` — a route is validated before the SHARED edge adopts it

**Who:** repair wave 3 (T2).
**Reason:** first-time setup wrote `<caddy conf.d>/<repo>.caddy` into the directory the edge serves
EVERY service of this host from, then reloaded, then SWALLOWED the reload's failure
(`|| log "caddy reload unavailable"`). An unparseable file of ours therefore did not break our route —
it broke the edge for all sixteen services at the next reload, and the install still reported success.
**What changed:** the assembled configuration is validated (`caddy validate --config <main>`) before the
route is adopted; a failing validation or a failing reload REMOVES our own file again and fails the
install with a named reason (exit 4). Where the edge cannot be validated at all (no binary, no main
config) the wrapper refuses to write into the shared directory rather than guessing (exit 5). The
`--check` plan states all three decisions.
**Consequence for operation:** on a host whose edge is not Caddy, or whose main config lives elsewhere,
set `DEVLAB_CADDY_MAIN` / `DEVLAB_CADDY_BIN` in the wrapper's environment defaults — otherwise
first-time setup of a foreign service refuses to touch the shared route directory.
**Diff hint:** `git diff c98d4a9 -- deploy/devlab-install`.
**Proof:** `backend/internal/deploy/wrapper_selfname_test.go`
(`TestInstallRouteIsValidatedBeforeTheSharedEdgeAdoptsIt`).

### `backend/cmd/devlabd/main.go` + `backend/internal/api/handlers_mercury_deliveries.go` — the protection pass is held until the operator arms it

**Who:** repair wave 3 (T2). `main.go` IS on the §0.2 freeze list, hence the state line below.
**Reason:** `verifyProtectionLoop` ran its first pass the moment the process came up, and a pass PATCHes
the default branch of every repository of the configured organisation that deviates. That is the only
effect this daemon has outside DevLab, it reached repositories no cutover asked about, and on a restart
loop it happened once per restart — none of it named in the cutover runbook.
**What changed:** the loop waits `DEVLAB_RUNS_PROTECTION_START_DELAY` (default 15m) BEFORE the first
pass, so a boot alone reaches nothing and a stop inside the window costs nothing; and writing is off
until `DEVLAB_RUNS_PROTECTION_ENFORCE` is set affirmatively. Unarmed, the pass READS every repository
and REPORTS what deviates — into the notice pool and hence the daily report — in words that say the
deviation was not changed. `deliver.VerifyProtection` stays the ONE judge of what "satisfied" means: the
hold is a wrapper over its ops whose single writing call answers a named refusal, not a second
implementation of the rule. The pass also resolves the repository set through the same seam every other
surface uses (`runnerRepoSet`) instead of calling `discover.ReposForUser` a second time.
**Consequence for operation:** REQ-033.7's finding-and-recording half runs from the first pass; the
restoring half is armed deliberately, after the reported findings have been read. The cutover names both
switches (section "Fremdwirkung" of `00-cutover.md`).
**State of record:** `backend/cmd/devlabd/main.go@dafe6314cfb0` — this entry describes it.
**Diff hint:** `git diff c98d4a9 -- backend/cmd/devlabd/main.go backend/internal/api/handlers_mercury_deliveries.go`.
**Proof:** `backend/cmd/devlabd/main_test.go` (no pass inside the delay; a cancelled boot performs none
at all; the default delay is a real waiting period) and `backend/internal/api/protection_hold_test.go`
(unarmed: every repository read, none written, the finding recorded honestly; armed: restored; only an
affirmative value arms it).

### `deploy/devlabd.service` — `/tmp` is writable by decision, not by drop-in

**Who:** repair wave 3 (T2).
**Reason:** the template pairs `ProtectSystem=strict` with `PrivateTmp=false`, and strict makes /tmp
read-only as well. Every per-user child this service starts through `devlab-exec` writes there — the
claude CLI's scratch (`/tmp/claude-<uid>`), `go build`, `npm run build` — so the AI panel and every
artifact build would fail with EROFS. The live instance only worked because a drop-in carried
`PrivateTmp=true`, i.e. the template's isolation decision was being silently overridden by
configuration, and the cutover's step 2 left that line untouched.
**What changed:** `ReadWritePaths=/var/lib/devlab /tmp`. The per-user scratch lives in the REAL /tmp, the
same one the user's own session and the terminal service see; the isolation this service relies on stays
the OS user, not the mount namespace. The cutover removes `PrivateTmp=true` from the drop-in.
**What changed (environment contract):** the template documents the two renamed and the one retired
run variable it had not carried a note for (`DEVLAB_RUNS_AUTOMERGE` → `DEVLAB_RUNS_AUTOMERGE_WINDOW`,
`DEVLAB_RUNS_LIMIT_MAXRESUMES` → `DEVLAB_RUNS_LIMIT_MAX_RESUMES`, `DEVLAB_RUNS_AGENT_TIMEOUT` removed),
plus the two new protection switches.
**Consequence for operation:** the drop-in's `PrivateTmp=true` must go, and the two renamed variables
must be renamed — under their old names nothing reads them, so a usage-limit cap and an auto-merge
window silently fell back to their defaults. `00-cutover.md` treats the drop-in line by line.
**Diff hint:** `git diff c98d4a9 -- deploy/devlabd.service deploy/migration/00-cutover.md`.
**Proof:** `backend/it/cutover_runbook_test.go` (`TestUnitKeepsTheScratchDirectoryWritable`,
`TestCutoverDropInTableAgreesWithTheShippedEnvironmentContract` — every kept variable is one the shipped
code reads, every retired one is read by nothing, and every retired one carries a note in the template).

### `deploy/devlab-mkworkspace`, `deploy/devlab-deploy-recv` — the third and fourth definition of the state root are gone

**Who:** repair wave 3 (T2).
**Reason:** both scripts hard-coded absolute state paths (`/var/lib/devlab/workspaces`,
`/var/lib/devlab/staging`, `/var/lib/devlab/www`) while `devlab-install`, `devlab-exec` and
`internal/statepath` derived theirs. An instance with a different state root got a helper provisioning
one root, a daemon confining against another, and a prod receiver staging where nothing is served from.
**What changed:** both carry the ONE shared derivation (`STATE_DIR="${DEVLAB_STATE_DIR:-…}"`) and derive
what they need from it — the workspaces root in `mkworkspace`, the staging and web roots in
`deploy-recv`. The audit that pinned the single derivation for two scripts now covers all four.
**Diff hint:** `git diff c98d4a9 -- deploy/devlab-mkworkspace deploy/devlab-deploy-recv`.
**Proof:** `backend/internal/deploy/wrapper_namespace_test.go`
(`TestWrappersShareOneWorkspaceDerivation`, extended to all four scripts, plus the pattern that refuses
an absolute literal in any root variable).

### `deploy/migration/10-daten.md` — "the import only adds" no longer holds

**Who:** repair wave 3 (T1). `10-daten.md` is not on the §0.2 freeze list, so this entry is not owed
as a frozen-contract change — it is recorded here because it **withdraws a promise** the cutover
document made ("The import only adds — it deletes nothing"), and an operator who plans a rollback
around the old wording would plan around a rewrite that does happen.
**Reason (measured against the real state directory, not argued):** a pre-rebuild run record and a
rebuilt one carry the SAME `id` and different field names — `type/name/enabled/prompt/promptAt/
lastFiredAt/lastResult/done` against `kind/title/active/promptSnapshot/authorship`. Two consequences
followed, and both were reproduced on a copy of a live `mercury/` before anything was changed:

1. `json.Unmarshal` into the rebuilt run record turned all 63 pre-rebuild records into runs with an
   **empty kind and an empty title** — the service would have come up showing 63 nameless runs.
2. The idempotence check compared **ids**, so every one of those records answered "already
   imported". Run against the actual cutover situation, the import reported
   `already present — runs 8 · history entries 6 · protocol items 0` and imported **nothing**. It
   worked only against an empty directory — that is, only in a situation no cutover is ever in.

Three further pools were measured with the same question. `runs-deliveries.json` recorded a status
WORD while the rebuilt record expresses merged and closed as TIMES, so all 15 records read as
**open** — and an open delivery is what the next pull request stacks on and what the preflight
reports as an outstanding arrival. All 158 config snapshots in `runs-history/` held pre-rebuild run
sets, so a single restore would have written 63 nameless runs back into the pool. Three files
(`runs-settings.json`, `runs-incidents.json`, `runs-active`) have no rebuilt reader at all.
`runs-prs.json`, `runs-notices.json` and `runs-results/` were measured to read as they lie and are
**not** touched.
**What changed (behaviour):** the import now classifies every record by its **shape** instead of its
id. Only a record in the rebuilt form counts as already imported; the pre-rebuild stock is copied
verbatim to `<pool>.pre-migration` and the pool is then **replaced** with the rebuilt records plus
the imported ones. The delivery ledger is converted through its own write path (status word → time,
`resultId` → execution reference; the outcome time is the delivery's creation time because the
source carried no second timestamp, and a converted closed delivery says so in its closing reason).
Pre-rebuild config snapshots and the reader-less pools are moved aside. A record in NEITHER shape is
never interpreted: it is set aside and named with its find location. A pool file that is unreadable
as a whole aborts the import (exit `5`) and is left untouched. Nothing is deleted, and a repeated
takeover never overwrites an earlier copy (`.pre-migration.2`, `…3`).
**Consequence for operation:** `10-daten.md` gains the takeover table, the three post-import checks
and a by-hand rollback that names every `.pre-migration` artifact. The old rollback sentence is
gone. The state tarball from step 0 of `00-cutover.md` remains the primary rollback and is now
load-bearing rather than belt-and-braces.
**Diff hint:** `git diff c98d4a9 -- backend/cmd/devlab-migrate deploy/migration/10-daten.md`.
**Proof:** `backend/cmd/devlab-migrate/takeover_test.go` — the cutover situation itself
(`TestImportOverPreRebuildRunPoolCarriesTheStateOver`), byte-exact idempotence over the whole state
tree (`TestSecondRunOverTheTakenOverDirectoryWritesNothing`), forms told apart rather than ids
(`TestIdempotenceDistinguishesTheFormsNotTheIDs`, `TestFormOfDecidesByMarkersAndRefusesToGuess`),
uninterpretable stock set aside and named (`TestUndecidableRecordsAreSetAsideNamedNotInterpreted`,
`TestUnreadableRunPoolAborts`, `TestSetAsideNeverOverwritesAnEarlierCopy`), and one test per further
pool against a fixture derived field-by-field from the real file
(`TestLegacyDeliveryLedgerReadsEveryRecordAsOpenUntilItIsConverted`,
`TestUnknownDeliveryStatusIsRefusedByName`,
`TestPreRebuildConfigSnapshotsAreSetAsideAndRebuiltOnesStay`,
`TestLegacyPendingPRPoolNeedsNoConversion`, `TestLegacyNoticePoolNeedsNoConversion`,
`TestLegacyArchiveRecordWithTheRealWorldTraitsIsRead`,
`TestPoolsWithoutAReaderAreSetAsideWithTheirReason`).

---

## Repair wave 4

### `deploy/devlabd.service` — the address is an environment value, and one instance value returns to the drop-in

**Who:** repair wave 4 (U1). The template is not on the §0.2 freeze list; this entry is recorded
because it names two REQUIREMENTS the cutover must satisfy, and a cutover that misses either changes
observable behaviour without failing.
**Reason (measured against the installed unit, read-only, `systemctl cat devlabd`):** the running
instance carries two values in the UNIT — not in the drop-in — that step 2 of `00-cutover.md`
overwrites wholesale, and the drop-in table treated only the drop-in:

1. `DEVLAB_COOKIE_DOMAIN`. It is read by the shipped code (`api/cookies.go`) and deliberately absent
   from this template because it is an instance value. Lost, the session cookies this service
   re-mints on refresh become HOST-ONLY and stand beside the landscape's domain-wide ones instead of
   overwriting them — which is the exact duplicate the code's own comment exists to prevent, and a
   logout at the landscape no longer clears the host-only copy.
2. The listen address. The old unit passed it as `ExecStart=… --listen <addr>`; the rebuilt binary
   parses NO command-line flags and reads `DEVLAB_ADDR` alone, so a carried-over flag silently does
   nothing and the process listens wherever `DEVLAB_ADDR` points. If that is not the address the
   shared edge proxies to, every request to the service fails while the unit reads as active.

Additionally `DEVLAB_REPOS_PATH`, which the old unit set to an operator home, is now only the
dev-bypass sandbox base — carried over it aims repository resolution at foreign working copies
instead of the per-user workspaces under the state root.
**What changed:** three notes in the template's environment-contract block (the address, the cookie
domain, the sharpened `DEVLAB_REPOS_PATH` warning) plus the commented instance-value line for
`DEVLAB_COOKIE_DOMAIN`, in the same form `DEVLAB_GH_OWNER` already uses. No directive and no
`Environment=` line changed value, so no instance behaviour changes from the template alone.
**Also recorded here:** `DEVLAB_RUNS_MAINTAIN_ENFORCE` — the arming switch of the pull-request
maintenance, added to `internal/deliver` in this same wave — joins the environment contract and the
commented arming block beside `DEVLAB_RUNS_PROTECTION_ENFORCE`. An arming switch that no template
documents is a switch nobody sets: the drop-in table of `00-cutover.md` carries its row, and the
table's own audit passes only because the shipped code reads exactly that name.
**Consequence for operation:** `00-cutover.md` gains the table „Die Alt-Unit, Zeile für Zeile" beside
the existing drop-in table, the drop-in table gains the `DEVLAB_COOKIE_DOMAIN` row, and step 2 checks
all three: the cookie domain must be present, the four derived paths must be absent, and `--listen`
must be gone from `ExecStart` while `DEVLAB_ADDR` matches the edge.
**Diff hint:** `git diff 89ee3da -- deploy/devlabd.service deploy/migration/00-cutover.md`.
**Proof:** `backend/it/cutover_runbook_test.go`
(`TestCutoverDropInTableAgreesWithTheShippedEnvironmentContract` holds every row of the drop-in table
to the shipped environment contract — the new `DEVLAB_COOKIE_DOMAIN` row passes only because the code
reads that name), plus the two probes the step itself performs.

### `deploy/migration/00-cutover.md` — the first start is HELD, and the foreign effect is the whole list

**Who:** repair wave 4 (U1).
**Reason:** the runbook claimed exactly one thing reaches outside DevLab (the branch-protection pass).
Read against the code that is wrong by an order of magnitude, and the difference is not academic:
`deliver.Maintain` is driven by the scheduler's tick (`sched/sched.go:246 s.runMaintain`), so from the FIRST tick
after the first start it posts a delivery-origin status on every open pull request of every managed
repository — hand-raised ones included — deletes the delivery branch of any tracked pull request a
human merged meanwhile, and merges whatever has passed its window. The daily reporter performs a pass
BEFORE its first interval, so a mail can leave the host seconds after the start; and `api.New` starts
the constitution README seed as a background task, which clones the constitution repository and may
push one commit. Measured on the real pre-cutover state directory: 61 tracked pull requests in 21
repositories (5 blocked), 13 open ledger deliveries in 12 repositories, and one daily-report record
still in `failed` with 46 attempts — which, having no backoff, is due again on the first pass.
**What changed (no code):** the section „Fremdwirkung" now lists **fourteen** writing sites with the
code position of each, whether it fires at the first start, what it writes and how it is held. The
sequence gained the hold that follows from it: step 6 starts the daemon WITHOUT the runner identity
(`DEVLAB_RUNS_USER`, `DEVLAB_RUNS_TOKEN_USER`), which makes `runnerToken()` answer "no runner account
configured" — so PR maintenance, the protection pass, the chain, the reporter and the README seed all
fail closed with a named error — step 6a MEASURES what the next tick would touch, and step 7 sets the
identity and restarts. Step 0 now saves every artefact the cutover overwrites or removes (the four
shared wrappers, both sudoers files, binary, unit, drop-in) with its path preserved, and the rollback
restores each one in reverse order. Step 4 delivers the web root without rewriting its owner and mode
(`rsync -a` transfers the SOURCE directory's owner and mode onto the target; measured in a sandbox:
`drwxr-x---` became `drwxrwxr-x`) and proves the target unchanged. Step 5 stages the export where the
service account can read it, proves the readability before the import, and removes the staging.
**Consequence for operation:** the cutover is two starts, not one, and the acceptance walk of
`20-sichtpruefung.md` happens after step 7. The hold the ORDER provides is a property of the
procedure, not of the software; a switch that would make it one belongs to the owner of
`internal/deliver` and `internal/api`, so the runbook does not assert its absence — it READS whether
the installed binary and the template carry one (the `strings`/`grep` probe under the table) and says
what follows in either case.
**Diff hint:** `git diff 89ee3da -- deploy/migration/00-cutover.md`.
**Proof:** the eight assertions of `backend/it/cutover_runbook_test.go` still hold (backup complete
against the derived state layout, sudoers validated before installing, organisation file provisioned,
health probe derived from the configuration, `sudoedit` only, drop-in treated line by line, protection
hold named); every new claim carries its own probe in the step that makes it — the backup lists what
it saved and names what was absent, the web root is compared before and after, the export's
readability is asserted before the import runs, and the held start is proven by the three log lines
that say the passes had no runner account.

### `deploy/migration/10-daten.md` — two more pools have a reader, and the import has a precondition

**Who:** repair wave 4 (U1).
**Reason:** the takeover table surveyed every pool of the pre-rebuild state directory except three
that DO have a reader in the rebuild. `mercury/daily-reports.json` is the load-bearing one: the
rebuilt ledger reads it field for field, and a record left in `failed` without a backoff is due again
on the reporter's very first pass, whatever its age — so the first start re-attempts a send the old
instance already failed, and a day inside the lookback window is reported for work the OLD instance
did. `mercury/axiom-checks.json`, `mercury/axiom-authors.json` and `mercury/attachments/` were
measured to read as they lie and need no conversion; saying so is what keeps the next reader from
converting them. Separately, the import's own command was not executable as written: it ran as the
service account against an export in the operator's home, where that account cannot even traverse
(`0750`), so the documented line ended in exit `5`.
**What changed:** three rows in the takeover table, and an import block that stages the export into a
directory the service account owns, PROVES the readability before the import runs, and removes the
staging afterwards (the raw export carries this instance's prompts). The binary is invoked by name
because the cutover now installs it — running it out of `/tmp` made the import depend on the umask of
the account that built it. M4 states that the startup reconciliation is DEFERRED at the held first
start and runs at the restart of step 7; step A states that it must follow step 7, because the
constitution store resolves its token through the same runner identity.
**Diff hint:** `git diff 89ee3da -- deploy/migration/10-daten.md`.
**Proof:** the readability precondition and the staging removal are probes in the step itself; the
three pool rows were measured against the real state directory (shape comparison against
`report.Record`, `runs.AxiomChecks`, `axiomauthors.Author` and the attachment path layout), and the
"due again" claim is the shipped `due()` in `report/reporter.go:262 due`, which returns true for a failed
record with no backoff.

### `deploy/migration/20-sichtpruefung.md` — the inspection kinds are the matrix's own, and the open work is one list

**Who:** repair wave 4 (U1).
**Reason:** the document bound two claims to a wave name rather than a commit, and its legend called
every open item an inspection "at the running instance" — which is wrong for the matrix's `Review`
kind: reading file permissions, a module cut or an invariant checklist needs no running service, and
lumping it in with a measurement hides which work can start before the daemon is up. The document
also never said WHICH lines the operator still has to walk, so the open half of the acceptance had no
list.
**What changed:** the two corrections now name commits (`c98d4a9`, `89ee3da`); the legend spells the
three kinds the matrix uses (`Sichtprüfung`, `Messung`, `Review`) and states that a review needs no
running instance; and a working list carries all 65 open lines grouped by kind — 15 E2E, 8
measurements, 37 looks, 22 reviews, of which 11 need nothing but a review — with the two grep commands
that recompute the totals from the table itself.
**Diff hint:** `git diff 89ee3da -- deploy/migration/20-sichtpruefung.md`.
**Proof:** the kinds were derived by comparing every row against the "Wie geprüft wird" cell of the
same line in the acceptance matrix; the comparison reports zero disagreements in either direction.
`tools/abnahme.sh` check `doc-a` (cited check ids exist, section sizes match) and
`backend/it/cutover_runbook_test.go` (`TestInspectionResultRowIsBoundToACommit`,
`TestInspectionReportsE2ELinesAsE2E` — the working list sits outside the enumeration block that test
parses, so it cannot dilute the E2E claim) both hold.

### `backend/cmd/devlabd/main.go` — the restart gate is a boot precondition, and the boot names the maintenance hold

**Who:** repair wave 4 (U2).
**State of record:** `backend/cmd/devlabd/main.go@62ed7ea56be4` — this entry describes it (entry 1
describes the boot order it extends).
**Reason (measured):** the readiness socket was started in a goroutine whose only reaction to a
failure was `log.Printf`. That socket is not a status line: it is the interlock `devlab-migrate`
takes. Measured against the shipped binary — with the socket up the migration refuses with exit `10`
while an execution runs; with no socket at all there is NO refusal, because a daemon that does not
answer reads as dead and therefore as "nothing running". A daemon that had lost its gate went on
serving, and the next import would have worked a LIVE state tree with two writers on it.
**Decision:** refuse the start, rather than carrying the lock elsewhere. A second lock path (a pid
file, a unit query) would be a parallel access point to the same truth, and the gate must be
answerable for the migration to take it at all — so the honest rule is that the daemon does not serve
what it cannot protect. This is symmetric with the two boot preconditions above it: an unwritable
state root and an unreadable session secret already refuse the start.
**What changed:** the gate goes up BEFORE the HTTP listener. `awaitGate` waits until the socket
ANSWERS a dial, or until the attempt to bring it up reports its failure, or until
`DEVLAB_READY_GATE_TIMEOUT` (default 10s) is over; the first two are named, the third is named as
silence, and each refuses the start. A gate lost LATER brings the daemon down through the existing
drain (a running execution is still persisted as interrupted, K-2) and exits non-zero. The boot
additionally states in ONE line whether the pull-request maintenance starts armed or held (see the
`deliver.go` entry below), the way it already states the self-check and the reporter.
**Diff hint:** `git diff 89ee3da -- backend/cmd/devlabd/main.go`.
**Proof:** `backend/cmd/devlabd/main_test.go` — a gate that comes up late is awaited, a bind failure
is handed back verbatim, and silence refuses within its budget. Probe against the built binary: with
the socket path blocked the process exits `1` with `refusing to serve without the interlock …` and
answers nothing on its HTTP address; with a free state root it logs `pull-request maintenance HELD`,
then `listening on …`, and `GET /ready` over the socket answers `204`.

### `backend/internal/deliver/deliver.go` — the pull-request maintenance stands still until the operator arms it

**Who:** repair wave 4 (U2). `deliver.go` is not on the §0.2 freeze list; this entry is recorded
because it changes the SHIPPED DEFAULT of an effect outside DevLab.
**Reason:** `deliver.Maintain` merges pull requests, deletes their branches and writes commit statuses
in other organisations' repositories, and the scheduler drives it from the FIRST tick after boot —
over a pool that a cutover imported minutes earlier and that nobody has looked at. That is the one
effect no restart may cause on its own; the protection check next to it was already held for exactly
this reason (`DEVLAB_RUNS_PROTECTION_ENFORCE`, entry above).
**What changed:** `DEVLAB_RUNS_MAINTAIN_ENFORCE` arms the writing half; unarmed — the shipped default
— `Maintain` performs an OBSERVATION pass: it reads its own pools, makes no GitHub call at all (so it
needs neither token nor network), changes none of its own records either, and raises ONE notice
(`delivery-held`) naming the standstill and the switch that ends it. It stays silent while nothing is
waiting. Holding the pass whole is deliberate: a pass that mirrored half its findings and held back
the other half would leave a mixed state nobody can reason about, so the state the migration produced
stays exactly as it is until the operator arms it, and the first armed pass then works the queue in
creation order. The word set of the switch is parsed by ONE function (`deliver.ArmedByEnv`), so the
two holds cannot disagree about what "armed" means.
**Visible where it matters:** the notice feed labels the kind (`Maintenance held`), the daily report
carries it in the delivery-alarm rubric, the Deliveries surface shows the service's own sentence with
the moment it was last reported, and the boot log names the state.
**Diff hint:** `git diff 89ee3da -- backend/internal/deliver/deliver.go`.
**Proof:** `TestMaintainHeldWritesNothing` (unarmed: zero calls of any kind on the fixture GitHub,
ledger/PR pool/result untouched, the notice raised with the switch in it, a second pass bundled into
one row; armed: the same situation merged and pruned) and `TestMaintainHeldStaysSilentWithNothing-
Waiting`. Every pre-existing test that exercises the writing half now arms it explicitly through
`armMaintain(t)` / `t.Setenv(deliver.EnvMaintainEnforce, "1")` — the default is off, and the suite
says so.

### `backend/internal/runs/results.go` — an archived execution has ended, and nothing in it is running

**Who:** repair wave 4 (U2).
**Reason (measured against the imported archive, 82 records):** the mapping left `EndedAt` nil for a
zero `finishedAt` (14 records) and carried a step recorded as `running` verbatim (4 records). Both are
claims the stock does not cover. The history selector drops an execution that never ended while the
history's counter counted every record it read — measured on the imported stock: `History (65) · 17
still open` with an EMPTY Active list, so an entry that exists was invisible and a number nobody can
point at was claimed. And a `running` stage made the surface pulse for a repository where nothing
runs, which is the ghost REQ-039.1 exists to remove.
**What changed:** the archive is read as the closed past it is. A step recorded as running is closed
as ABORTED — a terminal state whose mandatory reason names what the archive recorded and that the
outcome is unknown. A record without a finishing time is ENDED at the last instant the record itself
carries (its own start when it carries no later one), and it SAYS so: the result's report states which
instant stands in for the missing stamp. Where the source additionally flagged the record as not ok,
every repository carries one `archived-cutoff` stage (`not-executed`, with its reason) stating that
nothing followed the last recorded step — otherwise a chain cut off after `implement` would read as a
completed success (K-4). A record the source flagged as ok keeps its recorded success: a false failure
is as much a lie as a false green. Nothing is invented beyond the record: no time is claimed that the
document does not carry, and the moved-aside archive keeps every original verbatim.
**Consequence for the surface:** `src/views/mercury/tasks/select.ts` gains `outsideHistory`, which
splits what the history does NOT show by the reason that keeps it out — running (shown in Active) or
awaiting delivery (shown in the ledger) — and `ExecutionsView` renders those two named numbers instead
of subtracting the history from the pool. Measured after the change: all 82 archive records carry an
end, no stage is transient, and the counter claims nothing.
**Diff hint:** `git diff 89ee3da -- backend/internal/runs/results.go src/views/mercury/tasks/select.ts`.
**Proof:** `TestArchiveRecordWithoutFinishingTimeIsEndedAndCounted` over the four forms the imported
archive actually holds among its unstamped records (instance-neutral fixtures: cut off mid-step, all
recorded steps ended, flagged ok with complete chains, no repository at all) plus the invariant "every
archive record ended, holds nothing transient and states where its end came from";
`TestLegacyResultsReadTolerantly` and `backend/it/legacy_test.go` for the aborted step;
`TestLegacyArchiveRecordWithTheRealWorldTraitsIsRead` for the imported document; and
`src/views/mercury/tasks/select.test.ts` for the partition (history ∪ running ∪ awaiting = the pool,
each record in exactly one place).

---

## Repair wave 5

**No frozen file changed in this wave.** Every entry below is recorded for the same reason the
earlier waves recorded theirs: it changes what the delivery CLAIMS about itself, or how a claim is
kept honest. `contract-a` and `contract-b` stay green because the §0.2 list was not touched.

### `backend/internal/report/selfcheck.go` — the self-check says nothing about a past it cannot read

**Who:** repair wave 5. Not a frozen file; recorded because it is a display that contradicted the
stock, in a file no earlier repair had opened.
**Reason (measured on the real archive, 82 records):** the delivery self-check compares stages
STRICTLY against the chain's vocabulary, over `ResultStore.List()`, which mixes the pre-rebuild
archive in. An archived record carries its stage names verbatim — `implement`, but `dev-deploy`
where the chain says `deliver-dev` — so `implement` matched and the delivery never did. Every
archived execution therefore read as "implemented, never delivered", and the FIRST start of a
rebuilt instance raised `changes were implemented but nothing was delivered in the last 72h` over a
past that is closed and cannot be resumed. The finding's own next step ("check the delivery path and
resume") has no addressee there at all.
**What changed:** the pass filters on the provenance the tolerant reader already records
(`Result.Legacy`) and judges only documents written in its own vocabulary. The archive is neither
translated nor judged: a translation would trade a wrong finding for a guessed one, because
"delivered" meant something else in the old system (a push plus an opened pull request, not a dev
delivery). An archived record is now evidence neither for the finding nor against it — it cannot
raise one and it cannot silence one.
**Diff hint:** `git diff 2e6a1ab -- backend/internal/report/selfcheck.go`.
**Proof:** `TestSelfCheckSaysNothingAboutTheArchive` drives ONE real archived document — instance
stripped out, its shape (per-step `ok` booleans, no `status`, exactly the four archived step names)
kept — through the PRODUCTION tolerant reader, and first asserts the premise (implement executed,
no delivery visible) so the fixture cannot silently stop reproducing the blocker.
`TestSelfCheckStillFiresOnTheSameSituationInTheCurrentVocabulary` and
`TestSelfCheckArchiveNeitherRaisesNorSilences` are the two controls.

### `backend/it/vocabulary_test.go` — the audit now sees READING a retired name, not only producing one

**Who:** repair wave 5.
**Reason:** six review rounds passed over the self-check because the vocabulary audit only ever
policed the PRODUCTION of a retired word (`TestNoSynonymForAFixedWord`,
`TestRetiredStepNamesLiveOnlyOnTheLegacyPath`), and `readsAlongsideCanonical` explicitly permits
reading one anywhere. The defect names no retired word at all — it simply never asks whether the
document in front of it is an archived one — so no check could have failed.
**What changed:** a third check, `TestEveryStageComparisonHandlesTheLegacyVocabulary`, over the
COMPARISON rather than the word. Every place that compares a stage must either consult the archived
provenance (`Result.Legacy`) or carry a `stage-vocabulary:` line stating why no archived document
reaches it; a place that says neither is a failure. Three live-path sites (`exec_record.go`
`StageUpdate`, `executor.go` `finishRepo`, `stages.go` `implementRun`) now state it; they compare
stages this execution just wrote and never open a stored document.
**Diff hint:** `git diff 2e6a1ab -- backend/it/vocabulary_test.go`.
**Proof:** a CANARY inside the test: the checker is fed the self-check exactly as it stood when the
blocker was raised and must report exactly that file. Without the canary the extended check would be
one that passes on everything, including the defect it was written for.

### `backend/internal/api/handlers_mercury_deliveries.go`, `backend/internal/deliver/deliver.go` — the standstill is reported in the configuration it exists for

**Who:** repair wave 5.
**Reason:** `MaintainDeliveries` resolved the runner token BEFORE `deliver.Maintain` and returned on
its absence. The cutover's first start (step 6) runs deliberately WITHOUT `DEVLAB_RUNS_USER` and
`DEVLAB_RUNS_TOKEN_USER`, so in exactly the configuration `reportMaintainStandstill` was written for
it was unreachable: the operator saw no sign that the maintenance stands still, although the
standstill is intended and although the delivery ledger behind it is what step 6a asks them to look
at.
**What changed:** the identity is resolved for the WRITING half only. Unarmed — the default, and the
state of the first start — the pass runs without one and reports; armed, a missing identity fails by
name rather than reporting a standstill it never attempted to end. `Maintain` states the nil-ops
tolerance in its contract and refuses it one line below the hold, so it can never reach a writing
line.
**Diff hint:** `git diff 2e6a1ab -- backend/internal/api/handlers_mercury_deliveries.go backend/internal/deliver/deliver.go`.
**Proof:** `TestMaintainReportsTheStandstillWithoutARunnerIdentity` builds exactly the step-6
configuration (no identity, unarmed, nothing injected into the ops seam) and asserts one standstill
notice AND that neither the PR pool nor the ledger moved; `TestMaintainRefusesToWriteWithoutARunner-
Identity` is its armed counterpart.

### `deploy/migration/00-cutover.md` — the foreign-effect table's anchors point at the code again

**Who:** repair wave 5.
**Reason:** six of the ten `deliver.go` anchors of the "Fremdwirkung" table had drifted onto a
`continue`, a `}` and a `default:`; the remaining four sat one line above their call. That is the
one table with which the operator is told where to READ each writing call before the first start.
**What changed:** every anchor was verified against the code and corrected, and each one now carries
the CALL it points at (`deliver/deliver.go:879 gh.PostCommitStatus`) instead of a bare line number.
**Diff hint:** `git diff 2e6a1ab -- deploy/migration/00-cutover.md`.
**Proof:** new check `doc-b` in `tools/abnahme.sh` reads every `datei:zeile <aufruf>` anchor of the
runbook parts and fails unless the named line really carries the named call — and fails on an anchor
that names no call, so the check cannot be dodged by omitting the verifiable half. Both failure
modes were provoked once before the check was kept.

### The five smaller findings of the same review

**Who:** repair wave 5.

* **`backend/internal/api/ready.go`** bound the readiness socket inside `<state-root>/.ready-<rand>/s`.
  A Unix socket path is limited to `sun_path` (108 bytes with the NUL) and the kernel answers an
  overrun with the bare "bind: invalid argument", which names neither the limit nor the state root.
  The staging prefix is now `.r`, which makes the staged path always SHORTER than the published
  `restart-ready.sock` — the staging can no longer be the thing that fails while the contract path
  would have fitted — and the length is checked before the bind and reported with the knob that
  changes it (`DEVLAB_STATE_DIR`). Proof:
  `TestReadySocketNamesThePathLimitInsteadOfBindInvalidArgument`.
* **`backend/cmd/devlab-migrate/takeover.go`** set `runs-settings.json` aside as "no reader" without
  naming the slot capacity it held. The value is deliberately NOT converted — the rebuilt
  `settings.json` is a three-field document the store reads whole, so writing it with the capacity
  alone would pin the default time budget and the automerge window at zero and switch both off — so
  it is an OPERATOR HANDLING, and the protocol now prints the number (`it held maxConcurrent = N`)
  together with the one place that carries it into the first start
  (`DEVLAB_RUNS_MAX_CONCURRENCY`). Proof: `TestTheSetAsideSettingsValueIsNamedAsAnOperatorHandling`,
  which also asserts the migration writes no `settings.json` of its own.
* **`deploy/migration/00-cutover.md`, Rollback** restored the tarball over the tree but removed
  nothing, and `tar -x` deletes nothing — so the state tree carried BOTH worlds afterwards
  (`executions/`, `runs-results.imported`, the `.pre-migration` set-asides and the pools the old
  stand does not know). The rollback now removes them by name, REPLACES the three trees the cutover
  rewrites as a whole (`mercury/runs-history`, `www`, `axioms` — each only if the tarball actually
  carries it, so the removal can never become a loss), and ends with a `find` whose output must be
  empty.
* **`deploy/migration/00-cutover.md`, step 0** verified the backup through
  `tar -tzf $BAK/devlab-state-*.tar.gz`. A second pass into the same `$BAK` would have given `tar`
  two arguments — it reads the second as a MEMBER of the first — and the backup verification would
  have failed on a backup that is fine. The tarball now has a fixed name (`$STATE_TAR`; `$BAK`
  already carries the timestamp), so a repeat replaces the one backup instead of leaving two behind.
* **`runs.json.bak-*`** — hand-made copies of the run pool that the takeover never looked at. They
  are the operator's own files: nothing writes them, nothing reads them, and no surface offers them
  as a restore point, so they are neither moved nor renamed. But copying one back over `runs.json`
  would re-inject exactly the records the takeover converted, so the protocol now names each one and
  how many still hold pre-rebuild records — which makes keeping or deleting them a decision instead
  of a surprise. Proof: `TestHandMadeRunPoolCopiesAreReportedAndLeftAlone`.

## Repair wave 6

### `backend/internal/api/api.go`, `src/data/source.ts`, `src/data/httpSource.ts` — the assignment has an access of its own

**Who:** repair wave 6 (the assignment dead end).

**Reason:** the automatic axiom→run assignment (REQ-004) was reachable from exactly ONE place —
`addAxiom` kicking it after a NEW axiom was created. Every other route into the same state existed
without a trigger, and the takeover produced precisely such a state: seven recurring runs imported
WITHOUT axioms while no run carried any axiom. From there the surface was closed: the runs needed
axioms, the axioms arrived only through the assignment, and the assignment started only from an
axiom nobody wanted to write. The coverage view said "assignment in progress…" and offered nothing
to press, which is the button-into-the-void case REQ-040.3 forbids read from the other side — an
access with no caller is one fault, a state with no access is the other.

**What changed:**

* `api.go` gains ONE route, `POST /api/mercury/runs/assign` (`guardCSRF`, handler
  `runsAssign`). It is not a second assignment path: it starts the SAME pass the axiom writes kick,
  on the caller's session, and answers with the immediate honest outcome (how many axioms are
  uncovered, whether a pass was armed) rather than a claimed success — the result itself arrives in
  the notice feed.
* `source.ts` declares `mercuryRunAssign()`, `httpSource.ts` calls the route. The caller in the UI is
  the button in the coverage view, which is the one surface that names the uncovered set.

**Consequence for operation:** none for existing callers; the route is additive. The MCP tool table
carries `run_assign` for it, so the REQ-043 parity list stays complete in both directions
(`contract/mcp-tools.json` is regenerated, not hand-edited).

**State of record:**

- `backend/internal/api/api.go@58028eec98d5`
- `src/data/source.ts@e366711a086d`
- `src/data/httpSource.ts@9ab108023c5f`

**Diff hint:** `git diff eae031b -- backend/internal/api/api.go src/data/source.ts src/data/httpSource.ts`.
**Proof:** `TestExplicitAssignmentLeavesTheDeadEnd` and
`TestExplicitAssignmentIsHonestWhenThereIsNothingToDo` (backend/internal/api), plus the standing
list-shaped suites: `backend/it/surface_test.go` (every route has a caller and every caller a route)
and `src/parity.test.ts` (every UI capability is reachable over MCP).

### `src/types.ts`, `src/data/source.ts`, `src/data/httpSource.ts` — an AI proposal is taken on, not waited for

**Who:** repair wave 6 (the AI call that broke off).

**Reason (measured):** `POST /api/mercury/runs/ai-fill` answered correctly — after 109 s. A model
call over the whole constitution is minutes of work, and the plan loop may re-ask the model, while
the hop in front of devlabd drops a connection that has been silent for about 100 s. The browser
therefore reported "failed" for work the server had finished cleanly, and the finished proposal was
lost with the connection. The seam made that inevitable: `mercuryRunAiFill()` was a request whose
ANSWER was the result, so the result could only ever travel on a connection nobody could keep
alive long enough.

**What changed:**

* `types.ts`: `RunProposal` is no longer "the plan plus its legend" but the STATE of one analysis —
  `none` / `running` / `completed` / `failed` (the contract's own words), with `id`, `startedAt`,
  `endedAt`, the named `reason` of a failure and the reviewable `proposal` only in `completed`. New
  `RunProposalAction` (`request` | `read` | `cancel`) names what may be done at the access point,
  and `RunProposalKind` (`fill` | `finetune`) is the one word for which analysis is meant.
* `source.ts` / `httpSource.ts`: `mercuryRunAiFill(action?)` and `mercuryRunAiFinetune(action?)`
  carry the action in the body of the SAME route. No route was added and no operation was renamed,
  so both list-shaped suites stay complete; requesting, reading and abandoning a proposal do not
  become three parallel paths to one entity.
* The result now arrives over the one live stream (topic `runs`, S12) like every other change of
  state, and the surface reads it back through the same access point — one mechanism (REQ-034).

**Consequence for operation:** the two accesses answer `202 Accepted` with `state: running` instead
of blocking; a caller that wants the plan asks again — `read` reports the state and starts nothing —
or waits for the `runs` tick. A call that meets an analysis which is running, finished OR FAILED
reports THAT one, a failure with its named reason, and asks no model. Starting over is the
deliberate `cancel`, then `request`. An MCP agent has the same three acts (next entry), so it is
never left with a state it can neither read nor end.

**State of record:**

- `src/types.ts@65d770d9f4d4`
- `src/data/source.ts@dc18e32e7d13`
- `src/data/httpSource.ts@28768ec2ba26`

**Diff hint:** `git diff eae031b -- src/types.ts src/data/source.ts src/data/httpSource.ts`.
**Proof:** `backend/internal/api/handlers_mercury_runs_ai_test.go`
(`TestAiFillAnswersWhileTheModelIsStillThinking` drives a model stand-in that answers only after
the test releases it — a handler that waits for the answer cannot return there at all;
`TestAiProposalFailureCarriesANamedReason`, `TestReadingNeverStartsAnAnalysisAndAReloadLosesNothing`,
`TestCancelAbandonsTheAnalysisAndItsLateAnswer`, `TestApplyingAProposalConsumesIt`), plus
`src/views/mercury/tasks/logic.test.ts` for what the surface may claim at each moment.

### `contract/mcp-tools.json`, `backend/internal/api/mcp_tools.go` — an agent may read and cancel a proposal, not only ask for one

**Who:** repair wave 6 (the agent that could only ask).

**Reason (measured):** `run_ai_fill` was called five times over MCP with empty arguments — the only
shape its schema allowed, `additionalProperties: false` and no argument at all — while aigentic
answered 403. All five answered `state: running`, none ever answered `failed`, the reason was never
delivered, and five full constitution prompts went to the model. Two faults met there. The access
point treated a repeat over a FAILED analysis as a fresh start, so every round began a new model
pass; and the tool table offered no way to read or to cancel one, so the surface had three acts
where an agent had one. That is a REQ-043 parity gap in the direction that is easy to miss: the
capability existed, but only one of the two ways in could reach it — and a caller that can only ask
has no way out of the loop it is in.

**What changed:**

* `mcp_tools.go`: both planning rows carry the optional `action` argument (`request` | `read` |
  `cancel`), worded once in `proposalActionArg` so the two rows cannot drift apart. The plain call
  is still the request, so no existing caller changes.
* `aiProposalAccess` (`handlers_mercury_runs.go`) reports whatever analysis exists — running,
  completed, or failed WITH its named reason — and never restarts by itself. Starting over is the
  deliberate two-step `cancel`, then `request`; that is exactly what pressing the button on a
  failure does.
* `contract/mcp-tools.json` is regenerated from the table
  (`UPDATE_FIXTURES=1 go test ./internal/api -run MCPToolTable`), never hand-edited.

**Consequence for operation:** an agent reads a proposal without starting one, ends one it no longer
wants, and meets a failure by its name over both ways in. The claim of the entry above — that an
agent "learns the outcome by calling again" — held for the successful case only and is corrected
there.

**State of record:**

- `contract/mcp-tools.json@a2d85070626a`
- `backend/internal/api/mcp_tools.go@52c076c54a64`
- `backend/internal/api/handlers_mercury_runs.go@b404af88e789`

**Diff hint:**
`git diff eae031b -- contract/mcp-tools.json backend/internal/api/mcp_tools.go backend/internal/api/handlers_mercury_runs.go`.
**Proof:** `backend/internal/api/handlers_mercury_runs_ai_test.go`:
`TestFivePlainAgentCallsCostExactlyOneModelPass` drives the measured loop itself — five plain tool
calls against a refusing model stand-in — and holds it to ONE model pass with the reason delivered;
`TestReadingAFailureOverMCPDeliversItsReasonAndAsksNoModel`, `TestCancelOverMCPAbandonsTheAnalysis`
and `TestBothPlanningToolsOfferRequestReadAndCancel` hold the agent's three acts;
`TestAiProposalFailureCarriesANamedReason` holds the deliberate two-step restart. Parity itself stays
list-shaped: `TestToolTableMirrorsTheDataSource` and `TestEveryRouteHasAToolOrAStatedReason`
(backend/internal/api), `backend/it/surface_test.go` and `src/parity.test.ts`.

---

## The explicit release of a blocked pull request

**What changed:** a new capability `delivery_resume` — route `POST /api/mercury/runs/deliveries/resume`,
store operation `runs.PRStore.ResumeBlocked`, data-source operation `mercuryResumeDelivery`, and a
control in the notices panel.

**Why:** the chain has always had an honest terminal state for a pull request whose read keeps
failing: it is blocked, and it waits for a person to say "try again" instead of retrying for ever
(K-5). The state was implemented, its comment even names the way out — "no further automatic attempt
until an explicit resume clears it" — but that resume did not exist. Neither a route, nor a store
operation, nor a control. A pull request blocked by a passing outage therefore stayed blocked for
good, and with it every delivery queued behind it in that repository.

Measured on 2026-07-31 on the running instance: 63 of 64 tracked pull requests carried a block, and
not one of the reasons described the pull request itself — 50 reads that never reached GitHub while
the service was restarting, 7 refused by the rate limit those restarts had burned, 1 stray 404, and
5 naming a production condition from the retired system that no longer exists. The whole delivery of
the instance stood still with no way to start it again.

**What it does and does not do:** it clears the blockade and the spent retry episode, so the next
maintenance pass evaluates the pull request again from a fresh start. It merges nothing, removes
nothing and decides nothing about the pull request — the pass blocks again, with a fresh reason,
whatever genuinely fails. That is why pressing it twice is harmless, and why it is not a destructive
capability. Without an argument it releases every blocked entry; with `{repo, number}` exactly the
named one, so a single repository can be started again without touching the rest.

**State of record:**

- `backend/internal/runs/prs.go@c5cb2cf13415`
- `backend/internal/api/api.go@539e0fd900ed`
- `backend/internal/api/mcp_tools.go@0fb2b422be21`
- `contract/mcp-tools.json@48acef6f9a9c`
- `src/data/source.ts@37c47a4effd5`
- `src/data/httpSource.ts@794942a541a2`
- `src/data/stubSource.ts@2bcc72284c4a`
- `src/views/mercury/NoticesPanel.tsx@0d331b3ef77a`

**Diff hint:**
`git diff 4793de5 -- backend/internal/runs/prs.go backend/internal/api/api.go backend/internal/api/mcp_tools.go contract/mcp-tools.json src/data src/views/mercury/NoticesPanel.tsx`.
**Proof:** `backend/internal/runs/prs_resume_test.go`:
`TestTheExplicitResumeIsTheWayOutOfTheBlockedState` releases one named entry and leaves its
neighbour blocked, then releases the rest, and holds all three invariants — the spent retry state
goes with the blockade, nothing is merged or removed, and a second press frees nothing;
`TestResumingAnUnknownPullRequestChangesNothing` holds that naming an untracked pull request invents
none. Parity stays list-shaped as before: `TestToolTableMirrorsTheDataSource` and
`TestEveryRouteHasAToolOrAStatedReason`.

---

## The task state is derived per RUN, not per workbench

**What changed:** `preflight.Sources` gains one observation point, `PriorImplementAt(runID, repo)`,
and `preflight.Derive` uses it. The rule "the workbench is ahead of the default branch ⇒
implemented-undelivered" is no longer a rule on its own; it holds only where THIS run already ran
the implement stage at that repository and the ledger holds no delivery for it. A merged delivery of
this run now outranks an ahead workbench instead of the other way round.

Alongside it, the wire key of the resume answer is `resumed`, not `released` — the contract's word
for `delivered` may not be reused for something else, and its sibling `mercuryResumeReportDelivery`
already answered with `resumed`.

**Why:** `mercury-dev` is a branch every run shares. That it runs ahead attests that undelivered
work EXISTS — never whose it is. The old rule read it as the current task's own work, so a fresh
task on a repository carrying anyone's undelivered commit took the rest path: implement created
nothing, every stage still reported `executed`, a delivery of the OTHER run's commits opened a pull
request, and the work that had been asked for never came into existence. Nothing looked wrong
anywhere.

Measured on 2026-07-31 on the running instance: all 23 workbenches were ahead — from 1 commit
(presentr, remshel, scheme, …) to 34 (devlab). Every new task on every repository would have been
skipped this way. It was found because the todo "Presentr: Dateien hochladen als Raumwissen" reported
`implement: already implemented — nothing new created` at a token consumption of 0.

**What it does and does not do:** the second run at a shared repository now implements its own task
instead of being declared done. It is not re-implementing anything — a second agent run over the SAME
task stays fenced, by the same rule, now run-scoped: an open ledger delivery of this run, or this
run's own earlier implement, still take the rest path. The execution archive is read as a source like
any other: unreachable ⇒ `unknown`, named, never guessed. An archived pre-rebuild document is skipped
by its provenance (`Result.Legacy`) rather than by a stage-name comparison, because it carries the
retired vocabulary and never wrote into today's ledger.

**State of record:**

- `backend/internal/preflight/preflight.go@4e806dc4522e`
- `backend/internal/api/exec_deps.go@bcf33917e207`
- `backend/internal/api/handlers_mercury_prs.go@fa5ab380dc71`
- `src/data/source.ts@a04b0b6b865f`
- `src/views/mercury/NoticesPanel.tsx@e9fae0d92d0a`

**Diff hint:**
`git diff b6ed499 -- backend/internal/preflight backend/internal/api/exec_deps.go backend/internal/api/handlers_mercury_prs.go src/data/source.ts src/views/mercury/NoticesPanel.tsx`.
**Proof:** `backend/internal/preflight/preflight_test.go`:
`TestDeriveForeignWorkOnSharedWorkbenchIsNotThisTask` holds the fault itself — an ahead workbench
without this run's own implement is `not-implemented`, and the evidence names whose work it is;
`TestDeriveThreeStates/implemented-undelivered via workbench ahead after this run's own implement`
holds the one case the rest path exists for; `TestDeriveMergedWinsOverForeignAhead` holds the new
precedence; `TestDeriveUnknownOnUnreachableHistory` holds the honest unknown. End to end,
`backend/it/race_test.go:TestRepositoryExclusivityQueuesInsteadOfSkipping` now counts two agent runs
at the shared repository, one per task, where it previously counted one.
