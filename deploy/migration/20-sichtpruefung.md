# Visual inspection and measurement checklist

Every acceptance line that cannot be decided by a grep or a test is listed here, with what to do
and what must be true. The automated half runs as `tools/abnahme.sh --tests`; this file is the
other half.

Run it against the freshly deployed instance, in this order — the later sections need a real
execution to have happened.

Legend: **[S]** look at it · **[M]** measure it (logs, counters, request lists) · **[E]** click
through it end to end. The bracketed id is the acceptance line the item belongs to.

---

## Status per acceptance line

The wording of every line lives in `spec/ABNAHME.md` — it is not repeated here. What this table adds
is the STATE of each line, in exactly four kinds:

- **Grep green** — the audit check(s) of `tools/abnahme.sh` that carry this line pass. The check ids
  are named, so a failure points at the exact check. Those ids are not decoration: check `doc-a` of
  the same script asserts that every id named here EXISTS, so this table cannot cite a check nobody
  runs.
- **Test green** — the line's automated test(s) pass (`go test ./...` including `backend/it`,
  `node --test`). "via the referenced line" means the matrix itself points at another line's tests.
- **E2E open** — the matrix names an END-TO-END walk-through as this line's own kind of proof: a
  person clicking the whole path at the running instance. Nothing in this repository can discharge
  it — no grep, no unit or integration test — so it stays open until it has been walked, and a line
  that names E2E is never reported as finished on the strength of its unit tests alone.
- **Visual inspection open** — the line additionally demands one of the three kinds the matrix
  spells for it: a LOOK (`Sichtprüfung`) or a MEASUREMENT (`Messung`) at the running instance, or a
  REVIEW (`Review`) of the artefact — which is a reading of code, permissions or a module cut and
  needs no running service at all. The working list below says which of the three each line asks
  for, because the three are not interchangeable: a review can be done before the daemon is up, a
  measurement cannot be done at all without it. Those are the items of THIS document; a line marked
  "not itemised" is covered by a section here without naming the id, so read the section it belongs
  to.

A line with several kinds needs all of them.

Fifteen lines name an end-to-end walk-through in the matrix: REQ-007, REQ-012, REQ-035, REQ-036,
REQ-037, B-25, B-27, B-28, B-31, B-32, B-33, B-34, B-37, B-39, B-40. Each of them carries **E2E open**
below. Four of them once read "Test green" and nothing else — an acceptance claim wider than the
evidence, which is the one thing this document may never make. B-27, B-40 and REQ-035 were corrected
in `c98d4a9`; REQ-037 in `89ee3da`, where the kind was still misread: its cell asks for a
"Klick-Durchgang" over every history entry (including the dead and the failed ones), which is a person
clicking the running instance — an E2E proof under a different word, not a unit test. A commit is
named here rather than a wave, for the same reason the measurement row carries one: a wave is not a
state of this repository, and a claim about a correction has to be re-checkable
(`git show <commit> -- deploy/migration/20-sichtpruefung.md`).

### How the automated half is established

The procedure, not a snapshot: a count ages with the next commit, so what stands here are the
commands and the rule each must satisfy. Run them in the repository root.

| # | Command | The rule |
|---|---|---|
| 1 | `bash tools/abnahme.sh --tests` | the last line reads `N passed, 0 failed`. N is however many checks the matrix currently carries — the count is not the criterion, the ZERO is |
| 2 | `gofmt -l backend` | prints nothing |
| 3 | `cd backend && go build ./... && go vet ./...` | both silent |
| 4 | `cd backend && go test ./... -count=1`, once more with `-race` | every package `ok` |
| 5 | `npx tsc -p tsconfig.json --noEmit` | prints nothing |
| 6 | `npm test` | `fail 0`, and `pass` equals `tests` |
| 7 | `npm run build` | exits 0 |

Record the reading when an inspection is performed, and record it AT A COMMIT: the first column holds
`git rev-parse HEAD` (short form is enough), never a wave name. A wave is not a state of this
repository — two rows of the same wave can disagree — while a commit is re-checkable by anyone who
reads the row later. A row is a MEASUREMENT of that commit and never a promise about the working tree:
if tree and row disagree, the tree is right and the row is stale.

| Commit (`git rev-parse --short HEAD`) | 1 acceptance | 4 go test | 6 node --test | 2 · 3 · 5 · 7 |
|---|---|---|---|---|
| c98d4a9 | 67 passed, 0 failed | whole suite ok (`go test ./... -count=1`) | 183 tests, 0 fail | 2 · 3 · 5 silent; 7 (`npm run build`) NOT run |

The row states what was actually run, and nothing further: an unrun command is recorded as unrun,
never as green. While several agents work in ONE tree a whole-tree run also reads their unfinished
edits rather than the delivered state, so the closing row belongs to whoever closes the wave — and it
carries the commit that wave produced, not the commit it started from.

### The operator's working list — the 65 lines that no grep and no test can close

Everything above is either green or one of these 65 rows. They are listed here as ONE list, by kind,
so the operator can plan: **54 of them need the running instance**, the remaining **11 are reviews of
the artefact** and can be done before the daemon comes up. A line appears under every kind it asks
for, so the four counts add up to more than 65 while the union is exactly 65.

Recompute the list and the total from this document at any time — it ages with the table above, so
the commands are the criterion and the numbers below are a reading:

```sh
grep -cE '^\| (K|REQ|B)-[0-9]+ \|.*(E2E open|Visual inspection open)' deploy/migration/20-sichtpruefung.md   # 65
grep -cE '^\| (K|REQ|B)-[0-9]+ \|.*E2E open' deploy/migration/20-sichtpruefung.md                            # 15
```

| Kind | What it needs | Lines |
|---|---|---|
| **E2E** (15) | a person clicking the whole path at the running instance | REQ-007 · REQ-012 · REQ-035 · REQ-036 · REQ-037 · B-25 · B-27 · B-28 · B-31 · B-32 · B-33 · B-34 · B-37 · B-39 · B-40 |
| **Measurement** (8) | logs, counters, request lists — at the running instance, over time | K-2 · K-5 · K-6 · REQ-017 · REQ-018 · REQ-034 · REQ-044 · B-36 |
| **Look** (37) | the surface in front of you at the running instance | K-4 · K-5 · REQ-002 · REQ-004 · REQ-005 · REQ-009 · REQ-010 · REQ-015 · REQ-016 · REQ-020 · REQ-021 · REQ-022 · REQ-024 · REQ-029 · REQ-030 · REQ-031 · REQ-033 · REQ-034 · REQ-036 · REQ-038 · REQ-040 · REQ-041 · REQ-042 · REQ-043 · REQ-044 · B-01 · B-03 · B-18 · B-24 · B-28 · B-29 · B-30 · B-36 · B-38 · B-42 · B-43 · B-45 |
| **Review** (22) | reading the artefact — code, permissions, module cut, invariant checklist. **No running service.** Eleven lines need nothing else: REQ-023 · REQ-026 · REQ-028 · B-06 · B-14 · B-15 · B-17 · B-20 · B-23 · B-41 · B-44 | REQ-022 · REQ-023 · REQ-026 · REQ-028 · REQ-040 · REQ-041 · REQ-043 · B-03 · B-06 · B-14 · B-15 · B-17 · B-20 · B-23 · B-24 · B-28 · B-31 · B-32 · B-37 · B-39 · B-41 · B-44 |

The kinds are not this document's invention: each is the word the acceptance matrix uses in the
line's own "Wie geprüft wird" cell (`Sichtprüfung`, `Messung`, `Review`, `E2E`/`Klick-Durchgang`).
Where this document and the matrix disagree, the matrix decides.

### §1 — the six construction faults (K-1 … K-6)

| Line | Status |
|---|---|
| K-1 | Grep green (K-1a, K-1b, K-1c) · Test green |
| K-2 | Grep green (K-2, K-2b) · Test green · Visual inspection open (measurement) |
| K-3 | Test green |
| K-4 | Test green · Visual inspection open (visual) |
| K-5 | Test green · Visual inspection open (visual/measurement) |
| K-6 | Grep green (K-6a, K-6b, K-6c) · Test green · Visual inspection open (measurement) |

### §2 — requirements (REQ-001 … REQ-044)

| Line | Status |
|---|---|
| REQ-001 | Grep green (REQ-001a, REQ-001b) · Test green |
| REQ-002 | Grep green (REQ-002) · Test green · Visual inspection open (visual) |
| REQ-003 | Grep green (REQ-003a, REQ-003b) · Test green |
| REQ-004 | Grep green (REQ-004) · Test green · Visual inspection open (visual) |
| REQ-005 | Test green · Visual inspection open (visual) |
| REQ-006 | Test green |
| REQ-007 | Test green · E2E open (paste at every upload point) |
| REQ-008 | Test green |
| REQ-009 | Test green · Visual inspection open (visual) |
| REQ-010 | Grep green (REQ-010) · Test green · Visual inspection open (visual) |
| REQ-011 | Test green |
| REQ-012 | Grep green (REQ-012a, REQ-012b, REQ-012c, REQ-012d) · Test green · E2E open (both directions, and the shared detail) |
| REQ-013 | Grep green (REQ-013) · Test green |
| REQ-014 | Test green |
| REQ-015 | Grep green (REQ-015a, REQ-015b) · Test green · Visual inspection open (visual) |
| REQ-016 | Test green · Visual inspection open (visual) |
| REQ-017 | Grep green (REQ-017) · Visual inspection open (measurement) |
| REQ-018 | Test green · Visual inspection open (measurement) |
| REQ-019 | Test green |
| REQ-020 | Test green · Visual inspection open (visual) |
| REQ-021 | Test green · Visual inspection open (visual) |
| REQ-022 | Grep green (REQ-022a, REQ-022b) · Test green · Visual inspection open (visual/review) |
| REQ-023 | Test green (via the referenced line) · Visual inspection open (review, not itemised) |
| REQ-024 | Test green · Visual inspection open (visual) |
| REQ-025 | Test green |
| REQ-026 | Test green · Visual inspection open (review, not itemised) |
| REQ-027 | Grep green (REQ-027a, REQ-027b, REQ-027c) · Test green |
| REQ-028 | Grep green (REQ-028) · Test green · Visual inspection open (review, not itemised) |
| REQ-029 | Test green · Visual inspection open (visual) |
| REQ-030 | Test green · Visual inspection open (visual) |
| REQ-031 | Test green · Visual inspection open (visual) |
| REQ-032 | Test green |
| REQ-033 | Test green · Visual inspection open (visual) |
| REQ-034 | Grep green (REQ-034a, REQ-034b) · Test green · Visual inspection open (visual/measurement) |
| REQ-035 | Test green (the structural half: `src/reload.test.ts`) · E2E open (reload in every main view) |
| REQ-036 | Test green · Visual inspection open (visual) · E2E open (live transcript during a real run) |
| REQ-037 | Test green · E2E open (click every history entry, the dead and the failed ones included) |
| REQ-038 | Visual inspection open (visual) |
| REQ-039 | Grep green (REQ-039a, REQ-039b) · Test green |
| REQ-040 | Grep green (REQ-040a, REQ-040b, REQ-040c, REQ-040d, REQ-040e, REQ-040f, REQ-040g) · Visual inspection open (visual/review) |
| REQ-041 | Test green · Visual inspection open (visual/review) |
| REQ-042 | Grep green (REQ-042) · Test green · Visual inspection open (visual) |
| REQ-043 | Test green · Visual inspection open (visual/review) |
| REQ-044 | Grep green (REQ-044) · Test green · Visual inspection open (visual/measurement) |

### §3 — inventory of the old system (B-01 … B-45)

| Line | Status |
|---|---|
| B-01 | Test green · Visual inspection open (visual) |
| B-02 | Grep green (B-02) · Test green |
| B-03 | Grep green (B-03) · Test green · Visual inspection open (visual/review) |
| B-04 | Test green |
| B-05 | Test green |
| B-06 | Test green · Visual inspection open (review, not itemised) |
| B-07 | Test green (via the referenced line) |
| B-08 | Test green |
| B-09 | Test green |
| B-10 | Test green |
| B-11 | Grep green (B-11) · Test green |
| B-12 | Test green |
| B-13 | Grep green (B-13) · Test green |
| B-14 | Test green · Visual inspection open (review, not itemised) |
| B-15 | Test green · Visual inspection open (review, not itemised) |
| B-16 | Grep green (B-16) · Test green |
| B-17 | Visual inspection open (review, not itemised) |
| B-18 | Grep green (B-18) · Visual inspection open (visual) |
| B-19 | Test green (via the referenced line) |
| B-20 | Grep green (B-20) · Test green · Visual inspection open (review, not itemised) |
| B-21 | Grep green (B-21) · Test green |
| B-22 | Grep green (B-22) · Test green |
| B-23 | Test green · Visual inspection open (review) |
| B-24 | Visual inspection open (visual/review) |
| B-25 | Test green · E2E open (the boot gates, login → link → app) |
| B-26 | Test green (via the referenced line) |
| B-27 | Test green · E2E open (view change and back: drafts, terminal, AI session) |
| B-28 | Test green · Visual inspection open (visual/review) · E2E open (the gates) |
| B-29 | Test green · Visual inspection open (visual) |
| B-30 | Visual inspection open (visual) |
| B-31 | Test green · Visual inspection open (review) · E2E open (every tree operation) |
| B-32 | Test green · Visual inspection open (review) · E2E open (per capability) |
| B-33 | Test green · E2E open (per capability) |
| B-34 | Test green · E2E open (both surfaces use the one kit; live follow during a real run) |
| B-35 | Grep green (B-35, B-35b) |
| B-36 | Visual inspection open (visual/measurement) |
| B-37 | Test green · Visual inspection open (review) · E2E open (proposal → application → visible change) |
| B-38 | Visual inspection open (visual, not itemised) |
| B-39 | Test green · Visual inspection open (review) · E2E open (edit, save, diff, vision, comments) |
| B-40 | Test green · E2E open (per panel) |
| B-41 | Grep green (B-41a, B-41b) · Visual inspection open (review, not itemised) |
| B-42 | Visual inspection open (visual) |
| B-43 | Grep green (B-43) · Test green · Visual inspection open (visual) |
| B-44 | Grep green (B-44) · Test green (via the referenced line) · Visual inspection open (review) |
| B-45 | Grep green (B-45, B-45b, B-45c) · Test green · Visual inspection open (visual) |

## 0. Before the first look

- [ ] `tools/abnahme.sh --tests` is green, or every open line is known and accepted.
- [ ] The daemon is up; `GET /api/health` answers `{"ok":true,…}` and nothing else.
- [ ] The browser is a fresh profile (no stale storage), and a second browser or private window is
      available — several items need two sessions at once.

## 1. Boot, gates and shell

- [ ] **[E] B-25 · B-28** Boot as a user with no session: the login gate appears, sign-in leads to
      the GitHub-link gate, linking leads into the application. No step can be skipped by editing
      the address.
- [ ] **[S] B-29** A user without a right sees no card for that service: capability cards of
      services they may not use are absent, not disabled.
- [ ] **[S] B-28** Every shell element is present and usable: top bar, icon rail, resizable panel
      column, status bar, brand as the way back, repo and branch pickers, settings, help, theme
      toggle (dark is the default).
- [ ] **[S] B-24** Stop the backend and reload: the surface shows DEFINED empty states — never a
      spinner that never ends and never invented data.
- [ ] **[S] B-03** The built web surface is served by the daemon itself (not only by a dev server).
- [ ] **[S] B-18** The preview reference opens the running dev service; there is no button that
      leads nowhere.

## 2. The IDE (S1–S4)

- [ ] **[E] B-39** Open a file, edit it, save with Cmd/Ctrl+S, look at the diff, open the vision
      catalogue, write a comment, delete it again.
- [ ] **[S] B-39** The structure view shows only states it can attest. Nothing claims a stage that
      was never observed.
- [ ] **[E] B-40** Each panel does its job: project tree with search, git panel (push, pull,
      branch, checkout, open pull request), version control (stage, unstage, commit), AI panel in
      both modes, terminal, vision upload, comments.
- [ ] **[S] REQ-043.2** The AI symbol sits on the right-hand side.
- [ ] **[S] Cross-cutting 11** Right-click menus exist where they are useful and carry no
      pointless entries; list elements answer the common keyboard shortcuts (including Cmd/Ctrl
      combinations); file collections accept drag and drop.
- [ ] **[E] REQ-007.2** Paste from the clipboard at EVERY upload point (vision panel, task media)
      and confirm it produces the same result as the file dialog.
- [ ] **[E] B-27** Switch to another view and back: editor drafts, the terminal session and the AI
      session are unchanged.
- [ ] **[S] S3** Reload the browser with the terminal open: either the session is reattached, or a
      visibly NEW session is announced. A silent loss is a defect.
- [ ] **[S] Model labelling** Every AI answer names the model that produced it.

## 3. The constitution (S5)

- [ ] **[E] B-31** Drag and drop an axiom onto another category, reorder within a category, move a
      whole category, add, edit, optimise and conform an axiom.
- [ ] **[S] REQ-003** No badge and no button offers to "recompose" anything — there is no state in
      which a prompt is stale.
- [ ] **[S] REQ-004** After adding an axiom a notification appears saying which run took it, and
      the assignment can be adjusted afterwards. An axiom nobody took shows up as uncovered.
- [ ] **[S] REQ-002.5** The constitution reference in a repository's `CLAUDE.md` arrives as part of
      a regular delivery. Where it is the only change, that delivery is complete on its own.
- [ ] **[E] B-37** Ask the Mercury assistant for a change, apply its suggestion, and see the change
      in the tree.

## 4. Tasks and runs (S6)

- [ ] **[S] REQ-005** Automatic runs and todos have their own view and their own history; nothing
      from one appears in the other.
- [ ] **[S] REQ-009.4** The picker shows model AND version, and both kinds use the same component.
- [ ] **[S] REQ-010.3** The execution view names the time budget in force, and says when it is the
      default rather than an own choice.
- [ ] **[E] REQ-006** Create a todo with several targets, including a repository that does not
      exist yet. The picker offers every repository of the instance.
- [ ] **[E] REQ-008** "Run now" is preselected and starts at once; a date in the past cannot be
      chosen; the preselected future date is sensible.
- [ ] **[E] B-32 · B-33** Schedule editor, AI fill, AI fine-tuning, coverage view, notices, history
      restore, target picker, attachments, due date — each works from the surface.
- [ ] **[S] REQ-041** One and the same person component is used everywhere, and an autonomous run
      is labelled as autonomous — never attributed to a person.

## 5. Execution and delivery (S13, S10)

- [ ] **[S] REQ-036** The active executions are a LIST with repository, current step and
      consumption.
- [ ] **[E] REQ-036** Open a running execution during a REAL run: the live transcript arrives
      continuously, in the same surface — no separate tool.
- [ ] **[M] REQ-017** Token counters and the monetary equivalent climb while the agent works.
- [ ] **[S] REQ-017** There is no cost cap anywhere and no "remaining budget" display.
- [ ] **[S] K-4 · REQ-030.6** The four step states are distinguishable WITHOUT reading the label
      (colour and form). Nothing that was skipped is green.
- [ ] **[S] REQ-030.7** The history shows the full step list of an execution, including the prompt
      it ran with.
- [ ] **[E] REQ-037.4** Click through EVERY history entry, including failed ones, dead ones and
      imported legacy ones: each renders a defined state. A blank panel is a defect.
- [ ] **[S] REQ-037.2** A hanging or failed stage can be triggered again from the surface.
- [ ] **[S] REQ-038** Reports are rendered Markdown, not plain text.
- [ ] **[S] REQ-021.4** Sample three completion reports: three parts (done / not done with reason /
      conclusions), and none of them ends in a question to the user.
- [ ] **[S] REQ-020.4** A task that is implemented but not delivered is named as exactly that and
      offers delivery in one action.
- [ ] **[S] REQ-022.3** The delivered state is named (the commit) in the execution view.
- [ ] **[S] REQ-024** The delivery view is complete: repository, commit span, pull request, stage,
      time. Rolling back asks first.
- [ ] **[S] REQ-040.6** Every dangerous action explains its effect, the state that follows, and the
      way back — before it is performed.
- [ ] **[S] REQ-040.7** Every view is understandable without explanatory text: no tooltip and no
      help paragraph is needed to know what is in front of you.
- [ ] **[S] K-5 · REQ-016.3** A blocked delivery is recognisable, names reason, time and number of
      attempts, and can be resumed. A pause says WHY, not just "paused".
- [ ] **[S] REQ-015.5** The slot overview shows everything at a glance: occupied and free slots,
      deferred executions with their continuation point, an overload.
- [ ] **[S] REQ-034.7** A pending restart and the starts queued behind it are visible.

## 6. The calendar (REQ-012)

- [ ] **[S] REQ-012** Past AND coming runs are visible in both calendars.
- [ ] **[E] REQ-012** Clicking a past occurrence opens the SAME detail view the history opens.
- [ ] **[M] B-36** Watch the network panel with the calendar open and idle: no periodic request.

## 7. Live updates (REQ-034)

- [ ] **[E] REQ-034.1** Change something in a second browser session (or with the MCP tools): the
      first session shows it without a reload, and new entries appear in the lists at once.
- [ ] **[S] REQ-034.2** While typing in a form, an external change to the SAME element is named as
      a conflict; the typed text is never discarded.
- [ ] **[S] REQ-034.3** Nothing jumps and no scroll position is lost when an update arrives.
- [ ] **[M] REQ-034.4** With every view idle, the network panel shows NO periodic requests — one
      open stream and nothing else.
- [ ] **[E] REQ-034.6** Cut the connection (offline for a moment, then online): the stream heals
      itself, including after the session token has been re-minted.

## 8. Reporting and notices (S14)

- [ ] **[S] REQ-042** The daily report is structured, summarised, carries links into the surface,
      and holds the rubrics: delivery alarms, admin overrides, protection deviations.
- [ ] **[S] REQ-042** Exactly one mail per day and recipient arrived, and none on a day without an
      execution.
- [ ] **[S] REQ-031.2** A notice about undelivered work stays until it is read, and names
      repository, reason and next step.
- [ ] **[S] REQ-029.4** Delivery gaps announce themselves — the template repository is exempt.
- [ ] **[S] REQ-033.3** An admin override appears in the report as an incident: who, when, which
      pull request, why.

## 9. Landscape and ports (REQ-044, B-30)

- [ ] **[S] B-30** The landscape graph renders, and the port allocation is shown with it.
- [ ] **[M] REQ-044.5** The inventory finds the known double allocation of the old instance.
- [ ] **[S] REQ-044.4** The allocation view is part of the central configuration.
- [ ] **[E] REQ-044.2** Set up a service on an occupied port: it is refused, the occupant is named,
      and a free port is proposed.

## 10. Operation, deploy and restart (S11, K-2)

- [ ] **[S] B-01 · B-45** The unit template and the drop-in pattern are in the repository, every
      instance value is configuration, and each changed variable carries a migration note.
- [ ] **[S] B-43** The wrapper and sudoers templates are in the repository, and the doctrine
      comment ("the runner user has no sudo at all") is intact.
- [ ] **[S] B-44** The invariants of the delivery chain hold: root never builds, install only,
      canonicalised paths, the production target is server-side.
- [ ] **[M] K-2** Deploy the self repository DURING an execution: the execution runs to its end,
      the restart happens afterwards, and the handover unit is OUTSIDE the daemon's control group
      (`systemctl status` / `systemd-cgls` show it as its own transient unit).
- [ ] **[M] K-2** Send SIGTERM during an execution: the process is gone within the stop budget, the
      execution persists as interrupted, and it continues after the start.
- [ ] **[M] REQ-018.5** The drain-out limit is logged when it is reached.
- [ ] **[M] K-5** Over a simulated hour, the log holds ONE bundled message with count and period —
      not a repetition every twenty seconds.
- [ ] **[M] K-6** No scenario produced two identical open pull requests in one repository.
- [ ] **[E] REQ-029.2** Set up delivery for a service that had none: the waiting delivery starts by
      itself, without anyone triggering it.

## 11. Reload and state preservation (REQ-035)

- [ ] **[E] REQ-035** Reload in EVERY main view (dashboard, IDE, Mercury with each section, Atlas):
      the same tab, the same view, the same session comes back.
- [ ] **[E] REQ-035** Start a run, then reload: the run does not "disappear" — the same execution
      is still open in front of you.
- [ ] **[M] B-23** Let the access token expire (or delete the `h_access` cookie by hand) with
      several views open, then act: the surface re-mints the token ONCE — the network panel shows a
      single `POST /api/auth/refresh`, not one per request — and every request is retried without
      the user being thrown back to the login gate or losing an unsaved editor draft. The structural
      half of this criterion is audited by `src/reload.test.ts`; only the burst behaviour needs a
      browser.

## 12. Consistency of the kit (B-42)

- [ ] **[S] B-42** The kit is consistent: one button style, one dropdown, one modal, one toast, one
      breadcrumb, ONE person component, ONE badge tone map.
- [ ] **[S] B-42** Switch the theme: light and dark both hold, with no unreadable element.

---

## Result

| Section | Items | Passed | Open |
|---|---|---|---|
| 0 Before the first look | 3 | | |
| 1 Boot, gates and shell | 6 | | |
| 2 The IDE | 9 | | |
| 3 The constitution | 5 | | |
| 4 Tasks and runs | 7 | | |
| 5 Execution and delivery | 18 | | |
| 6 The calendar | 3 | | |
| 7 Live updates | 5 | | |
| 8 Reporting and notices | 5 | | |
| 9 Landscape and ports | 4 | | |
| 10 Operation, deploy and restart | 9 | | |
| 11 Reload and state preservation | 3 | | |
| 12 Consistency of the kit | 2 | | |

An item that cannot be checked because the surface is missing counts as OPEN, never as passed —
the rule the acceptance matrix applies to skipped steps applies to its own inspection too.
