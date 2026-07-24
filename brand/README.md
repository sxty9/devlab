# DevLab brand assets

`devlab-logo-{512,256,128}.png` — the DevLab mark: the code-brackets glyph (the
in-app `CodeIcon`) in Apple-blue accent (`rgb(10 132 255)`) on a dark rounded
tile, matching the in-app `Brand` lockup. Full-bleed square, so a host's own
rounding (e.g. GitHub's) stays clean.

Use the 512px PNG for the GitHub OAuth app logo (GitHub downscales it).

Regenerate with `node generate-logo.mjs` (uses the repo's Playwright/Chromium).
