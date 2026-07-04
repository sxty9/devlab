# Phase C — per-Linux-user isolation (live migration runbook)

**Why:** so the terminal and the AI agent run the shell / `claude` **as the logged-in Linux user**
in that user's real workspace (with their rights + their own `~/.claude` config), and the write
loop shares one coherently-owned git repo (git refuses mixed-owner repos → all ops on a workspace
must run as the same user).

**Model:**
- devlabd runs as a dedicated **`devlab`** system user (not `nanu`).
- Each user's workspace: `/var/lib/devlab/workspaces/<user>` owned `<user>:devlab`, mode `2750`
  (setgid → files inherit group `devlab`; the read-only service reads via group, other users can't).
- **Writes** (clone, add/commit/push/pull/checkout/branch, file write/delete) run via
  `sudo -n -u <user> /usr/local/sbin/devlab-exec …` (as the user; token via `DEVLAB_GH_TOKEN` env).
- **Reads** (status/tree/log/diff, file/raw) run as `devlab` directly with `-c safe.directory=*`.
- Provisioning a user's workspace root (chown) runs via `sudo -n /usr/local/sbin/devlab-mkworkspace <user>`.

Both wrappers are root-owned + pinned in `/etc/sudoers.d/devlab`; `devlab-exec` confines every path
to the caller's workspace root (verified: refuses `/etc`, `/etc/pwn`).

## Migration steps (run once, supervised; reversible)
```sh
# 1. dedicated service user + group
sudo groupadd -f devlab
sudo useradd -r -g devlab -s /usr/sbin/nologin -d /var/lib/devlab devlab 2>/dev/null || true

# 2. install the pinned wrappers + sudoers (validate before trusting)
sudo install -m0755 -o root -g root deploy/devlab-exec        /usr/local/sbin/devlab-exec
sudo install -m0755 -o root -g root deploy/devlab-mkworkspace /usr/local/sbin/devlab-mkworkspace
sudo install -m0440 -o root -g root deploy/devlab.sudoers     /etc/sudoers.d/devlab
sudo visudo -cf /etc/sudoers.d/devlab

# 3. secrets readable by the new service user
sudo chgrp devlab /etc/devlab/link-key /etc/devlab/github-oauth.json
sudo usermod -aG holistic devlab           # read /etc/holistic/jwt-secret + revoked.json

# 4. re-own state + existing workspaces to the new model
sudo chown -R devlab:devlab /var/lib/devlab
sudo chmod 0711 /var/lib/devlab/workspaces
#   for each existing user dir: chown <user>:devlab, setgid 2750, recursive user ownership
for d in /var/lib/devlab/workspaces/*/; do u=$(basename "$d"); \
  sudo chown -R "$u":devlab "$d" && sudo chmod 2750 "$d"; done

# 5. flip the unit to User=devlab (deploy/devlabd.service already updated) + restart
sudo install -m0644 deploy/devlabd.service /etc/systemd/system/devlabd.service
sudo systemctl daemon-reload && sudo systemctl restart devlabd
curl -s http://127.0.0.1:8781/api/health

# 6. verify the write loop still works end-to-end as the user (clone→edit→commit→push).
```

**Rollback:** set the unit back to `User=nanu`, `chown -R nanu:nanu /var/lib/devlab`, restart.
The wrappers/sudoers are inert unless devlabd invokes them.
