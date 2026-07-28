# 00 — Cutover (Orchestrator, root; B-9 order: stop → migrate → start)

_Owner: B5. This skeleton pins the section; B5 fills the exact command sequence from
BAUPLAN §5 (backup, wrappers + sudoers, unit + drop-in, marker removal, build as
unprivileged user, install-only, migrate BEFORE first start, start + honest probes)._

## Order (binding)

1. Backup: old state path + binary + unit (+ runs drop-in).
2. Wrappers + sudoers (pinned, narrow; the runner keeps NO sudo): devlab-exec (no preview
   verbs), devlab-install, devlab-restart-when-free, devlab.sudoers, devlab-runs.sudoers
   (grant: devlab → devlab-install). Remove the retired wrappers (devlab-restart-idle,
   devlab-deploy, devlab-preview); move /etc/devlab/deploy.d out of service (B-44).
3. Unit + drop-in: instance values ONLY in the drop-in. REMOVE Environment=DEVLAB_RUNS_MODE
   (does not exist in the rebuild, REQ-027.1); SET DEVLAB_STATE_DIR, DEVLAB_RUNS_DRAIN_GRACE,
   DEVLAB_RUNS_RESUME_WINDOW. DEVLAB_RUNS_MAX_CONCURRENCY stays as FIRST-start seed only.
4. Remove the legacy markers mercury/run-active and mercury/runs-active (REQ-039.3).
5. Build as an UNPRIVILEGED user (root never builds), then install-only.
6. Data migration BEFORE the first start (devlab-migrate refuses while the daemon lives).
7. Start + honest checks: unit active, port held, /api/health, ready socket answers 204.

## Rollback

Reverse order with the step-1 backup artifacts.
