# 00 — Cutover (Orchestrator, root; B-9 order: stop → migrate → start)

_Owner: B5. The command sequence below is the binding cutover (BAUPLAN §5): backup, wrappers +
sudoers, unit + drop-in, marker removal, build as an unprivileged user, install-only, migrate
BEFORE the first start, start + honest probes. Instance values live in the drop-in and in the
operator's shell — never in the repository. What this document cannot know is written as a
placeholder (`<export-dir>`, `<github-owner>`, same convention as `10-daten.md`); substitute it
before running the line._

```sh
# 0) Sicherung (Alt-Datenpfad + Binary + Unit)
sudo systemctl stop devlabd
sudo tar -C /var/lib/devlab -czf /root/devlab-state-$(date +%Y%m%d-%H%M%S).tar.gz mercury links chats comments
sudo cp -a /usr/local/bin/devlabd /root/devlabd.bak
sudo cp -a /etc/systemd/system/devlabd.service /root/devlabd.service.bak
sudo cp -a /etc/systemd/system/devlabd.service.d/runs.conf /root/runs.conf.bak

# 1) Wrapper + sudoers (gepinnt, eng; Runner behält GAR KEIN sudo)
sudo install -o root -g root -m0755 deploy/devlab-exec /usr/local/sbin/devlab-exec          # ohne preview-Verben
sudo install -o root -g root -m0755 deploy/devlab-install /usr/local/sbin/devlab-install
sudo install -o root -g root -m0755 deploy/devlab-restart-when-free /usr/local/sbin/devlab-restart-when-free
sudo install -o root -g root -m0440 deploy/devlab.sudoers /etc/sudoers.d/devlab
sudo install -o root -g root -m0440 deploy/devlab-runs.sudoers /etc/sudoers.d/devlab-runs   # Grant: devlab → devlab-install
sudo visudo -c
sudo rm -f /usr/local/sbin/devlab-restart-idle /usr/local/sbin/devlab-deploy /usr/local/sbin/devlab-preview
sudo mv /etc/devlab/deploy.d /root/deploy.d.bak    # per-Repo-Skriptmechanik außer Betrieb (B-44)

# 2) Unit + Drop-in (Instanz-Werte bleiben NUR im Drop-in)
sudo install -m0644 deploy/devlabd.service /etc/systemd/system/devlabd.service
sudo $EDITOR /etc/systemd/system/devlabd.service.d/runs.conf
#   ENTFERNEN: Environment=DEVLAB_RUNS_MODE=full          (existiert im Neubau nicht, REQ-027.1)
#   SETZEN:    Environment=DEVLAB_STATE_DIR=/var/lib/devlab
#              Environment=DEVLAB_GH_OWNER=<github-owner>  (PFLICHT: der GitHub-Owner der
#                        Instanz; es gibt keinen Default mehr — ohne diesen Wert löst devlabd
#                        KEIN Repository auf und liefert eine leere Menge, nie eine fremde)
#              Environment=DEVLAB_RUNS_DRAIN_GRACE=60s
#              Environment=DEVLAB_RUNS_RESUME_WINDOW=240h
#   (DEVLAB_RUNS_MAX_CONCURRENCY bleibt als STARTWERT; Laufzeit gewinnt, REQ-013.2)
sudo systemctl daemon-reload

# 3) Alt-Marker entfernen (REQ-039.3) — die Doppel-Falle existiert nicht mehr
sudo rm -f /var/lib/devlab/mercury/run-active /var/lib/devlab/mercury/runs-active

# 4) Bauen als UNPRIVILEGIERTER User (root baut nie), dann install-only
(cd backend && go build -o /tmp/devlab-build/devlabd ./cmd/devlabd && go build -o /tmp/devlab-build/devlab-migrate ./cmd/devlab-migrate)
npm ci && npm run build
sudo install -o root -g root -m0755 /tmp/devlab-build/devlabd /usr/local/bin/devlabd
sudo rsync -a --delete dist/ /var/lib/devlab/www/

# 5) Datenmigration VOR dem ersten Start (B-9; migrate verweigert bei laufendem Dienst)
#    <export-dir> = Ablage des Roh-Exports (Instanzdaten, nie im Repo — siehe 10-daten.md)
sudo -u devlab env DEVLAB_STATE_DIR=/var/lib/devlab /tmp/devlab-build/devlab-migrate \
    --input <export-dir>/mercury-runs-roh.json

# 6) Start + ehrliche Prüfung
sudo systemctl start devlabd && systemctl status devlabd --no-pager
systemctl show -p Environment devlabd | grep -q 'DEVLAB_GH_OWNER=[^[:space:]]' \
    || echo 'FEHLT: DEVLAB_GH_OWNER — die Repo-Menge bleibt leer (Schritt 2)'
ss -tlnp | grep 8781 && curl -fsS http://127.0.0.1:8781/api/health
curl -fsS --unix-socket /var/lib/devlab/restart-ready.sock http://x/ready -o /dev/null -w '%{http_code}\n'   # 204 erwartet
```

## Rollback

Reverse order with the step-0 backup artifacts: stop devlabd, restore
`/root/devlabd.bak → /usr/local/bin/devlabd`, `/root/devlabd.service.bak` and
`/root/runs.conf.bak` into systemd, `daemon-reload`, restore the state tarball into
`/var/lib/devlab`, move `/root/deploy.d.bak` back to `/etc/devlab/deploy.d`, reinstall the
retired wrappers from the previous checkout, then start devlabd.
