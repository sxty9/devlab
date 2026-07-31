# 00 — Cutover (Orchestrator, root; B-9 order: stop → migrate → start)

_Owner: B5. The command sequence below is the binding cutover (BAUPLAN §5): backup, wrappers +
sudoers, unit + drop-in, marker removal, build as an unprivileged user, install-only, migrate
BEFORE the first start, a HELD first start with honest probes, and only then the runner identity.
Instance values live in the drop-in and in the operator's shell — never in the repository. What this
document cannot know is written as a placeholder (`<export-dir>`, `<github-owner>`,
`<cookie-domain>`, same convention as `10-daten.md`); substitute it before running the line._

_This machine is shared. The daemon is one of sixteen services behind one Caddy and one sudo
configuration, so every step that touches something SHARED — `/etc/sudoers.d`, `/etc/caddy/conf.d`,
the state root, the web root — validates or measures before it adopts, and the sequence never leaves
the host in a state where a mistake of ours breaks a neighbour._

_What reaches beyond DevLab is **not one thing**. Fourteen writing sites are listed in
„Fremdwirkung" below with the tick each one fires on; four of them fire within the first minute of a
start, three of those on the very first scheduler tick. That is why the first start is run WITHOUT
the runner identity (step 6) and the identity is added afterwards (step 7): without it every
GitHub-reaching pass fails closed with a named error, and the operator looks at what the next tick
WOULD touch before it touches it._

```sh
# ── Werte dieses Durchgangs (Instanzwerte, nie im Repo) ──────────────────────────────────────
STATE_DIR=/var/lib/devlab                          # State-Root dieser Instanz (Vorlagen-Default)
SVC_USER=devlab                                    # Dienst-Account der Unit (User=, Vorlagen-Default)
EXPORT=<export-dir>/mercury-runs-roh.json          # der Roh-Export (siehe 10-daten.md)
BAK=/root/devlab-cutover-$(date +%Y%m%d-%H%M%S)    # Sicherung DIESES Durchgangs
STATE_TAR="$BAK/devlab-state.tar.gz"               # EIN Durchgang, EINE Zustandssicherung

# 0) Sicherung — VOLLSTÄNDIG: der Zustand UND jedes Artefakt, das dieser Cutover verändert
sudo systemctl stop devlabd
sudo install -d -m0700 "$BAK"
#    0a) Der Zustand unter dem State-Root, ausser workspaces (Arbeitsbäume, jederzeit neu klonbar):
#        mercury (Läufe/Executions), links (Token-Store), chats, comments, www (die ALTE SPA, die
#        Schritt 4 überschreibt) und axioms (der Verfassungs-Klon; ein nicht gepushter Stand darin
#        existiert sonst nirgends). --ignore-failed-read: ein noch nicht angelegtes Verzeichnis ist
#        kein Fehler.
#        Der Tarball trägt einen FESTEN Namen: $BAK trägt den Zeitstempel bereits, und ein zweiter
#        Anlauf in dasselbe $BAK ersetzt damit die eine Sicherung, statt eine zweite danebenzulegen.
#        Ein Glob über zwei Tarbälle hätte `tar -tzf` zwei Argumente gegeben — tar liest das zweite
#        dann als Mitglied IM ersten, und die Prüfung unten wäre fälschlich fehlgeschlagen.
sudo tar -C "$STATE_DIR" --ignore-failed-read -czf "$STATE_TAR" \
    mercury links chats comments www axioms
#        Prüfen, dass die Sicherung jedes Glied wirklich enthält (sonst ist der Rückweg
#        unvollständig, und das merkt man erst, wenn man ihn braucht):
for d in mercury links chats comments www axioms; do
  sudo tar -tzf "$STATE_TAR" | grep -q "^$d/" \
    && echo "gesichert: $d" || echo "NICHT im Tarball (fehlt oder ist leer): $d"
done
#    0b) Jedes GETEILTE Artefakt, das Schritt 1, 2 und 4 überschreiben oder entfernen — die Wrapper
#        (mit dem Runner geteilt), die beiden sudoers-Dateien (maschinenweit wirksam), das Binary,
#        die Unit und das Drop-in. Der Pfad bleibt im Sicherungsbaum erhalten, damit der Rückweg ein
#        gerades Zurückkopieren ist. Ein nicht vorhandenes Artefakt wird BENANNT, nicht stillschweigend
#        übersprungen: sonst hält man einen unvollständigen Rückweg für einen vollständigen.
for f in /usr/local/bin/devlabd \
         /etc/systemd/system/devlabd.service \
         /etc/systemd/system/devlabd.service.d/runs.conf \
         /usr/local/sbin/devlab-exec \
         /usr/local/sbin/devlab-mkworkspace \
         /usr/local/sbin/devlab-install \
         /usr/local/sbin/devlab-restart-when-free \
         /usr/local/sbin/devlab-restart-idle \
         /usr/local/sbin/devlab-deploy \
         /usr/local/sbin/devlab-preview \
         /etc/sudoers.d/devlab \
         /etc/sudoers.d/devlab-runs; do
  if sudo test -e "$f"; then
    sudo install -d -m0700 "$BAK$(dirname "$f")" && sudo cp -a "$f" "$BAK$f" && echo "gesichert: $f"
  else
    echo "nicht vorhanden, nichts zu sichern: $f"
  fi
done
sudo find "$BAK" -type f -printf '%M %u:%g %p\n' | sort   # der Rückweg, Zeile für Zeile

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
#    Die drei zurückgezogenen Wrapper sind in Schritt 0b gesichert — deshalb ist ihr Entfernen
#    umkehrbar, ohne einen alten Checkout zu brauchen.
sudo rm -f /usr/local/sbin/devlab-restart-idle /usr/local/sbin/devlab-deploy /usr/local/sbin/devlab-preview
sudo mv /etc/devlab/deploy.d "$BAK/deploy.d"       # per-Repo-Skriptmechanik außer Betrieb (B-44)
#    Was in /etc/devlab liegen BLEIBT und im Neubau keinen Leser mehr hat, wird benannt statt
#    vergessen: die per-Repo-Bauhilfen der alten Deploy-Mechanik und der Schlüssel des alten
#    prod-Versands. Der Neubau liefert nur „dev" aus (deploy.SendProd hat keinen Aufrufer), also ist
#    das eine Aufräum-Entscheidung des Betreibers, keine Voraussetzung des Cutovers.
sudo ls -l /etc/devlab

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
#    Zwei Tabellen unten führen beide Quellen Zeile für Zeile: „Die Alt-Unit" (die Werte, die die
#    installierte Unit trug und die Schritt 2 vollständig überschreibt) und „Das Alt-Drop-in".
#      Aus der ALT-UNIT ins Drop-in:  DEVLAB_COOKIE_DOMAIN=<cookie-domain>
#      Aus der ALT-UNIT ersatzlos:    DEVLAB_REPOS_PATH, DEVLAB_LINKS, DEVLAB_WORKSPACES,
#                                     DEVLAB_STATIC_DIR (leiten sich aus DEVLAB_STATE_DIR ab)
#      Im Drop-in ENTFERNEN:  PrivateTmp=true, DEVLAB_RUNS_MODE, DEVLAB_RUNS_AGENT_TIMEOUT
#      Im Drop-in UMBENENNEN: DEVLAB_RUNS_AUTOMERGE        → DEVLAB_RUNS_AUTOMERGE_WINDOW
#                             DEVLAB_RUNS_LIMIT_MAXRESUMES → DEVLAB_RUNS_LIMIT_MAX_RESUMES
#      ERGÄNZEN:              DEVLAB_GH_OWNER=<github-owner>   (PFLICHT, kein Default mehr)
#      NOCH NICHT setzen:     DEVLAB_RUNS_USER, DEVLAB_RUNS_TOKEN_USER — sie kommen in Schritt 7,
#                             nach den Prüfungen. Ohne sie wendet sich der Dienst an NICHTS ausserhalb.
#      BLEIBT:                DEVLAB_RUNS_MAX_DURATION, DEVLAB_RUNS_MAX_CONCURRENCY (nur Startwert),
#                             DEVLAB_RUNS_LIMIT_BACKOFF, DEVLAB_RUNS_TICK
sudo systemctl daemon-reload
#    Die zusammengeführte Wahrheit lesen — ein Drop-in gewinnt gegen die Vorlage, also wird
#    geprüft, was systemd am Ende sieht, nicht was in der Vorlage steht:
systemd-analyze cat-config systemd/system/devlabd.service | grep -E '^(PrivateTmp|ReadWritePaths)='
systemctl show -p Environment devlabd | tr ' ' '\n' | grep -E 'DEVLAB_RUNS_(MODE|AGENT_TIMEOUT|AUTOMERGE|LIMIT_MAXRESUMES)=' \
    && echo 'ÜBRIG: eine zurückgezogene Variable steht noch im Drop-in (Schritt 2)'
#    Kein Instanzwert der Alt-Unit darf still verloren gehen. Der eine, den die Vorlage bewusst
#    nicht trägt, muss jetzt im Drop-in stehen:
systemctl show -p Environment devlabd | tr ' ' '\n' | grep -q '^DEVLAB_COOKIE_DOMAIN=' \
    || echo 'FEHLT: DEVLAB_COOKIE_DOMAIN — die Alt-Unit trug ihn; ohne ihn mintet der Dienst host-eigene Cookies NEBEN den domänenweiten'
#    Und die vier abgeleiteten Pfade dürfen NICHT mitwandern (eine Wiederholung ist eine zweite
#    Wahrheit; DEVLAB_REPOS_PATH ist zusätzlich die Sandkasten-Basis des dev-Bypass):
systemctl show -p Environment devlabd | tr ' ' '\n' | grep -E '^DEVLAB_(REPOS_PATH|LINKS|WORKSPACES|STATIC_DIR)=' \
    && echo 'ÜBRIG: ein abgeleiteter Pfad ist noch gesetzt (Tabelle „Die Alt-Unit, Zeile für Zeile")'
#    Die Adresse trug die Alt-Unit als Flag am ExecStart. Das neue Binary parst KEINE Flags — es
#    liest allein DEVLAB_ADDR. Ein mitgeschlepptes Flag wirkt still nicht, und die Adresse muss die
#    sein, auf die die Kante zeigt, sonst antwortet der Dienst niemandem:
systemctl show -p ExecStart --value devlabd | grep -q -- '--listen' \
    && echo 'ÜBRIG: --listen am ExecStart — das Binary liest die Adresse aus DEVLAB_ADDR'
systemctl show -p Environment --value devlabd | tr ' ' '\n' | sed -n 's/^DEVLAB_ADDR=//p'
sudo grep -rh 'reverse_proxy' /etc/caddy/conf.d/ 2>/dev/null | sort -u   # zeigt die Adressen der Kante

# 3) Alt-Marker entfernen (REQ-039.3) — die Doppel-Falle existiert nicht mehr
sudo rm -f "$STATE_DIR/mercury/run-active" "$STATE_DIR/mercury/runs-active"

# 4) Bauen als UNPRIVILEGIERTER User (root baut nie), dann install-only
(cd backend && go build -o /tmp/devlab-build/devlabd ./cmd/devlabd && go build -o /tmp/devlab-build/devlab-migrate ./cmd/devlab-migrate)
npm ci && npm run build
sudo install -o root -g root -m0755 /tmp/devlab-build/devlabd /usr/local/bin/devlabd
#    Das Migrations-Binary wird MIT installiert statt aus /tmp gestartet: Schritt 5 führt es als der
#    Dienst-Account aus, und ob der ein Verzeichnis unter /tmp betreten darf, hängt an der umask des
#    bauenden Kontos. Ein installiertes Binary hängt an nichts.
sudo install -o root -g root -m0755 /tmp/devlab-build/devlab-migrate /usr/local/bin/devlab-migrate
#    Die SPA ins Web-Root — OHNE Eigentümer und Modus des ZIELS umzuschreiben. `rsync -a` überträgt
#    Eigentümer, Gruppe und Rechte des QUELL-Verzeichnisses auf das Ziel: aus einem 0750-Web-Root
#    eines eigenen Kontos würde das bauende Konto mit dem Modus des Build-Verzeichnisses. Also erst
#    messen, dann ohne diese drei Attribute übertragen, dann prüfen, dass das Ziel unverändert ist
#    und der Dienst die neue SPA lesen kann.
web_before="$(sudo stat -c '%U:%G %a' "$STATE_DIR/www")"
sudo rsync -a --delete --no-owner --no-group --no-perms --omit-dir-times dist/ "$STATE_DIR/www/"
web_after="$(sudo stat -c '%U:%G %a' "$STATE_DIR/www")"
[ "$web_before" = "$web_after" ] \
    && echo "Web-Root unverändert: $web_after" \
    || echo "GEÄNDERT: Web-Root war $web_before, ist $web_after — zurücksetzen (chown/chmod), sonst liest ein anderes Konto als vorher"
sudo -u "$SVC_USER" test -r "$STATE_DIR/www/index.html" \
    && echo 'die neue SPA ist für den Dienst-Account lesbar' \
    || echo 'FEHLT: der Dienst-Account kann die SPA nicht lesen — die Oberfläche bliebe leer'

# 5) Datenmigration VOR dem ersten Start (B-9; migrate verweigert bei laufendem Dienst)
#    Der Export liegt beim Betreiber, und dort kann der Dienst-Account NICHTS lesen: ein Home ist
#    typischerweise 0750 und für ein fremdes Konto nicht einmal durchquerbar, also scheitert schon
#    `test -r`. Deshalb wird der Export an einen Ort gebracht, den der Dienst-Account lesen KANN,
#    die Lesbarkeit wird VOR dem Import geprüft, und danach wird die Ablage entfernt: der
#    Roh-Export trägt die Prompts und Aufgaben dieser Instanz.
IMPORT_DIR="$STATE_DIR/import"
sudo install -d -o "$SVC_USER" -g "$SVC_USER" -m0700 "$IMPORT_DIR"
sudo install -o "$SVC_USER" -g "$SVC_USER" -m0600 "$EXPORT" "$IMPORT_DIR/mercury-runs-roh.json"
sudo -u "$SVC_USER" test -r "$IMPORT_DIR/mercury-runs-roh.json" \
    && echo 'Vorbedingung erfüllt: der Dienst-Account kann den Export lesen' \
    || echo 'ABBRUCH: der Export ist für den Dienst-Account nicht lesbar — der Import endete mit Exit 5'
#    Erst die Probe (schreibt nichts, druckt das ganze Protokoll), dann der Import selbst. Beide
#    Ausgaben füllen die Ergebnis-Spalten in 10-daten.md.
sudo -u "$SVC_USER" env DEVLAB_STATE_DIR="$STATE_DIR" devlab-migrate --input "$IMPORT_DIR/mercury-runs-roh.json" --dry-run
sudo -u "$SVC_USER" env DEVLAB_STATE_DIR="$STATE_DIR" devlab-migrate --input "$IMPORT_DIR/mercury-runs-roh.json"
sudo rm -rf "$IMPORT_DIR"
sudo test ! -e "$IMPORT_DIR" && echo 'Export-Ablage entfernt'

# 6) Erster Start — ZURÜCKGEHALTEN (ohne Runner-Identität; siehe „Fremdwirkung")
sudo systemctl start devlabd && systemctl status devlabd --no-pager
systemctl show -p Environment devlabd | grep -q 'DEVLAB_GH_OWNER=[^[:space:]]' \
    || echo 'FEHLT: DEVLAB_GH_OWNER — die Repo-Menge bleibt leer (Schritt 2)'
#    Adresse aus der Konfiguration ableiten statt einen Port zu behaupten: Ports sind
#    Laufzeit-Konfiguration (REQ-044), und ein hart notierter Port prüft nach der ersten
#    Umkonfiguration den falschen Socket. Die Adresse als GANZES vergleichen — eine nackte Portzahl
#    trifft in `ss`-Ausgaben auch Prozess-Nummern.
addr="$(systemctl show -p Environment --value devlabd | tr ' ' '\n' | sed -n 's/^DEVLAB_ADDR=//p' | tail -n1)"
[ -n "$addr" ] || echo 'FEHLT: DEVLAB_ADDR — ohne Adresse ist nicht prüfbar, wo der Dienst hört'
ss -tlnp | grep -F "$addr" && curl -fsS "http://$addr/api/health"
#    Die Bereitschafts-Probe läuft ALS DER DIENST-ACCOUNT: der Socket ist 0660 und gehört dem
#    Dienst-Account und dessen Gruppe (api.SocketMode). Das Betreiber-Konto ist in dieser Gruppe
#    nicht, `curl` als Betreiber scheitert also mit „Permission denied" — was nichts über den
#    Dienst sagt. Erwartet: 204 (frei). 423 hiesse „belegt", und das wäre nach einem gehaltenen
#    Start ein Befund, kein Zustand.
sudo -u "$SVC_USER" curl -fsS --unix-socket "$STATE_DIR/restart-ready.sock" http://x/ready \
    -o /dev/null -w '%{http_code}\n'
#    Der Beweis, dass der Start wirklich gehalten hat: die drei Pässe, die nach aussen wirken,
#    benennen ihren fehlenden Zugang, statt zu handeln.
systemctl show -p Environment devlabd | tr ' ' '\n' | grep -E '^DEVLAB_RUNS_(USER|TOKEN_USER)=' \
    && echo 'NICHT GEHALTEN: die Runner-Identität steht schon im Drop-in — sie gehört in Schritt 7'
journalctl -u devlabd --since '-5 min' | grep -E 'no runner account configured|reporter OFF'

# 6a) Was der nächste Tick anfassen WÜRDE — messen, bevor er es tut (Zahlen dieser Instanz, nicht
#     dieses Dokuments). Die drei Werte sind die ganze Breite der Wartung: wie viele PR-Sätze nicht
#     blockiert sind, in wie vielen Repositories sie liegen, und wann der erste Auto-Merge fällig wird.
sudo -u "$SVC_USER" jq '[.prs[]] | length' "$STATE_DIR/mercury/runs-prs.json"
sudo -u "$SVC_USER" jq '[.prs[] | select(.blocked != true)] | length' "$STATE_DIR/mercury/runs-prs.json"
sudo -u "$SVC_USER" jq -r '[.prs[].repo] | unique | length' "$STATE_DIR/mercury/runs-prs.json"
sudo -u "$SVC_USER" jq -r '[.prs[] | select(.blocked != true) | .mergeBy] | min' "$STATE_DIR/mercury/runs-prs.json"
#     Und der Tagesbericht: ein noch offener Tag ist beim ERSTEN Reporter-Durchgang wieder fällig.
sudo -u "$SVC_USER" jq -r '.records[] | [.day, .status, .attempts, .lastError] | @tsv' "$STATE_DIR/mercury/daily-reports.json"

# 7) Runner-Identität einsetzen — ab hier wirkt der Dienst nach aussen
sudoedit /etc/systemd/system/devlabd.service.d/runs.conf   # DEVLAB_RUNS_USER, DEVLAB_RUNS_TOKEN_USER
sudo systemctl daemon-reload && sudo systemctl restart devlabd
#    Ab dem ersten Tick (DEVLAB_RUNS_TICK) läuft die PR-Wartung, und der Reporter tickt sofort.
#    Beides einmal beobachten, statt es anzunehmen:
journalctl -u devlabd --since '-2 min' | grep -E 'sched maintain|protection verify|daily-report reporter'
```

Danach folgt Schritt **A** aus `10-daten.md` (Axiom-Zuordnung) — erst dann wird ein Lauf
eingeschaltet.

## Fremdwirkung: was dieser Start ausserhalb von DevLab berührt

Vierzehn Stellen, nicht eine. Zwölf wirken ausserhalb dieses Hosts (GitHub und Mail), zwei auf
diesem Host ausserhalb von DevLab (die Nachbardienste). **Drei feuern in der ersten Minute eines
Starts** (1, 2, 4) und eine vierte beim Start selbst (12, der README-Keim) — sie sind der Grund für
die Reihenfolge in Schritt 6/7: alle vier brauchen die Runner-Identität, und ohne sie benennen sie
ihren fehlenden Zugang, statt zu handeln.

| # | Stelle | Feuert sie beim ersten Start? | Was sie schreibt | Wie sie bis zur Freigabe stillhält |
|---|---|---|---|---|
| 1 | Origin-Status je offenem PR (`deliver.Maintain` → `deliver/deliver.go:879 gh.PostCommitStatus`) | **JA — beim ersten Scheduler-Tick** (`DEVLAB_RUNS_TICK`), und danach bei JEDEM Tick, unbedingt | einen Commit-Status auf dem Kopf-Commit JEDES offenen PRs jedes verwalteten Repositories — auch in PRs, die ein Mensch gestellt hat und die dieser Cutover nichts angehen | **Schalter:** ohne `DEVLAB_RUNS_MAINTAIN_ENFORCE` schreibt die Wartung NICHTS — sie stellt den Stillstand aus den eigenen Pools fest und meldet ihn, ohne einen einzigen fremden Aufruf. **Und Reihenfolge (Schritt 6):** ohne `DEVLAB_RUNS_USER`/`DEVLAB_RUNS_TOKEN_USER` löst `runnerToken` gar keinen Token auf. Die Probe unter der Tabelle liest, ob dieser Build den Schalter trägt |
| 2 | Branch-Löschung nach Merge (`finalizeMerged` → `deliver/deliver.go:901 gh.DeleteBranch`) | **JA — beim ersten Tick**, für jeden verfolgten PR, der inzwischen gemerged ist (auch von einem Menschen) | löscht den Lieferungs-Branch im fremden Repository (404 gilt als erfüllt) | wie 1 (derselbe Schalter deckt die ganze Wartung ab) |
| 3 | Auto-Merge (`deliver/deliver.go:843 gh.MergePullRequest`) | beim ersten Tick, dessen `mergeBy` erreicht ist — bei Messung 6a war das keiner; die Frist der Alt-Sätze liegt Wochen in der Zukunft | einen Merge-Commit in den Standard-Branch des fremden Repositories, höchstens einer je Repository und Tick | wie 1, zusätzlich die Frist selbst (`DEVLAB_RUNS_AUTOMERGE_WINDOW` sät nur den ersten Start; gespeicherte Sätze tragen ihre eigene Frist) |
| 4 | Tagesbericht-Mail (`api/handlers_mercury_report.go:181 cl.Send` → Landschafts-Mailer) | **JA — sofort beim Start**, denn der Reporter macht einen Durchgang vor dem ersten Intervall | eine Mail an den Empfänger, für jeden abgeschlossenen Tag im Rückblick-Fenster ohne Zustellsatz — und für jeden noch offenen Satz, gleich wie alt, weil ein `failed`-Satz ohne Backoff sofort wieder fällig ist | **Reihenfolge (Schritt 6):** ohne `DEVLAB_RUNS_USER` ist der Reporter AUS („reporter OFF (no run user provisioned)"). Messung 6a zeigt, welcher Tag sonst als erstes ginge |
| 5 | Verzweigungsschutz-Durchlauf (`deliver.VerifyProtection` → `deliver/deliver.go:560 gh.ProtectDefaultBranch`, `deliver/deliver.go:597 gh.ProtectDefaultBranch`) | nein — der erste Durchgang wartet `DEVLAB_RUNS_PROTECTION_START_DELAY` (Vorgabe 15m) | liest den Schutz JEDES Repositories der Organisation; scharf PATCHt er den Standard-Branch jedes abweichenden | zwei Schalter (unten) **und** die Reihenfolge aus 1: auch dieser Pass braucht den Runner-Token |
| 6 | PR öffnen (`deliver.OpenOrAdopt` → `deliver/deliver.go:146 gh.CreatePullRequest`) | nein — nur innerhalb einer Ausführung | einen Pull Request im Ziel-Repository (oder adoptiert den offenen) | kein Lauf ist eingeschaltet (Aktivierungs-Sperre, `10-daten.md` Schritt A); zusätzlich Reihenfolge aus 1 |
| 7 | Origin-Status direkt nach dem Öffnen/Adoptieren (`deliver/deliver.go:684 gh.PostCommitStatus`) | nein — nur innerhalb einer Ausführung | einen Commit-Status auf dem Kopf-Commit dieses PRs | wie 6 |
| 8 | Push des Arbeitsstands und des Lieferungs-Branches (`executor/stages.go:497 wb.PushBranch` über `api/exec_deps.go:591 b.PushBranch`) | nein — nur innerhalb einer Ausführung | zwei Branches im fremden Repository (`mercury-dev` und der Lieferungs-Branch) | wie 6 |
| 9 | Repository anlegen (`executor/stages.go:313 gh.CreateRepo`; der geschützte Zwilling: `deliver/deliver.go:200 gh.CreateRepo`) | nein — nur innerhalb einer Ausführung, und nur für ein Ziel, das es nicht gibt | ein NEUES privates Repository in der konfigurierten Organisation | wie 6 |
| 10 | Verzweigungsschutz direkt nach der Anlage (`api/exec_deps.go:731 ops.ProtectDefaultBranch`) | nein — nur mit 9 | den Schutz des Standard-Branches des neu angelegten Repositories | wie 6 |
| 11 | PR schliessen (`deliver/deliver.go:338 gh.ClosePullRequest`, `deliver/deliver.go:393 gh.ClosePullRequest`) | nein — nur auf eine Bedienhandlung (Rücknahme, Gegenbuchung) | schliesst einen PR im fremden Repository mit benanntem Grund | niemand bedient sie während des Cutovers |
| 12 | Verfassungs-Repository: Klon und Push (`axiomrepo/store.go:420 s.git`) | **beim Start, sobald eine Runner-Identität steht** — `api.New` startet den README-Keim (`api/api.go:137 s.axioms.EnsureReadme` → `axiomrepo/readme.go:28 s.Put`) als Hintergrund-Aufgabe. Er ist create-only, feuert also höchstens einmal; jeder weitere Push ist ein Verfassungs-Schreibvorgang, und der erste davon ist Schritt **A1** | klont das Verfassungs-Repository und legt, wenn es keine README hat, EINEN Commit darauf an; später je Verfassungs-Änderung einen | im gehaltenen Start ist `axiomsTokenUser()` leer, also startet die Hintergrund-Aufgabe gar nicht. Ab Schritt 7 ist der README-Keim beabsichtigt |
| 13 | dev-Auslieferung eines NACHBARDIENSTES (`api/exec_deps.go:789 deploy.DeliverDev` → `devlab-install`) | nein — nur innerhalb einer Ausführung | Unit, `/opt/<repo>`, Rechte-Manifest, eine Route im GETEILTEN Caddy-Verzeichnis, und einen Neustart genau dieser Unit | wie 6; der Wrapper validiert die Kante vor der Übernahme und nimmt die eigene Datei bei Fehlschlag zurück |
| 14 | Der Cutover selbst (Schritt 1) | ja, von Hand | `/etc/sudoers.d` (maschinenweit) und `/usr/local/sbin` (mit dem Runner geteilt) | jede sudoers-Datei wird EINZELN geprüft, bevor sie eingebaut wird, und Schritt 0b sichert jedes Artefakt |

Jeder Anker der Tabelle nennt `datei:zeile` **und den Aufruf**, der dort steht — nicht aus Sorgfalt,
sondern damit er prüfbar ist: eine blosse Zeilennummer veraltet beim nächsten Commit über der Datei,
und zwar lautlos. Prüfung `doc-b` in `tools/abnahme.sh` liest jeden Anker dieser Dokumente und
verlangt, dass die genannte Zeile den genannten Aufruf wirklich trägt; ein Anker ohne Aufruf
scheitert ebenfalls, sonst liesse sich die Prüfung durch Weglassen umgehen.

Nur LESEND, aber nach aussen: `ListOpenPullRequests`/`GetPullRequest` je Tick, `GetProtection`/
`DefaultBranch` je Schutz-Durchlauf, die Repo-Menge je Auflösung, und die Anlauf-Abstimmung
(`SyncStartupTodos`), die sofort beim Booten läuft. Sie schreiben nichts, verbrauchen aber
API-Kontingent und stehen im Audit-Log der Organisation. Mit der Reihenfolge aus 1 fällt auch das
weg: die Abstimmung meldet „startup reconciliation deferred: no runner account configured".

Die zwei Schalter des Schutz-Durchlaufs (Stelle 5):

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

Die Stellen 1–3 sind die PR-Wartung, und sie ist der Grund für die Reihenfolge: sie läuft ab dem
ersten Tick, und ihr Origin-Status-Durchgang schreibt bei JEDEM Tick auf jeden offenen PR jedes
verwalteten Repositories — die Kadenz, in der dieser Dienst nach aussen schreibt, ist also
`DEVLAB_RUNS_TICK`. Zwei Dinge halten sie zurück, und sie sind nicht dasselbe: ein SCHALTER ist eine
Eigenschaft der Software und gilt auch für den, der die Anleitung nicht liest; die REIHENFOLGE aus
Schritt 6/7 ist eine Eigenschaft des Vorgehens und gilt nur, solange man sie einhält. Stelle 4 (die
Mail) hat nur die Reihenfolge.

Welche Schalter dieser Build mitbringt, wird nicht behauptet, sondern gelesen — im Binary, das gerade
installiert wurde, und in der Vorlage, die die Umgebung dokumentiert:

```sh
# NICHT verankert (^…$): Go packt seine String-Konstanten ohne Trenner in einen Block, eine
# verankerte Suche findet deshalb NICHTS und meldete „kein Schalter", wo zwei sind.
strings /usr/local/bin/devlabd | grep -oE 'DEVLAB_RUNS_[A-Z_]*ENFORCE' | sort -u
grep -nE 'DEVLAB_RUNS_[A-Z_]*ENFORCE' deploy/devlabd.service
```

Erwartet wird `DEVLAB_RUNS_MAINTAIN_ENFORCE`: ungeschärft ist der Vorgabewert, und geschärft wird er
nach denselben Prüfungen wie der Schutz-Durchlauf — nach Schritt 6a, wenn der PR-Pool angesehen ist:

```sh
sudoedit /etc/systemd/system/devlabd.service.d/runs.conf    # Environment=DEVLAB_RUNS_MAINTAIN_ENFORCE=1
sudo systemctl daemon-reload && sudo systemctl restart devlabd
```

Findet die Probe keinen Schalter, hält allein die Reihenfolge — und dann ist die Kadenz des
Origin-Status (`DEVLAB_RUNS_TICK`) die Kadenz, in der dieser Dienst in fremde Repositories schreibt.

Alles andere, was dieser Cutover anfasst, ist geteilt aber lokal, und jeder Schritt prüft vor dem
Übernehmen: `visudo -c -f` je sudoers-Datei vor dem Einbau (Schritt 1), `caddy validate` vor dem
Übernehmen einer Route und Rücknahme der eigenen Datei bei Fehlschlag (im Install-Wrapper), das
Messen von Eigentümer und Modus des Web-Roots vor und nach der Auslieferung (Schritt 4), und der
Neustart eines Nachbardienstes findet nicht statt — der Wrapper startet nur die Unit, die er gerade
installiert hat.

## Die Alt-Unit, Zeile für Zeile

Schritt 2 überschreibt die installierte Unit VOLLSTÄNDIG. Alles, was sie trug und die Vorlage nicht
trägt, ist damit weg — still, denn eine fehlende Umgebungsvariable ist kein Fehler, sondern ein
anderes Verhalten. Deshalb steht hier jede Zeile der Alt-Unit mit ihrem Verdikt. Die Werte selbst
sind Instanzwerte und stehen als Platzhalter.

| Zeile der Alt-Unit | Verdikt |
|---|---|
| `ExecStart=…/devlabd --listen <adresse>` | **ersetzt** — die Adresse ist jetzt `DEVLAB_ADDR` (Vorlage). Das neue Binary parst KEINE Flags: ein mitgeschlepptes `--listen` wirkt still nicht, und die Adresse muss die sein, auf die die Kante zeigt (Prüfung in Schritt 2) |
| `DEVLAB_COOKIE_DOMAIN=<cookie-domain>` | **ins Drop-in übernehmen** — Instanzwert, in der Vorlage bewusst nicht. Ohne ihn mintet der Dienst beim Refresh host-eigene Cookies NEBEN den domänenweiten der Landschaft; welche der beiden der Browser dann sendet, entscheidet der Zufall, und ein Logout der Landschaft räumt die host-eigene nicht mit weg |
| `DEVLAB_REPOS_PATH=<pfad>` | **entfällt, und darf nicht mitwandern** — der Wert ist nur die Sandkasten-Basis des dev-Bypass. Auf ein Betreiber-Home gesetzt zeigt er die Auflösung auf fremde Arbeitskopien statt auf die per-User-Workspaces unter dem State-Root |
| `DEVLAB_LINKS`, `DEVLAB_WORKSPACES`, `DEVLAB_STATIC_DIR` | **entfallen** — sie leiten sich aus `DEVLAB_STATE_DIR` ab (`internal/statepath`). Die historischen Overrides funktionieren weiter, aber eine Wiederholung des abgeleiteten Werts ist keine Konfiguration, sondern eine zweite Wahrheit |
| `HOLISTIC_SECRET_FILE`, `HOLISTIC_REVOKED`, `DEVLAB_COOKIE_SECURE`, `DEVLAB_LINK_ENC_KEY_FILE`, `DEVLAB_GITHUB_OAUTH_FILE` | **stehen in der Vorlage** — nur dann ins Drop-in, wenn diese Instanz vom Vorlagenwert abweicht |
| `StateDirectory`, `NoNewPrivileges`, `ProtectSystem`, `ProtectHome`, `PrivateTmp` | **stehen in der Vorlage**, dort mit zwei Verschärfungen, die die Alt-Unit nicht hatte: `StateDirectoryMode=0711` (systemds Vorgabe 0755 liess jedes lokale Konto den State-Root AUFLISTEN) und `UMask=0027` |
| `ReadWritePaths=<state-root>` | **erweitert** — die Vorlage nennt zusätzlich `/tmp`, weil `ProtectSystem=strict` auch `/tmp` schreibgeschützt macht und jedes per-User-Kind dort seinen Scratch braucht |

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
| `DEVLAB_RUNS_USER` | **bleibt** — Instanzwert: das rechtelose OS-Konto des Runners. Aber erst in Schritt 7: solange es fehlt, wendet sich der Dienst an nichts ausserhalb (Tabelle „Fremdwirkung", Stellen 1–5) |
| `DEVLAB_RUNS_TOKEN_USER` | **bleibt** — Instanzwert: das verknüpfte GitHub-Konto, mit dem geklont/gepusht wird. Ebenfalls erst in Schritt 7 |
| `DEVLAB_RUNS_MAX_DURATION` | **bleibt** — harte Wanduhr-Grenze je Versuch im Scheduler |
| `DEVLAB_RUNS_MAX_CONCURRENCY` | **bleibt** — aber nur als STARTWERT; eine Laufzeit-Änderung gewinnt (REQ-013.2) |
| `DEVLAB_RUNS_LIMIT_BACKOFF` | **bleibt** — Wartezeit auf das echte Usage-Limit-Fenster |
| `DEVLAB_RUNS_TICK` | **bleibt** — Takt des Fälligkeits-Tickers, und damit auch der Takt, in dem die PR-Wartung nach aussen wirkt |
| `DEVLAB_GH_OWNER` | **ergänzen** — PFLICHT, kein Default mehr; ohne ihn bleibt die Repo-Menge leer |
| `DEVLAB_COOKIE_DOMAIN` | **ergänzen** — der Instanzwert der Alt-Unit (Tabelle „Die Alt-Unit, Zeile für Zeile") |
| `DEVLAB_RUNS_PROTECTION_ENFORCE` | **später ergänzen** — erst nach dem ersten gemeldeten Schutz-Durchlauf (siehe oben) |
| `DEVLAB_RUNS_MAINTAIN_ENFORCE` | **später ergänzen** — erst nachdem der PR-Pool angesehen ist (Schritt 6a). Ungeschärft meldet die Wartung ihren Stillstand und macht keinen fremden Aufruf |

`DEVLAB_STATE_DIR`, `DEVLAB_ADDR`, `DEVLAB_RUNS_DRAIN_GRACE`, `DEVLAB_RUNS_RESUME_WINDOW` und
`DEVLAB_RUNS_PROTECTION_START_DELAY` stehen in der Vorlage. Sie gehören nur dann ins Drop-in, wenn
diese Instanz von der Vorlage abweicht — eine Wiederholung des Vorlagenwerts ist keine Konfiguration,
sondern eine zweite Wahrheit.

## Rollback

Rückwärts, mit den Sicherungen aus Schritt 0 — die Reihenfolge ist die umgekehrte des Cutovers, weil
der Dienst nichts lesen darf, was gerade zurückgespielt wird. `$BAK` ist das Sicherungsverzeichnis
dieses Durchgangs.

1. `sudo systemctl stop devlabd`
2. **Schritt 7/2 zurück (Unit und Drop-in):**
   `sudo cp -a "$BAK/etc/systemd/system/devlabd.service" /etc/systemd/system/devlabd.service` und
   `sudo cp -a "$BAK/etc/systemd/system/devlabd.service.d/runs.conf" /etc/systemd/system/devlabd.service.d/runs.conf`,
   dann `sudo systemctl daemon-reload`.
3. **Schritt 5 zurück (Daten) — in drei Teilen.** `tar -x` ÜBERSCHREIBT, aber es LÖSCHT nichts:
   Entpacken allein liesse jedes Artefakt liegen, das erst der Neubau angelegt hat, und der
   Zustandsbaum trüge danach beide Welten — den alten Bestand und daneben `executions/`,
   `runs-results.imported`, die `.pre-migration`-Ablagen und die Pools, die es vorher nicht gab.
   ```sh
   # 3a) Was der Neubau NEU angelegt hat, benannt entfernen (der Alt-Stand kennt nichts davon)
   sudo rm -rf "$STATE_DIR/mercury/executions" \
               "$STATE_DIR/mercury/runs-results.imported" \
               "$STATE_DIR/mercury/settings.json" \
               "$STATE_DIR/mercury/ai-usage.json" \
               "$STATE_DIR/mercury/usage-limit.json" \
               "$STATE_DIR/mercury/restart.json" \
               "$STATE_DIR/mercury/order.json"
   #      Die Ablagen der Übernahme — Datei UND Verzeichnis, auch die durchnummerierten Wiederholungen
   sudo find "$STATE_DIR/mercury" -maxdepth 1 -name '*.pre-migration*' -exec rm -rf {} +
   # 3b) Drei Bäume schreibt der Cutover als GANZES neu: den Konfigurations-Verlauf (der Import legt
   #     einen zusätzlichen Schnappschuss an), die SPA (Schritt 4 legt neue, anders benannte Dateien
   #     darüber) und den Verfassungs-Klon. Sie werden ERSETZT statt überlagert — aber nur, wenn der
   #     Tarball sie wirklich trägt, sonst wäre das Entfernen ein Datenverlust statt eines Rückwegs.
   for d in mercury/runs-history www axioms; do
     if sudo tar -tzf "$BAK/devlab-state.tar.gz" | grep -q "^$d/"; then
       sudo rm -rf "$STATE_DIR/$d" && echo "wird ersetzt: $d"
     else
       echo "NICHT im Tarball — bleibt stehen, Entfernen wäre Verlust: $d"
     fi
   done
   # 3c) Zustand zurückspielen; der Tarball enthält mercury links chats comments www axioms
   sudo tar -C "$STATE_DIR" -xzf "$BAK/devlab-state.tar.gz"
   # 3d) Beweis, dass nichts aus dem Neubau übrig ist — die Liste MUSS leer sein
   sudo find "$STATE_DIR/mercury" -maxdepth 1 \
        \( -name executions -o -name 'runs-results.imported' -o -name '*.pre-migration*' \
           -o -name settings.json -o -name ai-usage.json -o -name usage-limit.json \
           -o -name restart.json -o -name order.json \) -print
   ```
   Das holt auch die **alte SPA** zurück, die Schritt 4 überschrieben hat, und den
   **Verfassungs-Klon** `axioms` mit seinem nicht gepushten Stand. Ein Neu-Klon der Verfassung
   ersetzt Letzteren NICHT. Die hand-gemachten Kopien `runs.json.bak-*` hat die Übernahme nie
   angefasst; sie liegen unverändert dort, wo der Betreiber sie hingelegt hat. Der by-hand-Weg ohne
   Tarball steht in `10-daten.md` („Rollback").
4. **Schritt 4 zurück (Binaries):**
   `sudo cp -a "$BAK/usr/local/bin/devlabd" /usr/local/bin/devlabd`, und
   `sudo rm -f /usr/local/bin/devlab-migrate` (der Alt-Stand kannte es nicht).
5. **Schritt 1 zurück (Wrapper und sudoers) — jedes gesicherte Artefakt, in der umgekehrten
   Reihenfolge seiner Installation.** Die Schleife spielt genau zurück, was Schritt 0b gesichert hat,
   und benennt, was der Alt-Stand nicht hatte:
   ```sh
   for f in /etc/sudoers.d/devlab-runs /etc/sudoers.d/devlab \
            /usr/local/sbin/devlab-preview /usr/local/sbin/devlab-deploy \
            /usr/local/sbin/devlab-restart-idle /usr/local/sbin/devlab-restart-when-free \
            /usr/local/sbin/devlab-mkworkspace /usr/local/sbin/devlab-install \
            /usr/local/sbin/devlab-exec; do
     if sudo test -e "$BAK$f"; then sudo cp -a "$BAK$f" "$f" && echo "zurück: $f"
     else sudo rm -f "$f" && echo "im Alt-Stand nicht vorhanden, entfernt: $f"; fi
   done
   sudo visudo -c                                   # nach dem Zurückspielen, vor dem Start
   sudo mv "$BAK/deploy.d" /etc/devlab/deploy.d      # per-Repo-Skriptmechanik zurück
   ```
6. `sudo systemctl start devlabd`

`/etc/devlab/gh-owner` bleibt liegen: die Datei ist auch für den alten Stand harmlos und wird beim
nächsten Anlauf wieder gebraucht. Die Export-Ablage aus Schritt 5 ist dort bereits entfernt; liegt
sie noch, gehört sie gelöscht — sie trägt die Prompts der Instanz.
