# Mercury Automatische Läufe — Phase 2 Provisioning (autonome Ausführung)

Der Executor-Code ist gebaut, aber **inert**, bis du hier provisionierst. Ohne diese Schritte läuft
nur die Verwaltungsschicht; der Scheduler loggt `runs scheduler OFF` und tut nichts.

> **Sicherheit:** Ein unbeaufsichtigter Agent, der implementiert, pusht und deployt, ist die
> risikoreichste Konfiguration. Der Agent läuft daher als **dedizierter, unprivilegierter** Linux-User
> — **niemals `nanu`** (dessen passwortloses sudo würde ihn zur RCE-Maschine machen). Der einzige
> privilegierte Schritt (Deploy) läuft **nicht** im Agenten, sondern über den root-Wrapper
> `devlab-deploy`, der ausschließlich pro Repo vorab freigegebene Skripte ausführt.

## 1. Runner-User anlegen (unprivilegiert)

```sh
sudo useradd -m -s /bin/bash devlab-runs
sudo usermod -aG hp_devlab_access devlab-runs   # nötig, damit devlabd via devlab-exec als er laufen darf
# KEIN sudo für devlab-runs. Prüfen:
sudo -l -U devlab-runs   # darf NICHTS (außer ggf. nichts) zeigen
```

## 2. Claude als Runner authentifizieren

```sh
sudo -u devlab-runs -i
claude login          # Opus-fähiges Abo/Key in ~devlab-runs/.claude
# optional Defaults (Modell/Effort) setzen; der Executor übergibt --model opus --effort max
exit
```

## 3. GitHub-Token des Runners (für Klonen/Push/Merge)

Die OS-Identität (wer Claude ausführt) und die Token-Identität (wer klont/pusht) sind entkoppelt:

- **`DEVLAB_RUNS_USER`** = OS-Konto **und Workspace-Besitzer**. Muss der dedizierte, unprivilegierte
  `devlab-runs` sein — **niemals** ein Konto, das ein Mensch interaktiv in der DevLab-IDE benutzt. Der
  Runner räumt seinen Workspace vor jedem Lauf hart auf (`git reset --hard` + `clean -fdx`); zeigte
  `DEVLAB_RUNS_USER` auf ein Menschen-Konto, würde ein Nachtlauf dessen ungesicherte Änderungen im
  gleichnamigen IDE-Workspace stillschweigend löschen.
- **`DEVLAB_RUNS_TOKEN_USER`** = Link-Store-Schlüssel, dessen GitHub-Token klont/pusht/merged (und in
  dessen Namen committet wird). Hierhin gehört ein bestehendes verknüpftes Konto mit **write**-Scope
  auf die Holistic-Repos — z. B. der Owner. Fehlt die Variable, fällt sie auf `DEVLAB_RUNS_USER` zurück.

Verknüpfe also entweder für `devlab-runs` selbst ein GitHub-Konto (normaler DevLab-Link-Flow, als
dieser User eingeloggt), oder — üblicher — setze `DEVLAB_RUNS_TOKEN_USER=<owner>` und lass die
OS-Identität `devlab-runs`.

> **Kein sudo für den Runner.** `devlab-runs` erhält bewusst KEIN passwortloses sudo (siehe
> `devlab-runs.sudoers`). Ein unbeaufsichtigter Agent mit `bypassPermissions` würde jede sudo-Regel
> als Freibrief auf Root behandeln (systemd-Unit, `apt-get install`, Wildcard-Injection). Die
> Verfassungsvorgabe „passwordless sudo vorausgesetzt" gilt der **interaktiven** Implementierung mit
> Mensch davor, nicht dem Nacht-Runner. Der einzige privilegierte Schritt — Deploy — läuft über den
> root-Wrapper `devlab-deploy`, nie über die Shell des Agenten.

## 4. Deploy-Allowlist (nur für `full`-Modus)

Pro deploybarem Repo ein **geprüftes** Skript, root-eigen, nicht group/other-writable. Es baut+
installiert+startet aus dem übergebenen Workspace (`$1`).

```sh
sudo install -o root -g root -m 0755 deploy/devlab-deploy /usr/local/sbin/devlab-deploy
sudo mkdir -p /etc/devlab/deploy.d
sudo install -o root -g root -m 0755 deploy/deploy.d.example-devlab /etc/devlab/deploy.d/devlab
# ... pro Repo ein eigenes, das genau dieses Repo deployt.
```

Sudoers (eigene Datei, `visudo -f`):

```
# /etc/sudoers.d/devlab-runs  (siehe deploy/devlab-runs.sudoers)
devlab ALL=(root) NOPASSWD: /usr/local/sbin/devlab-deploy
```

## 5. Scheduler scharfschalten (systemd-Drop-in)

`DEVLAB_RUNS_MODE` ist die Sicherheitsleiter — **schrittweise** hochstufen:

| Mode | Verhalten |
|---|---|
| `off` (Default) | nichts läuft |
| `report` | klont + Claude im Plan-Modus (nur lesen), speichert den Bericht — **kein** Push/Deploy |
| `pr` | implementiert → committet → pusht Branch → öffnet PR (Mensch merged) |
| `full` | wie `pr` + Deploy aus dem committeten Workspace über `devlab-deploy` |

```sh
sudo systemctl edit devlabd
# [Service]
# PrivateTmp=true                          # PFLICHT: die Haupt-Unit hat ProtectSystem=strict +
#                                          # PrivateTmp=false → /tmp read-only; ohne dies scheitert
#                                          # JEDER Bash-Aufruf des Agenten (EROFS auf /tmp/claude-<uid>).
# Environment=DEVLAB_RUNS_MODE=report
# Environment=DEVLAB_RUNS_USER=devlab-runs       # OS-Konto + Workspace-Besitzer (dediziert, siehe §3)
# Environment=DEVLAB_RUNS_TOKEN_USER=<owner>     # GitHub-Token-Konto (getrennt, siehe §3)
# Environment=DEVLAB_RUNS_AUTOMERGE=720h         # 30 Tage (Default); Werte ≤0 werden ignoriert
# Environment=DEVLAB_RUNS_TICK=30s
sudo systemctl restart devlabd
journalctl -u devlabd -n5   # "runs scheduler ENABLED — mode=report ..."
```

### Optionale Sicherheits-Stellschrauben (alle mit sinnvollen Defaults)

| Env | Default | Bedeutung |
|---|---|---|
| `DEVLAB_RUNS_MAX_CONCURRENCY` | `2` | Obergrenze **gleichzeitig** laufender Läufe. Schützt Abo-Kontingent, Rechenzeit und Speicher: ohne Deckel würde ein Schwung ToDos je einen Claude-CLI zugleich starten. Läufe auf **verschiedene** Repos laufen nebenläufig; zwei auf **dasselbe** Repo nie gleichzeitig; ein **automatischer** Lauf (alle Repos) läuft **exklusiv** (startet erst wenn nichts mehr läuft, blockiert Starts währenddessen). Konservativ starten, bei Headroom erhöhen. |
| `DEVLAB_RUNS_MAX_DURATION` | `4h` | Obergrenze Wall-Clock **pro Lauf-Versuch**. Reststrecke wird auf den nächsten Termin übertragen (nicht neu begonnen). **`0` = AUS (unbegrenzt)** — nicht „keine Läufe"; Läufe stoppt man mit `MODE=off`. |
| `DEVLAB_RUNS_MAX_COST_USD` | `0` (aus) | Kosten­deckel **in Summe über alle gleichzeitig laufenden Läufe** einer Welle (nicht je Lauf, nicht kumulativ über Wellen). Weicher Deckel: der gerade laufende Repo überschreitet ggf. um seine eigenen Kosten. Reststrecke wird übertragen. Nur wirksam, wenn der Claude-CLI `total_cost_usd` liefert (bei Abo-Auth ggf. `0` → dann wirkungslos; einmal prüfen). |
| `DEVLAB_RUNS_LIMIT_BACKOFF` | `15m` | Wartezeit nach Abo-Limit, wenn die CLI keinen Reset-Zeitpunkt nennt. Empfehlung `5h` (einmal aufs Fenster warten statt blind pollen). |
| `DEVLAB_RUNS_LIMIT_MAXRESUMES` | `24` | Nach so vielen Abo-Limit-Fortsetzungen aufgeben. Empfehlung `2`. |
| `DEVLAB_RUNS_SELF_REPO` | `devlab` | Repo, das im `full`-Modus **nicht** aus seinem eigenen Lauf deployt wird (Neustart würde den Executor killen). Groß/klein egal. |
| `DEVLAB_RUNS_DEV_BRANCH` | `mercury-dev` | Name des **persistenten dev-Integrationsbranches** je Repo, den der Runner wachsen lässt und den der dev-Deploy ausliefert. Nie der Standard-Branch (aus dem prod bei Merge beliefert wird). |
| `DEVLAB_RUNS_DRAIN_GRACE` | `30s` | Beim Neustart (`systemctl restart` → SIGTERM) wartet devlabd so lange auf das Auslaufen des aktiven Laufs, bevor es diesen für den Carry-over abbricht. Neue Läufe werden ab der Neustart-Anforderung **gar nicht mehr** gestartet (sie werden eingereiht), unabhängig von diesem Wert. Sollte innerhalb von `TimeoutStopSec` (siehe `devlabd.service`, `120s`) liegen. |

### Wachsender dev-Stand statt Zusammensetzen

Ein Lauf setzt nicht mehr hart auf den Standard-Branch zurück, sondern auf den persistenten `mercury-dev`
je Repo — der **wächst**: der Standard-Branch wird eingefaltet (nicht der Stand auf ihn zurückgesetzt),
und die Arbeit landet obendrauf. So ist die Vorarbeit früherer Läufe immer vorhanden, ohne dass jemand
offene PRs einsammeln muss. Der **dev-Deploy liefert genau `mercury-dev`** aus (in der Ausführungsansicht
als `mercury-dev@<sha>` benannt); **prod** wird weiterhin ausschließlich aus dem gemergten Standard-Branch
beliefert. Jede Lieferung bekommt einen **gestapelten PR** (Basis = vorherige offene Lieferung, sonst
Standard-Branch), zeigt also nur ihre eigenen Änderungen. Zwei bewusste Handlungen (nur `pr`/`full`):
- `POST /api/mercury/runs/deliveries/{id}/rollback` — **Gegenbuchung** einer Lieferung (umkehrender
  Commit, keine Historie umgeschrieben; offener PR wird geschlossen, gemergter bekommt einen umkehrenden
  PR; baut spätere Arbeit darauf auf, wird automatisch ein ToDo erzeugt statt geraten).
- `POST /api/mercury/runs/reset` `{"repo":"owner/name"}` — **ausdrückliches Zurücksetzen** von
  `mercury-dev` auf den Standard-Branch (verwirft den akkumulierten dev-Stand, force-push).
| `DEVLAB_MERCURY_RUNS_ACTIVE_MARKER` | `<mercury>/runs-active` | Pfad der Datei, die devlabd schreibt, **solange Läufe aktiv sind** (Inhalt = Anzahl). Ein Deploy des `devlab`-Repos verschiebt seinen Neustart, solange sie existiert. Leer = Marker aus. |
| `DEVLAB_DEPLOY_RUNS_WAIT` | `1800` | (Deploy-Skript) Sekunden, die ein `devlab`-Deploy auf das Ende laufender Läufe wartet, bevor es trotzdem neustartet. `0` = nicht warten. |

> **Nebenläufigkeit + aufgeschobener Neustart.** Mehrere Läufe laufen gleichzeitig (Deckel
> `DEVLAB_RUNS_MAX_CONCURRENCY`); der Kostendeckel `DEVLAB_RUNS_MAX_COST_USD` gilt **in Summe** über die
> Welle, nicht je Lauf. Solange **irgendein** Lauf arbeitet, hält der Auto-Merge-Pfad einen Self-Deploy
> zurück und das `devlab`-Deploy-Skript verschiebt den `systemctl restart devlabd` (der Marker **zählt**
> die aktiven Läufe und wird erst freigegeben, wenn der letzte endet). Für die erste scharfe Nacht:
> klein anfangen (wenige Repos / ein ToDo, `MAX_CONCURRENCY=1`), Verbrauch beobachten, dann skalieren.

**Empfehlung:** erst `report` gegen einen echten Lauf testen (erzeugt nur Berichte), dann `pr`
(Branch + PR, nichts wird gemergt/deployt bis du prüfst), erst dann `full`.

## Aktueller Betriebszustand

Seit **2026-07-20** läuft der Runner scharf im Modus **`pr`** — er analysiert, implementiert,
pusht einen Branch und öffnet einen Pull Request. Gemergt und deployt wird nichts ohne Prüfung.

| Stellschraube | Wert | Begründung |
|---|---|---|
| `DEVLAB_RUNS_MODE` | `pr` | implementiert und öffnet PRs; kein unbeaufsichtigtes Deploy |
| `DEVLAB_RUNS_MAX_COST_USD` | `50` | ein ungedeckelter Sweep über ~19 Repos wurde auf $150–300 **pro Nacht** geschätzt; die Reststrecke wird per Carry-over fortgesetzt, nicht verworfen |
| `DEVLAB_RUNS_MAX_DURATION` | `4h` | Start 02:00 → Ende spätestens 06:00 |
| `DEVLAB_RUNS_AUTOMERGE` | `720h` | 30 Tage Prüffrist, explizit gesetzt statt implizit aus dem Code |

> Der Kostendeckel wirkt nur, wenn der Claude-CLI `total_cost_usd` liefert — bei Abo-Auth kann das
> `0` sein und der Deckel liefe leer. **Geprüft am 2026-07-20:** ein realer Lauf meldete `$0.8953`,
> die Kostenmeldung funktioniert und der Deckel greift.

### Voraussetzungen für `full` (noch offen)

`full` ist **nicht** einsatzbereit — der Deploy-Schritt würde für jedes Repo scheitern, weil beides
fehlt. Vor dem Hochstufen zu erledigen:

1. `sudo install -o root -g root -m 0755 deploy/devlab-deploy /usr/local/sbin/devlab-deploy`
2. Je Repo ein geprüftes Deploy-Skript nach `/etc/devlab/deploy.d/<repo>` (Vorlage:
   `deploy/deploy.d.example-devlab`). Ohne Eintrag überspringt der Wrapper das Repo (Exit 3).

## Kill-Switch / Rückbau
- `DEVLAB_RUNS_MODE=off` (oder Drop-in entfernen) + `systemctl restart devlabd` → Scheduler aus.
- Laufender Lauf: „Abbrechen" im UI (`POST /api/mercury/runs/cancel`).
- Auto-Merge stoppen: PRs in `runs-prs.json` sind nachvollziehbar; Datei leeren stoppt Auto-Merges.
