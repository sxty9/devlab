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

**One source for the production target.** The production target lives in exactly ONE place: this runtime
environment (delivered as the devlabd systemd drop-in). The daemon reads `DEVLAB_RUNS_PROD_TARGET` /
`_RECV` / `_KEY` via the environment and nothing else — proven by `TestNoDaemonCodeReadsDeadProdTarget`.
A file such as `/etc/devlab/prod-target` that looks like the target but steers nothing is the worse kind
of redundancy: someone edits it and changes nothing, then hunts the fault elsewhere. `--provision`
removes that dead twin and never recreates it (a self-check confirms it is gone), so the target exists
exactly once on the host.

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
- **Instance secrets are minted ON the host (BEFUND 1):** a delivered unit names the secrets its
  service reads (`Environment=HOLISTIC_SECRET_FILE=/etc/holistic/jwt-secret …`). A blank host has
  none, so the program installs cleanly and then dies at start ("no JWT secret …") in a restart
  loop — setup was a delivery TARGET but not an OPERATIONAL host. So first-time setup (both
  `devlab-install` on dev and `devlab-deploy-recv` on prod, sharing `setup_ensure_secrets` in
  `devlab-setup-lib.sh`) MINTS them, before the service starts:
  - **Derived, not listed.** The secret set is read from the unit itself
    (`setup_unit_secret_files`) — add a secret to a service's unit and the host mints it; the list
    never goes stale. Shared state dirs (`permissions.d`, `config.d`) are excluded.
  - **A rule, not a catalogue, decides what is generatable.** A name ending in `-secret` is an
    internally shared random token this host can mint from its own CSPRNG (jwt-secret,
    notify-secret, `<svc>-internal-secret`), written `root:holistic 0640` with the service account
    joined to `holistic`. Anything else (an outside credential such as an `.env` of foreign access
    keys) CANNOT be minted here.
  - **The boundary: secrets never travel.** Each host mints its OWN; a secret is never read off
    another host and never emitted, in any direction — two hosts sharing a secret are one
    environment, not two. A host's existing secret is never overwritten (idempotent, self-healing).
  - **Kein stummes Ausbleiben.** A secret that comes from OUTSIDE and is absent is NAMED
    (`MISSING-SECRET: <name>` plus a human line saying what will not work without it), never
    silently skipped.
- **Honest gate (F10) — up AND STAYS up (BEFUND 2):** "installed and started" is reported only
  after the unit is ACTIVE and the port is HELD, and then STAYS both continuously over a dwell
  (`deploy.VerifyRunning` on dev; `prove_running` in the receiver on prod — the SAME two-phase
  proof). A service that starts and dies immediately is a restart loop, not a start: it drops its
  port every cycle, so the continuous port-held requirement catches it. A unit that never comes up,
  or comes up and drops, is a FAILED delivery carrying the reason from the service's OWN log (e.g.
  "no JWT secret …") — never a `done`; a port conflict is named and a free port proposed where that
  is the cause.
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
  - **A CHANGED production host key** (the machine was reinstalled) is NOT a masked connection failure:
    it gets its own reason (`prod-host-key-changed`) and production is HELD until the new key is
    deliberately approved on the Blocked surface — the same approval path the root-wrapper renewal uses.
    Nothing is trusted silently: the approval pins the fingerprint the host now presents, and the accept
    path re-reads the key and refuses it if it changed again since the approval. See
    `backend/internal/deploy/prod_hostkey.go`.

## 8. Bring a NAKED production host up — one self-checking run, no prose to transcribe

A freshly installed, empty production host is made into a target the delivery chain can reach with ONE
command — there is no stretch of manual steps to copy out of this file. Run it ON the production host,
as root, from a checkout of the merged standard branch. It takes only the PUBLIC half of the deploy key
(a private key is never created on or written to the target):

```sh
sudo ./deploy/devlab-install-recv --provision --deploy-pubkey <path-to-deploy.pub> \
     --edge-address <host:port|:port> [service ...]
```

**Where this environment's edge answers is ONE declaration, read by BOTH sides.** The edge (the Caddy
site block built here) and the routing layer (a production-side `sxgate`) must agree on the socket this
environment answers on — otherwise the edge listens on one address while the routing layer forwards to
another, and every request for a production hostname ends as a 502 in front of a face that is elsewhere.
That address is stated in exactly ONE runtime-config file — `/etc/holistic/edge-address`, beside
`jwt-secret` (overridable only as a test seam), holding the bare socket in Caddy site-address / forward
target form (`10.10.0.1:8080` or `:8080`). `--edge-address` writes that declaration and the edge is
BUILT ON it (as a plain-HTTP site — the routing layer owns hostnames and TLS); the routing layer reads
the SAME file. There is NO baked-in default and neither side guesses the other's: a missing declaration
is a NAMED deficiency, fail-closed, never a silent `:80`. The address is an instance value and lives ONLY
in that file, never in the repository — so a production-side `sxgate` can own this declaration without
changing its shape. Ask it back (the routing layer's read) with:

```sh
devlab-install-recv --print-edge-address
```

It is one pass, idempotent, fail-closed and reversible, and it PROVES the result instead of claiming it.
In that single run it: ensures `rrsync` is present (the receiver confines every rsync receive through
it); creates the staging root it receives into (derived from `DEVLAB_STATE_DIR`, overridable with
`--staging`) and the INSTANCE ROOT it serves at — the serve root of the landscape's root application,
which is decided once in the shared library (`SETUP_ROOT_APP` / `setup_root_app_www`) and is deliberately
NOT a provisioning option: a host that took its instance root from one service's state directory answered
the whole instance with that service's login screen; installs Caddy and builds the edge as a
**site block** that imports the per-service route directory from INSIDE it (each route the receiver
drops in is a naked `handle` block, valid in Caddy only inside a site block — the shell comes from the
one template beside the route, `setup_edge_caddyfile_text`, so the container and its contents cannot
drift). Ubuntu's shipped example Caddyfile is not the holistic edge and is replaced (backed up first),
never appended to; a grown holistic edge that already imports the route directory and validates with a
route is left untouched and named, and any other foreign Caddyfile is named and refused, never
destroyed. It writes the locked-down deploy-key line — `command="/usr/local/sbin/devlab-deploy-recv",restrict <pubkey>` — into
the receiver login's `authorized_keys` (default `root`, override with `--recv-user`); and installs the
receiver + shared library themselves (the SAME install path the receiver-only mode uses — no second
copy). It closes with a self-check: `rrsync` resolves, the forced command actually rejects a shell
request, the receiver and library carry the expected checksums, the staging root and the instance root
exist, and the edge validates **with a delivered route present** (not merely empty — an empty edge
validates even when its shape could not hold a single route). Any failure is fail-closed (non-zero exit,
nothing half-done). Whether the root application has actually been DELIVERED to this host is a separate
fact the provisioning does not produce: it is reported as still outstanding, and until it arrives the
instance root answers `503` naming the missing root application — never with another service's
interface. After it passes,
**no further step on the target is needed to accept a delivery** — arm the DEV side by naming this host
in the environment (`DEVLAB_RUNS_PROD_TARGET`, `DEVLAB_RUNS_PROD_RECV`, `DEVLAB_RUNS_PROD_KEY`, §5).

Naming any service (e.g. `… --provision --deploy-pubkey deploy.pub prizm presentr`) also brings those
services up immediately from the artifact a prior send already staged, over the same install path the
chain uses. Instance values (the public key, the receiver login, the roots) are arguments or
environment — none is baked into the repository.

### 8.1 The transport is the FIRST stage, not an accessory

`DEVLAB_RUNS_PROD_TARGET` names a host the chain can only reach over a private WireGuard overlay (the
chain speaks to the overlay address, e.g. `root@10.10.0.1`, never a public one). Until that overlay
carries, the target does not exist — no key, no receiver and no delivery helps. So `--provision` sets up
**this host's side of the overlay in the same run** (Keine ähnlichen Geschwister — the same entry point,
no second script), when the overlay is named:

```sh
sudo ./deploy/devlab-install-recv --provision --deploy-pubkey <deploy.pub> \
     --overlay-address <this-host-overlay-cidr> --overlay-listen-port <port> \
     --overlay-peer-pubkey <home-side-PUBLIC-key> --overlay-peer-allowed <home-overlay-cidr> \
     --overlay-endpoint <this-host-public-address:port>
```

Each side generates its **own** keypair and hands out only the **public** half; a private overlay key is
never transmitted in either direction (Geheimnisse entstehen auf der Seite, die sie behält). This host's
private key is generated locally into `/etc/wireguard/<iface>.key` and stays there; the run prints this
host's **public** key. It is idempotent (a standing overlay carrying the intended config is reported, not
torn down) and reversible.

Because the far side was just rebuilt, the chain's **home side** still holds the old public key and the
tunnel is dead until it is caught up — the exact failure seen on 2026-08-06. The provision run prints the
**one line** that catches the home side up (with this host's fresh public key and endpoint already filled
in); run it as root on the chain's host:

```sh
sudo ./deploy/devlab-install-recv --overlay-here \
     --overlay-address <home-overlay-cidr> --overlay-peer-pubkey <this-host-PUBLIC-key> \
     --overlay-peer-allowed <this-host-overlay-cidr> --overlay-peer-endpoint <this-host-public-address:port> \
     --overlay-keepalive 25 --overlay-verify-peer <this-host-overlay-ip>
```

`--overlay-here` swaps in the far side's new public key and **proves** the tunnel with numbers — a
completed handshake and a ping with its loss/round-trip. If the peer does not answer it fails closed with
the real reason (no half-built path), leaving the correctly-built local side in place. Two commands, one
per host, is the irreducible minimum: the two machines share no filesystem, and the private keys must not
travel. All addresses, port and keys are arguments — none is baked into the repository.
