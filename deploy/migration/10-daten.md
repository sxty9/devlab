# 10 — Data migration (S15): the one-time import and the one-off items M1–M8

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
state root, unreadable export, a record that cannot be mapped faithfully) · `10` declined (the
service is still running).

## What the import writes

| Records | Treatment | Outcome |
|---|---|---|
| 7 automatic runs | created **without** axiom assignment — uncovered stays visible; authorship and schedule anchor are the import instant, so nothing becomes due on the first tick (B-10) | |
| 1 open foreign task | fed in as an executable task with its original metadata; fed in, **not** started (non-goal 2). A due date that already lapsed is dropped and protocolled | |
| 6 completed foreign tasks | history entries with their original metadata (time, outcome, tokens, cost, prompt, attachment names) and **no** run definition, so a finished task never reappears as open. No stage state is claimed: the export carries none | |
| 49 own-repository records | **not** fed back — their deduplicated substance is `ABNAHME.md`; the export stays the archive | |
| legacy execution archive `mercury/runs-results/` | read tolerantly, imported into `mercury/executions/`, then moved aside to `mercury/runs-results.imported` so one execution is never listed twice. Nothing is deleted; a file that does not parse is named and kept verbatim. Legacy states (for example "skipped because of a setting") stay viewable and are never produced anew (REQ-027.3) | |
| M1–M8 | recorded in the existing notice pool, one record per item with its next step — the migration adds no store of its own | |

Fill the outcome column from the protocol the import prints.

## One-off items M1–M8

These belong to the concrete old data set, not to the shape of the rebuild. Each is visible as a
notice after the import; acknowledging the notice is what closes the item.

| Item | What has to happen | Command / step | Outcome |
|---|---|---|---|
| **M1** rescue branches | Where the rebuild carries the capability anyway it is **proven**, not delivered twice: record per branch "folded in" or "superseded" (evidence: the acceptance lines for slots, time budget, usage and workbench), then remove all four | `git -C $OLD branch -D fix/auslieferungssperre-slots-lieferkette fix/zeitbudget-je-lauf fix/tokenverbrauch-live fix/arbeitsstand-vor-neustart-2136` and `git -C $OLD push origin --delete <branch>` where it exists remotely | |
| **M2** rollout backlog | The retired rollout left open pull requests, branches and follow-up entries; the rebuild has no rollout path at all | `gh pr close <n>` per open rollout pull request, delete their branches, drop their follow-up entries, and revert or counter-book the one-pull-request-per-repository commit depending on its merge state | |
| **M3** stale follow-up entry | A follow-up entry still points at a delivery branch that no longer exists (the prune loop that repeated forever) | Remove the entry for the deleted delivery branch from `mercury/runs-prs.json` while the service is stopped | |
| **M4** tasks whose work already arrived | Handled by the startup reconciliation, which records a completed execution per task | No manual step: check the notice feed after the first start | |
| **M5** deliver the two pending services | The old path needed a per-repository script; the generic path replaces it | Create one task per service, run it through the chain, then verify: unit active, visible in the dashboard, right declared | |
| **M6** obsolete process branches | Branches whose **content** arrived in the default branch (also through a collective merge) — content decides, never the commit id | Probe each branch by content (`git -C $OLD cherry <default> <branch>`, diff probe) and delete the contained ones | |
| **M7** port inventory | One service once started on a port another already held | Open Atlas → Ports (or `curl -fsS <origin>/api/atlas/ports`) and confirm every conflict and deviation reported | |
| **M8** the one service that is not delivered | One service is deliberately not set up in production; that decision belongs in **its** repository as a declared value | Declare the exclusion in that repository's service declaration — by the owner's own commit or through a fed-in task; the rebuild changes no foreign repository | |

## After the import

- The **first constitution write** triggers the automatic assignment of the imported runs
  (B-10, REQ-004): check the result in the coverage view — a run that stays uncovered stays
  visibly uncovered.
- The imported open task is executed on demand, not by the migration.
- Re-running the import is safe: it prints what is already there and writes nothing.

## Rollback

The import only adds — it deletes nothing. To undo it: stop the daemon, restore the state
tarball from step 0 of `00-cutover.md`, and (if the archive was moved) rename
`mercury/runs-results.imported` back to `mercury/runs-results`.
