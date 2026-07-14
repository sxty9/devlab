# DevLab per-user branch preview — secure implementation spec

Status: **designed + adversarially reviewed, NOT yet implemented.** Awaiting one product
decision (public-exposure gating) before build. Full analysis: workflow `wf_e58a6993-b49`.

## Thesis
`sxgate preview up` today re-execs the WHOLE command as root (`needs_root`), so a branch's
`preview.conf`, BUILD, SEED and RUN all run **as root** → root RCE if exposed to users who can
push arbitrary branches. Fix: **root does wiring only; the requesting Linux user `U` executes
everything that touches branch bytes**, via the existing Phase-C pattern (pinned `devlab-exec`
as `U`, path-confined to `/var/lib/devlab/workspaces/<self>`).

Two pinned programs, mirroring `devlab-exec`(user)/`devlab-mkworkspace`(root):
- **`devlab-exec`** (extend): new verbs `pv-manifest`, `pv-worktree-add`, `pv-build`,
  `pv-worktree-remove`, `pv-rm` — all run as `U`, all `require_under_root`.
- **`devlab-preview`** (new, root, single sudoers line): validates identity+slug, then drives the
  engine's infra half with `SXGATE_NO_SUDO=1` + new flags. Runs **zero** branch-derived code.

## Engine changes (`/home/nanu/sxgate/lib/preview.sh`), gated on `PREVIEW_RUNAS`
Empty `PREVIEW_RUNAS` ⇒ byte-for-byte today's behaviour (nanu's live previews untouched — the
migration-safety keystone). New flags `--as-user <U> --exec <path> --base <dir>`. When set:
- worktree checkout → detached SHA, as `U` (`pv-worktree-add`); repo is `U`-owned so git's
  dubious-ownership guard never fires.
- BUILD/HOOK/SEED → root only does pure `{worktree}/{state}/{zone}/{port}` string substitution
  (`_pv_expand`, no eval), hands the FINAL command to `U` via `DEVLAB_PV_CMD` env → `pv-build`.
- worktree/state under `--base /var/lib/devlab/workspaces/<U>/.previews/<slug>` (`<U>:devlab`).
- slug namespaced `<U>-<branch>-<service>` (anti-collision/hijack).
- `_pv_chown` disabled; ownership uniform `<U>:devlab` from checkout→run.
- vhost forced `proxy` mode; RUN service via a per-slug systemd drop-in with `User=<U>`.
- infra (ports, `sites.d/*.caddy`, `instances/*.env`, drop-in, daemon-reload/reload) stays root.

## ⚠️ MANDATORY red-team fixes (the raw design has reproduced holes — do NOT ship without these)
1. **CRITICAL — no attacker bytes into a root `eval`.** The `eval "$(pv-manifest …)"` design is a
   reproduced **root RCE** (branch shadows `declare`, or just prints payload to the captured
   stdout). Fix: `pv-manifest` sources the conf as `U` with **stdout/stderr suppressed**, emits
   only `declare -p <FIXED KEYS>` on a saved fd (`exec 9>&1`); root must **not `eval`** — parse
   `KEY=VALUE` strictly (Go-side in devlabd, or a non-executing parser) with a hard key allowlist
   and a conservative value charset (reject `$( ` backticks `;` newlines `{`). Applies to `up`
   AND `_pv_rebuild_locked`.
2. **CRITICAL — `Group=<U>`, never `Group=devlab`** in the RUN drop-in (gid `devlab` = read every
   workspace + the AES link-key). Add `InaccessiblePaths=/var/lib/devlab/workspaces
   /var/lib/devlab/links /etc/devlab`, `NoNewPrivileges=yes`, `ProtectSystem=strict`,
   `ProtectHome=tmpfs`, `PrivateTmp=yes`, `ReadWritePaths=<base>/<slug>`. Wrapper must **reject**
   any preview user whose `id -G` includes `devlab` or `holistic`.
3. **HIGH — strict slug regex `^[a-z0-9][a-z0-9-]{0,61}$` in BOTH the wrapper and the engine**
   (down/rebuild) before any root path/unit op — else root path-traversal delete via crafted slug.
   Scope `_pv_resolve_slug` to `<U>-*` under RUNAS. Keep the root-written `OWNER=<U>` meta as the
   real ownership guard.
4. **HIGH — per-user Unix-domain sockets, not the shared 8800-8899 TCP pool** (`reverse_proxy
   unix/…/state/run.sock`): a malicious RUN can squat another user's loopback port during a
   restart window and hijack their inbound traffic. Sockets are fs-permissioned to `U` and also
   remove the ~20-port box-wide ceiling.
5. **HIGH — take the global `flock` only for the short infra critical section.** Run
   worktree-add/BUILD/SEED OUTSIDE it under a per-slug lock, with a hard `timeout` on `pv-build`
   (else `BUILD='sleep infinity'` freezes everyone's previews).
6. **MED — `instances/` `0750 root:root`; write `.env`/`.meta` atomically `0640`** (not
   chmod-after — leaves a world-readable secret window); real secrets in the `U`-owned `state/`.

## devlabd endpoints (`internal/preview` + `internal/api`)
`POST /api/repos/{id}/previews {branch}` (guards: `hp_devlab_access` + `canPush` + workspace
ensured + branch committed locally + `.sxgate/preview.conf` present) → `sudo -n devlab-preview up
<sessionUser> <id> <branch>`. `DELETE …/previews/{slug}`, `POST …/previews/{slug}/rebuild`, `GET
…/previews` (list from devlabd's own state, not sxgate's — which already drifts). User comes from
the **session**, never the body.

## Exposure gating — DECIDED: per-preview passphrase (✅ Stage 1 built)
Product decision (user): **every preview is gated by a simple 3-word passphrase** (`apple-tiger-moon`),
so external testers need no Holistic account. Implemented in sxgate as **Caddy `basic_auth`** (bcrypt)
rendered by root into the dispatcher vhost — branch code can't read/bypass it. Committed on sxgate
branch `feat/devlab-preview-password` (`9af3e3f`), activated by `PREVIEW_PASSWORD=1` (legacy path
byte-identical when unset). Login user `preview`; passphrase printed on `up`, re-shown by
`sxgate preview pw <slug>`, stored `0640 root:$PREVIEW_PW_GROUP` (DevLab sets group `devlab` to read+show).
Verified offline: generator + bcrypt + `caddy validate` of gated vhosts (proxy + static_proxy).
DevLab per-user path (below) will always set `PREVIEW_PASSWORD=1` ⇒ every DevLab preview protected.
NOTE: `basic_auth` is a browser user+pass popup (user `preview`). If a pure password-only form is
wanted later, add a small `forward_auth` cookie service — deferred.

## Operator prerequisites (once, by hand)
`sxgate preview setup` done (✅ live); wildcard DNS `*.henrysoase.org` CNAME confirmed; each
preview user in `hp_devlab_access` with a provisioned workspace; exposure mitigation configured.

## Migration (additive, reversible)
Back up `preview.sh` + `/etc/sxgate/preview` + sudoers → land engine edits behind `PREVIEW_RUNAS`
(verify legacy path byte-identical) → extend `devlab-exec` (`env_keep += DEVLAB_PV_CMD`) → install
`devlab-preview` + sudoers line (`visudo -c`) → smoke-test as a throwaway `hp_devlab_access` user
(confirm BUILD+RUN run as `<user>`, worktree `<user>:devlab`, `.git/worktrees` NOT root-owned) →
wire devlabd endpoints last. Rollback = restore `preview.sh.bak` + drop the sudoers line.
