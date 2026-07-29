# Visual inspection and measurement checklist

Every acceptance line that cannot be decided by a grep or a test is listed here, with what to do
and what must be true. The automated half runs as `tools/abnahme.sh --tests`; this file is the
other half.

Run it against the freshly deployed instance, in this order — the later sections need a real
execution to have happened.

Legend: **[S]** look at it · **[M]** measure it (logs, counters, request lists) · **[E]** click
through it end to end. The bracketed id is the acceptance line the item belongs to.

---

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
| 2 The IDE | 8 | | |
| 3 The constitution | 5 | | |
| 4 Tasks and runs | 7 | | |
| 5 Execution and delivery | 16 | | |
| 6 The calendar | 3 | | |
| 7 Live updates | 5 | | |
| 8 Reporting and notices | 5 | | |
| 9 Landscape and ports | 4 | | |
| 10 Operation, deploy and restart | 8 | | |
| 11 Reload and state preservation | 3 | | |
| 12 Consistency of the kit | 2 | | |

An item that cannot be checked because the surface is missing counts as OPEN, never as passed —
the rule the acceptance matrix applies to skipped steps applies to its own inspection too.
