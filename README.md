# DevLab

**Developer collaboration & in-browser IDE for the Holistic ecosystem** — develop, maintain,
and ship (preview → prod) Holistic services from one place.

DevLab is an IntelliJ/VS-Code-style web workspace: pick a repo/service from the top bar, then
work in a left **icon rail** (Project · Version Control · Claude · Terminal), a resizable
**panel column**, and a **Monaco editor** with tabs. A bottom **pipeline bar** tracks the path
from _Vision Deposit → Code → Preview (sxgate) → Delivery → main merge_.

## Status

**Phase 1 — UI shell (mock data, no backend).** Design-first: built, preview-deployed via
sxgate, and refined before the backend is wired.

## Stack

- React 18 + TypeScript (strict) + Vite 5
- Tailwind CSS 3 with the **vendored Holistic Apple-dark design tokens** (`src/theme`) — visually
  identical to `@holistic/ui`, but self-contained so the repo builds and previews anywhere.
- Monaco editor (`@monaco-editor/react`) for the code surface.

## Develop

```bash
npm install
npm run dev        # http://localhost:5173
npm run build      # → dist/
```

## Preview deploy (sxgate)

```bash
npm run build                                   # sanity-check dist/
sudo /home/nanu/sxgate/sxgate preview up main   # → https://main-devlab.henrysoase.org
sudo /home/nanu/sxgate/sxgate preview rebuild main
sudo /home/nanu/sxgate/sxgate preview down main
```

Manifest: [`.sxgate/preview.conf`](.sxgate/preview.conf) (`MODE="static"`).

## Layout

```
src/
  theme/        vendored Apple-dark tokens + Tailwind preset + Inter fonts
  lib/          cn() class helper
  types.ts      shared domain types (Repo, Branch, FileNode, Tab, Stage, …)
  mock/         realistic mock workspace data (phase 1)
  state/        WorkspaceProvider context (active repo/branch/panel/tabs)
  ui/           primitives (icons, Tooltip, Dropdown, Button)
  shell/        TopBar, repo/branch dropdowns, IconRail, PanelHost, StatusBar
  panels/       Project tree, Version Control, Claude, Terminal
  main/         TabStrip, Monaco EditorView, StructureView, Welcome
  components/    Splitter (resizable column divider)
```
