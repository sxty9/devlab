// Surface rules of the IDE that are decided by the SOURCE, not by runtime data, and would
// otherwise only be caught by review: no asserted repo state (B 1.4), no tooltip-carried meaning
// ("intuitive by design"), and the AI on the right (REQ-043.2). Read as text, because these are
// statements about what the components may contain.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const read = (p: string) => readFileSync(new URL(p, import.meta.url), 'utf8');

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
  assert.doesNotMatch(src, /mock/i); // no simulated data source is described any more
  assert.doesNotMatch(src, /phase 1|phase 2/i); // no roadmap claims on the surface
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
