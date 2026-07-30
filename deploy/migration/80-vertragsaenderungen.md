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
repair wave 1 = `0a9f8fe`.

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
