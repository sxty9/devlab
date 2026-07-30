# 10 — Data migration (S15): the one-time import, the activation gate and the one-off items M1–M8

_Owner: B13. The import is a binary of its own (`cmd/devlab-migrate`): it is never installed as a
service, it has no endpoint, it refuses to run while the daemon is alive, and it is idempotent —
a second run writes no byte. Instance values (state root, export path, old checkout) are shell
variables here and in `00-cutover.md`; they never enter the repository._

## The import

Order (B-9): **stop → migrate → start**. The import probes the ready socket; while anything
answers there — free or busy — it declines, because the running daemon owns the pools.

```sh
STATE_DIR=/var/lib/devlab                   # the instance's state root
EXPORT=<export-dir>/mercury-runs-roh.json   # the raw run export — instance data, never committed
OLD=<old-checkout>                          # the pre-rebuild working copy (M1, M2, M6)

sudo systemctl stop devlabd
# 1) read-only rehearsal: prints the full protocol, writes nothing
sudo -u devlab env DEVLAB_STATE_DIR=$STATE_DIR devlab-migrate --input $EXPORT --dry-run
# 2) the import itself
sudo -u devlab env DEVLAB_STATE_DIR=$STATE_DIR devlab-migrate --input $EXPORT
sudo systemctl start devlabd
```

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

## Step A — the axiom assignment (mandatory before any run is switched on)

This step is part of the cutover, not an afterthought: until it is done and checked, the seven
imported runs are switched off and the instance performs no recurring work.

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
| **M4** tasks whose work already arrived | Handled by the startup reconciliation, which records a completed execution per task | No manual step: check the notice feed after the first start | |
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
- Re-running the import is safe: it prints what is already there and writes nothing.

## Rollback

The import only adds — it deletes nothing. To undo it: stop the daemon, restore the state
tarball from step 0 of `00-cutover.md`, and (if the archive was moved) rename
`mercury/runs-results.imported` back to `mercury/runs-results`.
