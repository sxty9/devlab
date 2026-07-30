# 00 — Cutover (Orchestrator, root; B-9 order: stop → migrate → start)

_Owner: B5. The command sequence below is the binding cutover (BAUPLAN §5): backup, wrappers +
sudoers, unit + drop-in, marker removal, build as an unprivileged user, install-only, migrate
BEFORE the first start, start + honest probes. Instance values live in the drop-in and in the
operator's shell — never in the repository. What this document cannot know is written as a
placeholder (`<export-dir>`, `<github-owner>`, same convention as `10-daten.md`); substitute it
before running the line._

_This machine is shared. The daemon is one of sixteen services behind one Caddy and one sudo
configuration, so every step that touches something SHARED — `/etc/sudoers.d`, `/etc/caddy/conf.d`,
the state root — validates before it adopts, and the sequence never leaves the host in a state where a
mistake of ours breaks a neighbour. What reaches beyond this host at all (branch protection) is named
in step 6 and stays held until it is armed deliberately._

```sh
# 0) Sicherung — VOLLSTÄNDIG (Rückweg von Schritt 4 und 5)
sudo systemctl stop devlabd
#    Alles unter dem State-Root ausser workspaces (Arbeitsbäume, jederzeit neu klonbar):
#    mercury (Läufe/Executions), links (Token-Store), chats, comments, www (die ALTE SPA, die
#    Schritt 4 mit `rsync --delete` vernichtet) und axioms (der Verfassungs-Klon; ein nicht
#    gepushter Stand darin existiert sonst nirgends). --ignore-failed-read: ein noch nicht
#    angelegtes Verzeichnis ist kein Fehler.
sudo tar -C /var/lib/devlab --ignore-failed-read \
    -czf /root/devlab-state-$(date +%Y%m%d-%H%M%S).tar.gz \
    mercury links chats comments www axioms
sudo cp -a /usr/local/bin/devlabd /root/devlabd.bak
sudo cp -a /etc/systemd/system/devlabd.service /root/devlabd.service.bak
sudo cp -a /etc/systemd/system/devlabd.service.d/runs.conf /root/runs.conf.bak
#    Prüfen, dass die Sicherung die beiden neuen Glieder wirklich enthält (sonst ist der Rückweg
#    unvollständig, und das merkt man erst, wenn man ihn braucht):
tar -tzf /root/devlab-state-*.tar.gz | grep -qE '^www/'    && echo 'www gesichert'
tar -tzf /root/devlab-state-*.tar.gz | grep -qE '^axioms/' && echo 'axioms gesichert'

# 1) Wrapper + sudoers (gepinnt, eng; Runner behält GAR KEIN sudo)
#    sudoers ZUERST prüfen, DANN einbauen: eine fehlerhafte Datei in /etc/sudoers.d macht sudo
#    maschinenweit unbrauchbar — auch für die fünfzehn Nachbardienste und für den Rückweg selbst.
#    `visudo -c -f <datei>` prüft die EINZELNE Datei, ohne sie einzubauen.
sudo visudo -c -f deploy/devlab.sudoers
sudo visudo -c -f deploy/devlab-runs.sudoers
sudo install -o root -g root -m0755 deploy/devlab-exec /usr/local/sbin/devlab-exec          # ohne preview-Verben
sudo install -o root -g root -m0755 deploy/devlab-install /usr/local/sbin/devlab-install
sudo install -o root -g root -m0755 deploy/devlab-mkworkspace /usr/local/sbin/devlab-mkworkspace
sudo install -o root -g root -m0755 deploy/devlab-restart-when-free /usr/local/sbin/devlab-restart-when-free
sudo install -o root -g root -m0440 deploy/devlab.sudoers /etc/sudoers.d/devlab
sudo install -o root -g root -m0440 deploy/devlab-runs.sudoers /etc/sudoers.d/devlab-runs   # Grant: devlab → devlab-install
sudo visudo -c                                    # Gesamtprüfung NACH dem Einbau (Belt zur Brace)
sudo rm -f /usr/local/sbin/devlab-restart-idle /usr/local/sbin/devlab-deploy /usr/local/sbin/devlab-preview
sudo mv /etc/devlab/deploy.d /root/deploy.d.bak    # per-Repo-Skriptmechanik außer Betrieb (B-44)

# 1a) Die Organisation als root-eigene Datei (PFLICHT, sonst verweigert der Install-Wrapper JEDE
#     Auslieferung mit Exit 5). Sie steht bewusst NICHT in der Umgebung: der Aufrufer darf den
#     Namensraum, an dem er gemessen wird, nicht selbst wählen. Derselbe Wert wie DEVLAB_GH_OWNER.
sudo install -d -m0755 /etc/devlab
printf '%s\n' '<github-owner>' | sudo tee /etc/devlab/gh-owner >/dev/null
sudo chmod 0644 /etc/devlab/gh-owner
sudo test -r /etc/devlab/gh-owner && echo 'gh-owner vorhanden'

# 2) Unit + Drop-in (Instanz-Werte bleiben NUR im Drop-in)
sudo install -m0644 deploy/devlabd.service /etc/systemd/system/devlabd.service
#    sudoedit, NICHT `sudo $EDITOR`: bei leerem EDITOR würde sudo die Drop-in-Datei als Kommando
#    AUSFÜHREN. sudoedit editiert eine Kopie als der aufrufende User und spielt sie als root zurück.
sudoedit /etc/systemd/system/devlabd.service.d/runs.conf
#    Das Drop-in Zeile für Zeile (Tabelle „Das Alt-Drop-in, Zeile für Zeile" unten):
#      ENTFERNEN:  PrivateTmp=true, DEVLAB_RUNS_MODE, DEVLAB_RUNS_AGENT_TIMEOUT
#      UMBENENNEN: DEVLAB_RUNS_AUTOMERGE      → DEVLAB_RUNS_AUTOMERGE_WINDOW
#                  DEVLAB_RUNS_LIMIT_MAXRESUMES → DEVLAB_RUNS_LIMIT_MAX_RESUMES
#      ERGÄNZEN:   DEVLAB_GH_OWNER=<github-owner>   (PFLICHT, kein Default mehr)
#      BLEIBT:     DEVLAB_RUNS_USER, DEVLAB_RUNS_TOKEN_USER, DEVLAB_RUNS_MAX_DURATION,
#                  DEVLAB_RUNS_MAX_CONCURRENCY (nur Startwert), DEVLAB_RUNS_LIMIT_BACKOFF,
#                  DEVLAB_RUNS_TICK
sudo systemctl daemon-reload
#    Die zusammengeführte Wahrheit lesen — ein Drop-in gewinnt gegen die Vorlage, also wird
#    geprüft, was systemd am Ende sieht, nicht was in der Vorlage steht:
systemd-analyze cat-config systemd/system/devlabd.service | grep -E '^(PrivateTmp|ReadWritePaths)='
systemctl show -p Environment devlabd | tr ' ' '\n' | grep -E 'DEVLAB_RUNS_(MODE|AGENT_TIMEOUT|AUTOMERGE|LIMIT_MAXRESUMES)=' \
    && echo 'ÜBRIG: eine zurückgezogene Variable steht noch im Drop-in (Schritt 2)'

# 3) Alt-Marker entfernen (REQ-039.3) — die Doppel-Falle existiert nicht mehr
sudo rm -f /var/lib/devlab/mercury/run-active /var/lib/devlab/mercury/runs-active

# 4) Bauen als UNPRIVILEGIERTER User (root baut nie), dann install-only
(cd backend && go build -o /tmp/devlab-build/devlabd ./cmd/devlabd && go build -o /tmp/devlab-build/devlab-migrate ./cmd/devlab-migrate)
npm ci && npm run build
sudo install -o root -g root -m0755 /tmp/devlab-build/devlabd /usr/local/bin/devlabd
sudo rsync -a --delete dist/ /var/lib/devlab/www/    # vernichtet die alte SPA — Sicherung: Schritt 0

# 5) Datenmigration VOR dem ersten Start (B-9; migrate verweigert bei laufendem Dienst)
#    <export-dir> = Ablage des Roh-Exports (Instanzdaten, nie im Repo — siehe 10-daten.md)
sudo -u devlab env DEVLAB_STATE_DIR=/var/lib/devlab /tmp/devlab-build/devlab-migrate \
    --input <export-dir>/mercury-runs-roh.json

# 6) Start + ehrliche Prüfung
sudo systemctl start devlabd && systemctl status devlabd --no-pager
systemctl show -p Environment devlabd | grep -q 'DEVLAB_GH_OWNER=[^[:space:]]' \
    || echo 'FEHLT: DEVLAB_GH_OWNER — die Repo-Menge bleibt leer (Schritt 2)'
#    Adresse aus der Konfiguration ableiten statt einen Port zu behaupten: Ports sind
#    Laufzeit-Konfiguration (REQ-044), und ein hart notierter Port prüft nach der ersten
#    Umkonfiguration den falschen Socket.
addr="$(systemctl show -p Environment --value devlabd | tr ' ' '\n' | sed -n 's/^DEVLAB_ADDR=//p' | tail -n1)"
[ -n "$addr" ] || echo 'FEHLT: DEVLAB_ADDR — ohne Adresse ist nicht prüfbar, wo der Dienst hört'
ss -tlnp | grep -F "$addr" && curl -fsS "http://$addr/api/health"
curl -fsS --unix-socket /var/lib/devlab/restart-ready.sock http://x/ready -o /dev/null -w '%{http_code}\n'   # 204 erwartet
```

## Fremdwirkung: was dieser Start ausserhalb von DevLab berührt

Genau eine Sache. Der Verzweigungsschutz-Durchlauf (REQ-033.7) liest den Schutz JEDES Repositories
der konfigurierten Organisation und schreibt bei Abweichung in dessen Standard-Verzweigung — auch in
Repositories, mit denen dieser Cutover nichts zu tun hat. Er ist deshalb zurückgehalten:

* **Kein Durchlauf beim Booten.** Der erste Durchlauf wartet `DEVLAB_RUNS_PROTECTION_START_DELAY`
  (Vorgabe 15m). Wer den Dienst innerhalb dieses Fensters wieder stoppt, hat GitHub nicht berührt —
  auch eine Neustart-Schleife kann so keine Schreib-Schleife werden.
* **Melde-Modus, bis der Betreiber freigibt.** Ohne `DEVLAB_RUNS_PROTECTION_ENFORCE` STELLT der
  Durchlauf Abweichungen nur FEST und meldet sie (Notice-Feed, Tagesbericht); er ändert nichts. Erst
  wenn die gemeldeten Funde die beabsichtigten sind, wird geschärft:

```sh
# nach dem ersten gemeldeten Durchlauf (Notices lesen!), erst dann:
sudoedit /etc/systemd/system/devlabd.service.d/runs.conf    # Environment=DEVLAB_RUNS_PROTECTION_ENFORCE=1
sudo systemctl daemon-reload && sudo systemctl restart devlabd
```

Alles andere, was dieser Cutover anfasst, ist geteilt aber lokal, und jeder Schritt prüft vor dem
Übernehmen: `visudo -c -f` je sudoers-Datei vor dem Einbau (Schritt 1), `caddy validate` vor dem
Übernehmen einer Route und Rücknahme der eigenen Datei bei Fehlschlag (im Install-Wrapper), und der
Neustart eines Nachbardienstes findet nicht statt — der Wrapper startet nur die Unit, die er gerade
installiert hat.

## Das Alt-Drop-in, Zeile für Zeile

Das Drop-in gewinnt gegen die Vorlage. Jede Zeile, die stehen bleibt, ist deshalb eine Entscheidung —
und zwei davon waren vorher unbemerkt: `PrivateTmp=true` überschrieb die Isolationsentscheidung der
neuen Vorlage, und zwei Schalter waren umbenannt worden und taten still nichts mehr.

| Zeile im Alt-Drop-in | Verdikt |
|---|---|
| `PrivateTmp=true` | **entfernen** — die Vorlage regelt `/tmp` jetzt selbst (`ReadWritePaths=… /tmp`), damit der Scratch der Per-User-Kinder im echten `/tmp` liegt und nicht in einem Namensraum, den nur dieser Dienst sieht |
| `DEVLAB_RUNS_MODE` | **entfernen** — Betriebsmodi existieren nicht (REQ-027.1) |
| `DEVLAB_RUNS_AGENT_TIMEOUT` | **entfernen** — wird von nichts mehr gelesen; das Zeitbudget je Versuch ist `DefaultTimeBudget` im Einstellungs-Pool (Startwert `DEVLAB_RUNS_TIME_BUDGET`) |
| `DEVLAB_RUNS_AUTOMERGE` | **umbenennen** → `DEVLAB_RUNS_AUTOMERGE_WINDOW`; sät nur noch den ersten Start, danach gewinnt der gespeicherte Wert (REQ-013.2) |
| `DEVLAB_RUNS_LIMIT_MAXRESUMES` | **umbenennen** → `DEVLAB_RUNS_LIMIT_MAX_RESUMES`; unter dem alten Namen liest es niemand, die Grenze fiele still auf die Vorgabe zurück |
| `DEVLAB_RUNS_USER` | **bleibt** — Instanzwert: das rechtelose OS-Konto des Runners |
| `DEVLAB_RUNS_TOKEN_USER` | **bleibt** — Instanzwert: das verknüpfte GitHub-Konto, mit dem geklont/gepusht wird |
| `DEVLAB_RUNS_MAX_DURATION` | **bleibt** — harte Wanduhr-Grenze je Versuch im Scheduler |
| `DEVLAB_RUNS_MAX_CONCURRENCY` | **bleibt** — aber nur als STARTWERT; eine Laufzeit-Änderung gewinnt (REQ-013.2) |
| `DEVLAB_RUNS_LIMIT_BACKOFF` | **bleibt** — Wartezeit auf das echte Usage-Limit-Fenster |
| `DEVLAB_RUNS_TICK` | **bleibt** — Takt des Fälligkeits-Tickers |
| `DEVLAB_GH_OWNER` | **ergänzen** — PFLICHT, kein Default mehr; ohne ihn bleibt die Repo-Menge leer |
| `DEVLAB_RUNS_PROTECTION_ENFORCE` | **später ergänzen** — erst nach dem ersten gemeldeten Schutz-Durchlauf (siehe oben) |

`DEVLAB_STATE_DIR`, `DEVLAB_ADDR`, `DEVLAB_RUNS_DRAIN_GRACE`, `DEVLAB_RUNS_RESUME_WINDOW` und
`DEVLAB_RUNS_PROTECTION_START_DELAY` stehen in der Vorlage. Sie gehören nur dann ins Drop-in, wenn
diese Instanz von der Vorlage abweicht — eine Wiederholung des Vorlagenwerts ist keine Konfiguration,
sondern eine zweite Wahrheit.

## Rollback

Rückwärts, mit den Sicherungen aus Schritt 0 — Reihenfolge zählt, weil der Dienst nichts lesen darf,
was gerade zurückgespielt wird:

1. `sudo systemctl stop devlabd`
2. `sudo cp -a /root/devlabd.bak /usr/local/bin/devlabd` (das alte Binary)
3. `sudo cp -a /root/devlabd.service.bak /etc/systemd/system/devlabd.service` und
   `sudo cp -a /root/runs.conf.bak /etc/systemd/system/devlabd.service.d/runs.conf`, dann
   `sudo systemctl daemon-reload`
4. Zustand zurückspielen — der Tarball enthält `mercury links chats comments www axioms`:
   `sudo tar -C /var/lib/devlab -xzf /root/devlab-state-<ts>.tar.gz`. Das holt auch die **alte SPA**
   zurück, die Schritt 4 mit `rsync --delete` vernichtet hat, und den **Verfassungs-Klon** `axioms`
   mit seinem nicht gepushten Stand. Ein Neu-Klon der Verfassung ersetzt Letzteren NICHT.
5. `sudo mv /root/deploy.d.bak /etc/devlab/deploy.d` (per-Repo-Skriptmechanik zurück)
6. Die zurückgezogenen Wrapper aus dem vorherigen Checkout wieder installieren
   (`devlab-restart-idle`, `devlab-deploy`, `devlab-preview`).
7. `sudo systemctl start devlabd`

`/etc/devlab/gh-owner` bleibt liegen: die Datei ist auch für den alten Stand harmlos und wird beim
nächsten Anlauf wieder gebraucht.
