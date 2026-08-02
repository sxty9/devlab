# Mercury runs — provisioning contract (rebuild)

The execution machinery is inert until the instance is provisioned. Without these steps only
the management layer runs. _Owner of the full walkthrough: B5 (S11); this file pins the
updated contract: no operating modes, no cost cap, ONE restart path, ports observed — never
stored._

> **Security doctrine (unchanged, binding):** an unattended agent that implements, pushes and
> delivers is the riskiest configuration. The agent therefore runs as a **dedicated,
> unprivileged** Linux user with **NO sudo at all**. The single privileged step — install —
> runs through the pinned root wrapper `devlab-install`, which is generic and install-only:
> it validates names and paths, installs artifacts, and **never executes repo-provided code
> as root** (E §7.4).

## 1. Runner user (unprivileged, no sudo)

```sh
sudo useradd -m -s /bin/bash devlab-runs
sudo usermod -aG hp_devlab_access devlab-runs
sudo -l -U devlab-runs    # must show NOTHING
```

## 2. Authenticate the agent CLI as the runner

```sh
sudo -u devlab-runs -i
claude login              # subscription/key lands in ~devlab-runs/.claude
exit
```

## 3. Runner GitHub link

Link the runner account once through the DevLab UI as the runner user, or provision its
token into the link store. `DEVLAB_RUNS_TOKEN_USER` names the linked account the chain acts
on behalf of.

## 4. Wrappers + sudoers (pinned, narrow)

```sh
sudo install -o root -g root -m 0755 deploy/devlab-exec /usr/local/sbin/devlab-exec
sudo install -o root -g root -m 0755 deploy/devlab-mkworkspace /usr/local/sbin/devlab-mkworkspace
sudo install -o root -g root -m 0755 deploy/devlab-install /usr/local/sbin/devlab-install
sudo install -o root -g root -m 0755 deploy/devlab-restart-when-free /usr/local/sbin/devlab-restart-when-free
sudo install -o root -g root -m 0440 deploy/devlab.sudoers /etc/sudoers.d/devlab
sudo install -o root -g root -m 0440 deploy/devlab-runs.sudoers /etc/sudoers.d/devlab-runs
sudo visudo -c
```

Both deploy wrappers carry a `--check` dry-run branch: the full validation cascade runs and
prints its decision without touching the host, so the security logic is verifiable in place:

```sh
/usr/local/sbin/devlab-install <repo> <artifact-dir> dev --check   # prints PLAN lines, or dies
/usr/local/sbin/devlab-restart-when-free --check                   # prints free|busy|dead
```

## 5. Environment contract (drop-in; instance values live ONLY here)

| Variable | Meaning |
|---|---|
| `DEVLAB_STATE_DIR` | the ONE state root (every persisted path derives from it) |
| `DEVLAB_RUNS_USER` | the runner Linux user the agent executes as |
| `DEVLAB_RUNS_TOKEN_USER` | linked account whose token pushes/PRs (default: the run user) |
| `DEVLAB_RUNS_MAX_CONCURRENCY` | FIRST-start seed of the slot capacity — runtime settings win (REQ-013.2) |
| `DEVLAB_RUNS_TICK` | due-check interval |
| `DEVLAB_RUNS_DRAIN_GRACE` | SIGTERM drain budget (< TimeoutStopSec; K-2) |
| `DEVLAB_RUNS_RESUME_WINDOW` | how old an interrupted execution may be and still auto-resume (default 240h) |
| `DEVLAB_RESTART_POLL` / `DEVLAB_RESTART_MAXWAIT` | ready-socket poll interval / hard cap of the handover restart |
| `DEVLAB_PORT_BAND` | port band for first-time service setup proposals (Atlas) |
| `DEVLAB_RUNS_PROD_TARGET` | rsync staging target of the production send (`user@host:path`) — server-side only |
| `DEVLAB_RUNS_PROD_RECV` | ssh destination of the install-only receiver's deploy key (`user@host`) |
| `DEVLAB_RUNS_PROD_KEY` | path to the deploy private key BOTH prod calls (file send + trigger) sign in with — readable by the service user; the file content never enters a log |

**Removed from the contract (do not set):** the operating-mode ladder (REQ-027.1 — the one
chain always runs preflight → implement → deliver-dev → publish → pull-request), any cost
cap (REQ-017 — consumption is reported live, never capped), and every activity marker file
(REQ-039.3 — activity is a projection over the execution documents; the only marker is
`mercury/restart.json`).

## 6. Switch-off path

Stop admissions by setting the slot capacity to 0 in the service configuration
(`PUT /api/service/config`); running executions drain honestly. There is no mode switch.

## 7. Delivery mechanics (S11 — one generic path)

- **Build as the user, install as root:** the runner builds via `devlab-exec artifact-build`
  into `<worktree>/.mercury-artifact`; root only ever installs that prebuilt result through
  `devlab-install` (name grammar, `realpath -e` confinement under the workspace root, env
  strictly `dev|prod`). Root never builds and never executes repo code.
- **First-time setup:** a template-conforming service with no unit yet is provisioned by the
  wrapper from its own root-owned inline templates (unit + route + rights manifest copy) with
  a validated port from the atlas proposal (`--port`). No per-repo scripts exist (B-44).
- **Honest gate (F10):** "installed and started" is reported only after the unit is ACTIVE and
  the port is HELD (`deploy.VerifyRunning`); otherwise the delivery FAILED, with the port
  conflict named and a free port proposed where that is the cause.
- **Ports (F9, REQ-044):** the ledger is derived on read from the route drop-ins and
  `/proc/net/tcp{,6}` — never stored, no maintainable list. `GET /api/atlas/ports` serves it;
  `DEVLAB_PORT_BAND` bounds proposals.
- **Self repo (K-2):** install lands immediately; the restart is handed to a transient unit
  outside the devlabd cgroup (`devlab-install … --handover` →
  `systemd-run --collect devlab-restart-when-free`), which polls the ready socket
  (`$DEVLAB_STATE_DIR/restart-ready.sock`: 204 free · 423 busy · dead ⇒ free) and restarts
  when free — or after `DEVLAB_RESTART_MAXWAIT`, logged. A failed handover fails the stage;
  nothing ever restarts inline.
- **prod (the LAST step of the chain, WHAT-1):** after a delivery MERGES, the production pass
  (`deliver.MaintainProd`, run on the maintenance tick right after the merges) ships the MERGED state
  — the default branch, never dev — into the rrsync-confined staging and fires the forced-command
  receiver `devlab-deploy-recv`, which installs the prebuilt artifact AND proves the unit ACTIVE on
  the target (the SAME honest gate the dev delivery uses, executed server-side). A task is done — and
  historized — only once its production step is proven; a merge alone never finishes it.
  - **Armed by configuration, not a switch.** Production is a real step of the chain, never one
    "permanently switched off" (Kein stummes Ausbleiben). It is armed by NAMING where it goes and how
    it signs in: `DEVLAB_RUNS_PROD_TARGET` (the rsync staging), `DEVLAB_RUNS_PROD_RECV` (the receiver
    host), and `DEVLAB_RUNS_PROD_KEY` (the deploy private key). A MISSING one of the three is a
    deficiency, not a legitimate off-state: it is reported as a failed production delivery (a
    disturbance the user sees) and retried, never a silent skip.
  - **One key, both calls, no default identity.** The file send and the forced-command trigger both
    authenticate with the ONE configured key (`ssh -i … -o IdentitiesOnly=yes`); the service user has
    no default ssh identity to fall back on. The key is named in the runtime configuration exactly
    like the target and receiver — never in the repository, never a file discovered beside it. A key
    that is missing or unreadable by the service user is reported with its OWN reason (which key, and
    whether it is absent or unreadable), not a masked "connection failed"; its file content never
    enters any message, log, or error.
  - **First contact records the host key durably.** On the first send ssh must record the target
    machine's host key; the service user has no home ssh directory. It is written to
    `<DEVLAB_STATE_DIR>/prod-known_hosts` — under the state root the service owns and that survives a
    restart (systemd's `StateDirectory`) — and CHECKED on every later contact
    (`StrictHostKeyChecking=accept-new` is trust-on-first-use, not "trust everything").
  - **Operator one-off: make the key readable by the service user.** The key file typically lands
    root-owned. DevLab never changes permissions under `/etc` itself. If the service user cannot read
    the configured key, grant it group read the same way the other service secrets are shared — e.g.
    `chgrp <service-group> <key> && chmod 640 <key>` — so the root-owned key stays owner-only for
    writes but group-readable for the service. (ssh does not reject a group-readable key it does not
    own, so this is safe.)
  - **A failed send is a matter of its own, after the stack (WHAT-3).** It never invalidates the
    merged layer beneath it: the task simply stays open, reports itself (`prod-undelivered` notice),
    and the send retries by itself on a growing backoff that is never given up on — while the stack
    builds on. The only proven not-applicable is a repository that is no service.
  - **The first armed run** ships one merged delivery's default-branch artifact and installs it on the
    second machine; **an unreachable receiver** fails the ssh trigger, so the delivery reads
    prod-failed and retries; a **half-installed target** cannot arise, because the receiver reports
    success ONLY after the unit is proven active — a receiver that installs but does not come up exits
    non-zero, so the sending side records a failure rather than a green half-delivery.
