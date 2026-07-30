# 10 — Data migration (S15): the one-time import, the activation gate and the one-off items M1–M8

_Owner: B13. The import is a binary of its own (`cmd/devlab-migrate`): it is never installed as a
service, it has no endpoint, it refuses to run while the daemon is alive, and it is idempotent —
a second run writes no byte. Instance values (state root, export path, old checkout) are shell
variables here and in `00-cutover.md`; they never enter the repository._

## The import

Order (B-9): **stop → migrate → start**. The import probes the ready socket; while anything
answers there — free or busy — it declines, because the running daemon owns the pools.

The import runs AS THE SERVICE ACCOUNT, because everything it writes must belong to that account.
That makes the export's readability a PRECONDITION, not a detail: an export left in the operator's
home is unreachable for the service account — a home is typically `0750`, so a foreign account
cannot even traverse it and `test -r` already fails, whereupon the import ends with exit `5`. So the
export is staged where the service account can read it, the readability is checked BEFORE the import
starts, and the staging is removed afterwards (the raw export carries this instance's prompts and
tasks). Steps 4 and 5 of `00-cutover.md` are where this happens inside the cutover; the same three
lines are what makes a later, standalone re-run work.

```sh
STATE_DIR=/var/lib/devlab                   # the instance's state root
SVC_USER=devlab                             # the daemon's service account (unit: User=)
EXPORT=<export-dir>/mercury-runs-roh.json   # the raw run export — instance data, never committed
OLD=<old-checkout>                          # the pre-rebuild working copy (M1, M2, M6)
IMPORT_DIR=$STATE_DIR/import                # the staging the service account can read

sudo systemctl stop devlabd
# 0) stage the export and PROVE it is readable for the account that will read it
sudo install -d -o $SVC_USER -g $SVC_USER -m0700 $IMPORT_DIR
sudo install -o $SVC_USER -g $SVC_USER -m0600 $EXPORT $IMPORT_DIR/mercury-runs-roh.json
sudo -u $SVC_USER test -r $IMPORT_DIR/mercury-runs-roh.json \
  && echo 'precondition met' || echo 'ABORT: unreadable for the service account — the import would exit 5'
# 1) read-only rehearsal: prints the full protocol, writes nothing
sudo -u $SVC_USER env DEVLAB_STATE_DIR=$STATE_DIR devlab-migrate --input $IMPORT_DIR/mercury-runs-roh.json --dry-run
# 2) the import itself
sudo -u $SVC_USER env DEVLAB_STATE_DIR=$STATE_DIR devlab-migrate --input $IMPORT_DIR/mercury-runs-roh.json
# 3) remove the staging — the export is instance data, not state
sudo rm -rf $IMPORT_DIR && sudo test ! -e $IMPORT_DIR && echo 'staging removed'
sudo systemctl start devlabd
```

`devlab-migrate` is invoked by name because step 4 of `00-cutover.md` INSTALLS it beside the daemon
(`/usr/local/bin`). Running it out of a build directory under `/tmp` would make the import depend on
the umask of the account that built it — the service account must be able to traverse and execute it.

Exit codes (uniform CLI convention): `0` ok · `1` generic · `2` usage · `5` config-state (no
state root, unreadable export, a record that cannot be mapped faithfully, a record that would be
ready to fire without a subject) · `10` declined (the service is still running).

## What the import writes

| Records | Treatment | Outcome |
|---|---|---|
| 7 automatic runs | created **inactive** and **without** axiom assignment — uncovered stays visible; authorship and schedule anchor are the import instant. They stay switched off until step **A** below is done and verified: without axioms there is no composed prompt, and a run whose prompt snapshot is empty would hand the agent the division-of-labor preamble alone — "you implement the task", no task named — across every target repository. The export's own switch is protocolled per run | |
| 1 open foreign task | fed in as an executable task with its original metadata, its prompt **composed** at import time through the one composition path (REQ-003), never copied from the export's aged string; fed in, **not** started (non-goal 2). A due date that already lapsed is dropped and protocolled | |
| 6 completed foreign tasks | history entries with their original metadata (time, outcome, tokens, cost, prompt, attachment names) and **no** run definition, so a finished task never reappears as open. They carry exactly one stage, `archived-outcome`, holding the outcome the export recorded — terminal, so the entry is complete and its success is the recorded one. No stage of the delivery chain is claimed: the export carries none | |
| 49 own-repository records | **not** fed back — their deduplicated substance is `ABNAHME.md`; the export stays the archive | |
| legacy execution archive `mercury/runs-results/` | read tolerantly, imported into `mercury/executions/`, then moved aside to `mercury/runs-results.imported` so one execution is never listed twice. Nothing is deleted; a file that does not parse is named and kept verbatim. Legacy states (for example the setting-based skip) stay viewable and are never produced anew (REQ-027.3) | |
| M1–M8 + activation gate | recorded in the existing notice pool, one record per item with its next step — the migration adds no store of its own | |

Fill the outcome column from the protocol the import prints.

The import **refuses** — nothing is written at all — when a record would enter the pool able to
fire without a prompt that names its subject: a recurring run switched on without axioms or
without a prompt, or a task with a due date but no task text. The refusal names the record. This
bar mirrors the scheduler's own due conditions; it is the reason the import can be run again
without thinking twice.

## The takeover of the pre-rebuild stock

The import does not write **beside** the old data — it carries the state over. The reason is a
property of the two record shapes: a pre-rebuild run record is
`{type,name,enabled,prompt,promptAt,lastFiredAt,lastResult,done,…}`, a rebuilt one is
`{kind,title,active,promptSnapshot,authorship,…}`, and **both carry the same `id`**. An import that
asks "do I already know this id?" therefore answers YES for every pre-rebuild record, imports
nothing, and leaves a pool whose records the daemon decodes into runs without a kind and without a
title. So the question the import asks is never *which id* but **which shape**: only a record in the
rebuilt form counts as already imported.

| Pool | What the rebuilt code makes of it as it lies | What the import does |
|---|---|---|
| `mercury/runs.json` | every pre-rebuild record decodes into a run **without kind and without title**, and its id reads as "already imported" | the pool is **replaced** by the records in the rebuilt form plus the imported ones; the old stock is copied verbatim to `runs.json.pre-migration` first |
| `mercury/runs-deliveries.json` | the source recorded a status **word**; the rebuilt record expresses merged and closed as **times**, so every record reads as **open** — and the next pull request stacks on the newest open delivery of the repository while the preflight reports it as an outstanding arrival | converted in place through the ledger's own write path: `merged`/`closed` become the corresponding time, `resultId` becomes the execution reference. The outcome **time** is the delivery's own creation time — the source carried no second timestamp — and a converted closed delivery states that in its closing reason. Copy first to `runs-deliveries.json.pre-migration` |
| `mercury/runs-history/` | a snapshot is a **full** run configuration and "restore" writes it back verbatim, so one restore of a pre-rebuild snapshot re-injects exactly what the import just removed | every snapshot holding a pre-rebuild (or unreadable) run set is moved to `runs-history.pre-migration/`. A snapshot in the rebuilt form and one holding no runs stay restorable, and the import leaves **one** snapshot of its own (`migrate`) as the restore point |
| `mercury/runs-prs.json` | read as it lies: repository, number, URL, run, times and the blockade all arrive | **nothing** — the file is not touched. The single field with no counterpart is the pre-rebuild deploy-attempt counter; the rebuilt record keeps retry state in `backoff`, and the counter only ever accompanied an already **blocked** record whose attempts are spent |
| `mercury/runs-notices.json` | read as it lies; the bundling fields that postdate these rows are filled on read | **nothing** — the migration protocol below is *added* beside the existing rows |
| `mercury/runs-results/` | read tolerantly and mapped, including a step recorded as `ok` before statuses existed, historical stage names in the instance's own language, the separate live block and a zero finishing time (which stays "never finished") | imported into `mercury/executions/`, then moved aside (row above). The auxiliary fields of the old document — `mode`, `timeBudget`, `numTurns`, `effort`, `promptHash`, `interrupted`, `suspended`, `resumeAt` — have no place in the rebuilt result and are **not** carried: the archive is display-only, its stages carry the observable truth, and the moved-aside archive keeps the originals |
| `mercury/daily-reports.json` | read as it lies — the rebuilt record spells `recipient`, `day`, `status`, `executions`, `attempts`, `lastAttempt`, `lastError` exactly as the source does, and the `backoff` field that postdates these rows is absent and reads as "no retry episode". But a record left in `failed` WITHOUT a backoff is **due again on the reporter's very first pass**, whatever its age: the reporter runs a pass before its first interval, so the first start re-attempts a send that the old instance already failed — and a report for a day inside the lookback window goes out for work the OLD instance did | **nothing** — and that is why it must be looked at before the runner identity is set: `00-cutover.md` step 6a prints day, status, attempts and the last error, step 6 keeps the reporter switched off until then. A day that may not be re-sent is settled by hand with the daemon stopped (status `blocked` waits for the explicit resumption, K-5); a day that no longer concerns anybody is moved aside with the file |
| `mercury/axiom-checks.json`, `mercury/axiom-authors.json`, `mercury/attachments/` | read as they lie: the examined-stand pool is `{repos: {repo: {axiom: {commit, at}}}}`, the authorship pool `{authors: {axiom: {createdBy, createdAt, updatedBy, updatedAt}}}`, and the attachment tree is `<run>/<attachment>` — all three the shape the rebuilt stores spell. An unreadable examination pool is set aside by the store itself and NAMED in the prompt, so it never reads as "never examined here" | **nothing** — nothing to migrate. Without them the first pass of every run would examine each repository in full, which is correct but expensive |
| `mercury/runs-settings.json`, `mercury/runs-incidents.json` | **no rebuilt store opens them** | moved to `<name>.pre-migration` with the reason in the protocol, so nothing is left that looks live and is not. The old slot capacity is **not** carried over: set it as `DEVLAB_RUNS_MAX_CONCURRENCY` in the drop-in (step 2 of `00-cutover.md`) if the pre-rebuild value is still wanted — read it out of `runs-settings.json.pre-migration` |
| the pre-rebuild single-execution marker | no reader — the twin-marker trap does not exist in the rebuild (REQ-039.3) | **not** the import's job: it is a trap to remove, not stock to keep, and step **3** of `00-cutover.md` deletes it by name before the import runs |

A record that is in **neither** shape — one carrying markers of both, or none at all — is never
interpreted: it is set aside with the rest of the stock and **named with its find location** in the
protocol. A pool file that is not readable at all aborts the whole import (exit `5`) and is left
exactly as it was.

Nothing is deleted anywhere: every set-aside artifact keeps its bytes under a `.pre-migration`
name, and a repeated takeover never overwrites an earlier copy (it takes `.pre-migration.2`, `…3`).

Check the outcome after the import — the target state is the eight records of the table above and
nothing else:

```sh
sudo -u $SVC_USER jq '[.runs[] | {id, kind, title, active}] | length, .' $STATE_DIR/mercury/runs.json
sudo -u $SVC_USER jq '[.runs[] | keys] | flatten | unique' $STATE_DIR/mercury/runs.json   # no type/name/enabled/done/prompt
sudo -u $SVC_USER ls -l $STATE_DIR/mercury/*.pre-migration $STATE_DIR/mercury/runs-history.pre-migration
```

## Step A — the axiom assignment (mandatory before any run is switched on)

This step is part of the cutover, not an afterthought: until it is done and checked, the seven
imported runs are switched off and the instance performs no recurring work.

It comes AFTER step 7 of `00-cutover.md`, not before: the constitution store resolves its token
through the runner identity (`DEVLAB_AXIOMS_TOKEN_USER`, else `DEVLAB_RUNS_TOKEN_USER`, else
`DEVLAB_RUNS_USER`), and the held first start has none of the three — A1 would fail with the store's
named "not configured" answer rather than write.

```sh
ORIGIN=<origin>            # the instance's own origin, as in 00-cutover.md

# A1  trigger the assignment: ONE write to the constitution assigns the imported runs (B-10,
#     REQ-004) — do it in the surface (Mercury → axioms), it needs a session behind it.
# A2  verify: every recurring run must carry axioms AND a prompt. Expected: 7 rows, no zeros
#     (id · axiom count · prompt bytes · switch; the switch is absent in the JSON while it is off).
curl -fsS "$ORIGIN/api/mercury/runs" \
  | jq -r '.runs[] | select(.kind=="auto")
           | [.id, (.axiomIds|length), (.promptSnapshot|length), (.active // false)] | @tsv'
# A3  verify the coverage view answers for the same set — an axiom left without a run stays
#     visibly uncovered; that is a finding, not a blocker.
curl -fsS "$ORIGIN/api/mercury/runs/coverage" | jq '{covered: (.covered|length), pending}'
# A4  only now switch each run on (Mercury → runs → the run's own switch), and check its next
#     firing.
curl -fsS "$ORIGIN/api/mercury/runs/calendar" | jq -r '.occurrences[] | [.at, .runTitle] | @tsv'
```

A run that still reports `0` axioms or a `0`-byte prompt in **A2** stays switched off. Switching
it on is what would send an agent into every one of its repositories with nothing to do — the one
failure mode this whole step exists to prevent. Acknowledge the activation-gate notice only once
**A2** is clean for every run that was switched on.

## The activation gate and the one-off items M1–M8

M1–M8 belong to the concrete old data set, not to the shape of the rebuild; the activation gate
belongs to the import itself. Each is visible as a notice after the import, each is closed the same
way: acknowledging its notice.

| Item | What has to happen | Command / step | Outcome |
|---|---|---|---|
| **A** activation gate | The seven recurring runs arrived switched off; they may only be switched on once they carry axioms and a prompt | Step **A** above | |
| **M1** rescue branches | Where the rebuild carries the capability anyway it is **proven**, not delivered twice: record per branch "folded in" or "superseded" (evidence: the acceptance lines for slots, time budget, usage and workbench), then remove all four | `git -C $OLD branch -D fix/auslieferungssperre-slots-lieferkette fix/zeitbudget-je-lauf fix/tokenverbrauch-live fix/arbeitsstand-vor-neustart-2136` and `git -C $OLD push origin --delete <branch>` where it exists remotely | |
| **M2** rollout backlog | The retired rollout left open pull requests, branches and follow-up entries; the rebuild has no rollout path at all | `gh pr close <n>` per open rollout pull request, delete their branches, drop their follow-up entries, and revert or counter-book the one-pull-request-per-repository commit depending on its merge state | |
| **M3** stale follow-up entry | A follow-up entry still points at a delivery branch that no longer exists (the prune loop that repeated forever) | Remove the entry for the deleted delivery branch from `mercury/runs-prs.json` while the service is stopped | |
| **M4** tasks whose work already arrived | Handled by the startup reconciliation, which records a completed execution per task | No manual step, but it does not happen at the HELD first start: the reconciliation resolves repositories through the runner identity, which step 6 of `00-cutover.md` deliberately withholds, so it logs `startup reconciliation deferred: no runner account configured` and runs at the restart of step 7. Check the notice feed after THAT start | |
| **M5** deliver the two pending services | The old path needed a per-repository script; the generic path replaces it | Create one task per service, run it through the chain, then verify: unit active, visible in the dashboard, right declared | |
| **M6** obsolete process branches | Branches whose **content** arrived in the default branch (also through a collective merge) — content decides, never the commit id | Probe each branch by content (`git -C $OLD cherry <default> <branch>`, diff probe) and delete the contained ones | |
| **M7** port inventory | One service once started on a port another already held | Open Atlas → Ports (or `curl -fsS <origin>/api/atlas/ports`) and confirm every conflict and deviation reported | |
| **M8** the one service that is not delivered | One service is deliberately not set up in production; that decision belongs in **its** repository as a declared value | Declare the exclusion in that repository's service declaration — by the owner's own commit or through a fed-in task; the rebuild changes no foreign repository | |

## After the import

- **Step A first.** The imported runs are switched off; nothing recurring happens until the
  assignment is done and verified. Everything below assumes A is closed.
- The imported open task is executed on demand, not by the migration.
- The imported history entries are archive states: they carry the recorded outcome and no run
  definition, so they are read, not started.
- Re-running the import is safe: it prints what is already there and writes nothing — not one byte.
  What makes that true is the shape check above, not a marker file.
- The restore history holds exactly one entry right after the import (`migrate`). The pre-rebuild
  snapshots are in `mercury/runs-history.pre-migration/` and are deliberately not offered: each one
  would write a full pre-rebuild configuration back into the pool.

## Rollback

The import **deletes** nothing, but it does **replace** the run pool and rewrite the delivery
ledger. To undo it: stop the daemon and restore the state tarball from step 0 of `00-cutover.md`.

Without the tarball, everything the import moved is still on disk under its own name and can be put
back by hand — in this order, with the daemon stopped:

```sh
sudo -u $SVC_USER mv $STATE_DIR/mercury/runs.json.pre-migration            $STATE_DIR/mercury/runs.json
sudo -u $SVC_USER mv $STATE_DIR/mercury/runs-deliveries.json.pre-migration $STATE_DIR/mercury/runs-deliveries.json
sudo -u $SVC_USER mv $STATE_DIR/mercury/runs-results.imported              $STATE_DIR/mercury/runs-results
sudo -u $SVC_USER mv $STATE_DIR/mercury/runs-history.pre-migration/*.json  $STATE_DIR/mercury/runs-history/
sudo -u $SVC_USER sh -c 'cd '$STATE_DIR'/mercury && for f in *.pre-migration; do mv "$f" "${f%.pre-migration}"; done'
```

The executions the import wrote into `mercury/executions/` stay: they are archive documents of
executions that happened, and the pre-rebuild service never read that directory.
