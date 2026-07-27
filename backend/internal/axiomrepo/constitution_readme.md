# Holistic Constitution

This repository holds the **Holistic constitution** — the axioms, implementation rules
(*Implementierungsregeln*) and run rules (*Laufregeln*) that every Holistic service and every
autonomous run is measured against.

It is **data, not code**. Nothing here is compiled or deployed; each Markdown file is one record of
the constitution.

## Where the constitution lives

- **This repository is the single source of truth.** Each record is a Markdown file whose path *is*
  its place in the taxonomy:

  ```
  axiome/<section>/<maxim>/<name>.md     an axiom
  regeln/<section>/<name>.md             an implementation rule
  laeufe/<name>.md                       a run definition
  ```

  The path is the category, the file is the record — there is no separate index.
- Every Holistic **code** repository additionally carries a generated `CLAUDE.md`. That file is a
  *rollout* of this constitution, produced by Mercury — a copy for the tools that read it in place,
  never the origin. Edit the constitution here; the `CLAUDE.md` copies follow.

## How it is edited

- **Through Mercury** (DevLab → Mercury), the constitution surface. Mercury clones this repository
  into a working copy and reads and writes it there. Every create, edit, move or delete is a single
  commit pushed straight to `main` — no pull request, no review gate. Two Mercury instances
  (development and production) may edit concurrently; a write that loses a push race is retried on the
  refreshed state, so no edit is ever lost.
- **Directly with git**, like any repository, if you prefer. Mercury re-reads `main` on its next
  refresh and picks up the change.

## Why it lives outside the protected code repositories

The Holistic **code** repositories are branch-protected: they change only through reviewed pull
requests. That is correct for code, but wrong for the constitution, which must stay **editable in one
step** so a maxim can be corrected the moment it is decided.

Keeping the constitution in its own, deliberately **unprotected** repository resolves the tension:

- code stays gated behind review;
- the constitution stays immediately editable;
- and it gains what a service's opaque object store could never give it — a **full git history**: who
  changed which sentence, when, and against what. For the one artefact everything else is measured
  against, that audit trail matters as much as the content.

---

*This README is documentation only. Mercury never lists or serves `README.md` as a constitution
record.*
