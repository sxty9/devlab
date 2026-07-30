# DevLab

**Developer collaboration, in-browser IDE and autonomous delivery for the Holistic services
landscape (HSL)** — read, edit, review and ship Holistic services from one place.

DevLab is one Go daemon (`devlabd`) serving the built SPA and the API from a single loopback port,
plus the React/TypeScript surface it serves. Two halves:

- **IDE** — pick a repository in the top bar and work in the icon rail (Project · Version control ·
  AI · Terminal), a resizable panel column and a Monaco editor with tabs. The write loop
  (stage · commit · push · branch · pull request) runs against the caller's own per-user working tree.
- **Mercury** — the constitution (axioms and implementation rules), the todos and recurring runs
  built from them, and the autonomous delivery chain that implements them: one typed stage list per
  target repository, honest per-stage outcomes, a live transcript with live consumption, a delivery
  ledger with counter-booking, and a daily report.

## Status

Rebuilt from the specification in `spec/`; the cutover is `deploy/MIGRATION.md`. Every line of the
acceptance matrix is carried either by a test or by an audit check — `tools/abnahme.sh`.

## Stack

- Go 1.26 backend (`backend/`); dependencies: `golang-jwt/jwt/v5` and `gorilla/websocket`.
- React 18 + TypeScript (strict) + Vite 5.
- Tailwind CSS 3 over the **vendored Holistic Apple-dark design tokens** (`src/theme`) — visually
  identical to the shared UI package, but self-contained so the repo builds anywhere.
- Monaco editor (`@monaco-editor/react`) for the code surface, xterm.js for the terminal.

## Develop

```bash
npm install
npm run dev                       # SPA on the Vite dev server
npm run build                     # → dist/
node --test                       # frontend unit tests
(cd backend && go test ./...)     # backend units + the integration suite (backend/it)
bash tools/abnahme.sh             # the executable half of the acceptance matrix
```

`VITE_DATA_SOURCE=stub` forces the offline data source (defined empty states, no backend); left
unset, the SPA probes the backend once and falls back to the stub when it is unreachable.

## Run

`devlabd` reads its whole contract from the environment — this repository carries no instance
literals. The service unit and its drop-in are templates under `deploy/`; the values that name a
concrete instance (state directory, port, unit name, run user, public URL) live only there.

```bash
(cd backend && go build -o ./devlabd ./cmd/devlabd)
DEVLAB_STATE_DIR=<state-root> DEVLAB_STATIC_DIR=dist DEVLAB_ADDR=127.0.0.1:<port> ./backend/devlabd
```

The boot order, the restart handover and the drain contract are documented at the top of
`backend/cmd/devlabd/main.go` (and in `spec/ARCHITEKTUR.md` §6.2).

## Layout

```
backend/
  cmd/devlabd/          the daemon: boot order, the composed execution seam, SIGTERM drain
  cmd/devlab-migrate/   one-shot data migration (refuses to run while the daemon is up)
  internal/api/         the THIN HTTP layer: the one route table, the guards, thin handlers
  internal/model/       the wire contract — the vocabulary at exactly one place
  internal/statepath/   every state-root path literal, at one place
  internal/execstate/   the ONE persisted state machine per execution
  internal/sched/       admission, slots, defer/overload, the due tick, restart, the one pause
  internal/executor/    the chain motor: the typed stage list and its five stages
  internal/workbench/   the fold-in state machine of the working state (never a reset)
  internal/deliver/     the ONE pull-request path, protection, merge + prune, rollback
  internal/deploy/      detection, build as the user, install-only as root, the honest gate
  internal/mercury/     the constitution: tree, composition, chat actions, the CLAUDE.md block
  internal/runs/        the passive pools (runs, results, deliveries, notices, settings, …)
  internal/report/      the daily report, its rubrics, and the delivery self-check
  it/                   the integration suite: the whole daemon minus main()
src/
  theme/                vendored Apple-dark tokens + Tailwind preset + Inter fonts
  types.ts              the frontend mirror of internal/model (fixture-checked)
  data/                 the ONE data seam (httpSource | stubSource) — the only place API paths live
  state/                session, live (the ONE SSE stream), route and workspace providers
  ui/ shell/ panels/    primitives, chrome, and the IDE panels
  main/                 tab strip, Monaco editor, diff, structure, vision
  views/                Dashboard, Atlas, and the Mercury surfaces
deploy/                 unit + drop-in templates, the pinned root wrappers, sudoers, migration
tools/abnahme.sh        the acceptance audit
```
