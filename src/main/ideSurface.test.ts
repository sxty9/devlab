// Surface rules of the IDE that are decided by the SOURCE, not by runtime data, and would
// otherwise only be caught by review: no asserted repo state (B 1.4), no tooltip-carried meaning
// ("intuitive by design"), and the AI on the right (REQ-043.2). Read as text, because these are
// statements about what the components may contain.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const read = (p: string) => readFileSync(new URL(p, import.meta.url), 'utf8');

test('the whole client is written in the implementation language (English)', () => {
  // Holistic is multilingual, but the implementation is English: every further language is added
  // afterwards, by the translation run. A ported German sentence is therefore a defect, and it hides
  // in exactly the places nobody visits twice — a gate, an error state, a fallback.
  const words = [
    'bitte', 'sitzung', 'anmelden', 'angemeldet', 'erneut', 'prüfen', 'berechtigung', 'erteilen',
    'verknüpfe', 'verknüpfen', 'konnte', 'werden', 'wird', 'nicht', 'deine', 'dein', 'ausschließlich',
    'ergibt', 'siehst', 'darfst', 'versuchen', 'ansicht', 'geladen', 'sichtbar', 'erreichbar', 'zugriff',
    'ungültig', 'fehlt', 'keine',
  ];
  const root = join(new URL('.', import.meta.url).pathname, '..');
  for (const path of walk(root)) {
    const text = readFileSync(path, 'utf8');
    for (const w of words) {
      assert.ok(
        !new RegExp(`\\b${w}\\b`, 'i').test(text),
        `${path.slice(root.length + 1)} carries the German word "${w}" — the implementation language is English`,
      );
    }
  }
});

/** Every implementation file of the client (tests excluded — a test names what it forbids). */
function walk(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...walk(path));
    else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name)) out.push(path);
  }
  return out;
}

test('the structure view renders only the stage rows the server attested', () => {
  const src = read('./StructureView.tsx');
  assert.match(src, /data\.stages\.map/); // the server's rows …
  // … and no invented ones: no local list of stage names, no fixed quick links.
  assert.doesNotMatch(src, /const\s+(STAGES|QUICK_LINKS|PIPELINE)\b/);
  assert.doesNotMatch(src, /'(Preview|Delivery|Vision|Code)'/);
  // An empty stage array renders nothing rather than an empty pipeline frame.
  assert.match(src, /data\.stages\.length > 0/);
  // Nothing is explained by hovering: the ground of a row is rendered next to it.
  assert.doesNotMatch(src, /\btitle=\{/);
  assert.match(src, /\{s\.hint\}/);
});

test('the welcome view offers ways in and asserts no pipeline state', () => {
  const src = read('./WelcomeView.tsx');
  assert.doesNotMatch(src, /const\s+STAGES\b/);
});

// Every surface that DESCRIBES DevLab to the user is held to the same rule: it may state what this
// build does — never a roadmap, a phase, a simulated data source or a preview deployment (the
// preview subsystem is deliberately gone, SPEC §4.4). Read as text, because the claims are decided
// by the source. One guard over ALL such surfaces: a sentence corrected in one file while it
// survives in the next is exactly what a single-file guard misses.
for (const [name, file] of [
  ['welcome view', './WelcomeView.tsx'],
  ['help modal', '../shell/HelpModal.tsx'],
] as const) {
  test(`the ${name} describes this build and promises nothing`, () => {
    const src = read(file);
    assert.doesNotMatch(src, /mock/i, file);
    assert.doesNotMatch(src, /phase \d/i, file);
    assert.doesNotMatch(src, /preview/i, file);
    assert.doesNotMatch(src, /arrives? with|coming soon|not yet (implemented|available)/i, file);
  });
}

test('the IDE offers the static way to the running service, from the ONE derivation (B-18/B-28)', () => {
  const bar = read('../shell/TopBar.tsx');
  // The access exists, it is instance-neutral (derived in lib/constants), and it opens the service
  // of the repository that is actually open — not a fixed address.
  assert.match(bar, /import \{ devServiceUrl \} from '@\/lib\/constants'/);
  assert.match(bar, /devServiceUrl\(activeRepo\.name\)/);
  // Only where the server states a service — a library has none, and no control leads into the void.
  assert.match(bar, /activeRepo\.kind !== 'service'/);
  // Exactly ONE such access in the chrome: the same control repeated per surface would be three
  // siblings for one purpose.
  const shell = ['../shell/TopBar.tsx', '../shell/StatusBar.tsx', './StructureView.tsx', './WelcomeView.tsx'];
  const carriers = shell.filter((f) => /devServiceUrl\(/.test(read(f)));
  assert.deepEqual(carriers, ['../shell/TopBar.tsx']);
  // And it claims nothing about the service being up — a deployment is attested by the ledger.
  assert.doesNotMatch(bar, /\blive\b/i);
});

test('the editor offers no control that only announces a future feature', () => {
  const src = read('./EditorView.tsx');
  assert.doesNotMatch(src, /Split editor/);
  assert.match(src, /KeyCode\.KeyS/); // Cmd/Ctrl-S still saves
});

test('the editor follows the chosen appearance instead of staying dark', () => {
  const theme = read('./monacoTheme.ts');
  assert.match(theme, /devlab-light/);
  assert.match(theme, /data-theme/);
  for (const f of ['./EditorView.tsx', './DiffView.tsx']) {
    assert.match(read(f), /theme=\{devlabThemeName\(\)\}/, f);
  }
});

test('the AI symbol sits on the same side as the AI sidebar', () => {
  const rail = read('../shell/IconRail.tsx');
  // The KI entry is in the right-hand rail, and only there.
  const right = rail.slice(rail.indexOf('RIGHT_ITEMS'));
  const left = rail.slice(rail.indexOf('LEFT_ITEMS'), rail.indexOf('RIGHT_ITEMS'));
  assert.match(right, /id: 'claude'/);
  assert.doesNotMatch(left, /id: 'claude'/);

  const shell = read('../shell/IdeShell.tsx');
  // Order on screen (the rendered body, not the imports): left rail · panel column · editor ·
  // AI sidebar · AI rail.
  const body = shell.slice(shell.indexOf('return ('));
  const order = ['IconRail side="left"', 'PanelHost', 'MainArea', 'ClaudePanel', 'IconRail side="right"'].map((s) =>
    body.indexOf(s),
  );
  assert.ok(
    order.every((at, i) => at > 0 && (i === 0 || at > order[i - 1])),
    `unexpected order: ${order.join(',')}`,
  );
  // One state decides which panel is open — the rails do not keep a second one.
  assert.match(shell, /activePanel === 'claude'/);
});

test('the AI panel marks every live answer with the model that produced it', () => {
  const src = read('../panels/ClaudePanel.tsx');
  assert.match(src, /answerMark\(/);
  // Marks are keyed by the turn's own timestamp, so a trimmed transcript cannot shift a mark onto
  // a different answer.
  assert.match(src, /mark=\{marks\[m\.ts\]\}/);
  // A transcript loaded from the store carries no marks, so none are claimed for it.
  assert.match(src, /setMarks\(\{\}\)/);
});

test('every file-collection surface takes dropped files as well as the dialog and the clipboard', () => {
  for (const f of ['../panels/VisionPanel.tsx', '../panels/ProjectPanel.tsx']) {
    const src = read(f);
    assert.match(src, /onDrop=/, f);
    assert.match(src, /dragHasFiles\(/, f);
    assert.match(src, /usePasteFiles\(/, f);
    // The drop reuses the ONE file extractor rather than a second implementation.
    assert.match(src, /filesFromClipboard\(\{ clipboardData: e\.dataTransfer \}\)/, f);
  }
});

test('the list surfaces answer the keyboard and the context menu', () => {
  for (const f of ['../panels/ProjectPanel.tsx', '../panels/VersionControlPanel.tsx', './TabStrip.tsx', '../panels/CommitGraph.tsx']) {
    const src = read(f);
    assert.match(src, /onKeyDown=/, f);
  }
  for (const f of ['../panels/ProjectPanel.tsx', '../panels/VersionControlPanel.tsx', './TabStrip.tsx']) {
    assert.match(read(f), /ContextMenu/, f);
  }
  // Cmd/Ctrl combinations are decided once, in the tested rules — not re-invented per panel.
  assert.match(read('../panels/treeOps.ts'), /metaKey \|\| e\.ctrlKey/);
  assert.match(read('../panels/ProjectPanel.tsx'), /treeKeyAction\(/);
  // One menu implementation serves them all.
  assert.match(read('../panels/ContextMenu.tsx'), /role="menu"/);
});
