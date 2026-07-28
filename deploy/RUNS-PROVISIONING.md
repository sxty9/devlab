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
- **prod (not armed):** the prod send (rsync into the rrsync-confined staging behind the
  forced-command receiver `devlab-deploy-recv`, target server-side only) is implemented and
  fixture-tested, but not armed in this phase.
