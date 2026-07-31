// REQ-043 — the PARITY LIST, both directions: everything the UI can do must also be possible over
// the AI chat / MCP, every MCP capability must be covered by the rights system, and neither side
// may carry a capability the other has never heard of.
//
// The two sides are read from their sources, never restated here:
//   · the UI surface   src/data/source.ts — the DataSource interface, the ONE seam between UI and
//                      data. Anything the UI can do goes through one of its operations.
//   · the agent surface contract/mcp-tools.json — the generated projection of the MCP tool table,
//                      each row naming the DataSource operation it mirrors.
// Deliberate gaps live in the artifact's own exception lists WITH their reason, so an uncovered
// capability can never hide as an oversight.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, '..');

interface ToolRow {
  name: string;
  description: string;
  dataSourceOp?: string;
  note?: string;
  method: string;
  route: string;
  tier: string;
  readOnly: boolean;
  destructive?: boolean;
  inputSchema: { type?: string; properties?: Record<string, unknown>; required?: string[] };
}

interface ParityArtifact {
  protocolVersion: string;
  tools: ToolRow[];
  omittedDataSourceOps: { subject: string; reason: string }[];
  untooledRoutes: { subject: string; reason: string }[];
}

const artifact: ParityArtifact = JSON.parse(readFileSync(join(root, 'contract', 'mcp-tools.json'), 'utf8'));

/** The DataSource operations, read out of the interface declaration itself. */
function dataSourceOps(): string[] {
  const src = readFileSync(join(root, 'src', 'data', 'source.ts'), 'utf8');
  const start = src.indexOf('export interface DataSource {');
  assert.ok(start >= 0, 'src/data/source.ts declares no DataSource interface');
  let depth = 0;
  let end = -1;
  for (let i = start; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') {
      depth--;
      if (depth === 0) {
        end = i;
        break;
      }
    }
  }
  assert.ok(end > start, 'the DataSource interface is not closed');
  const body = src.slice(start, end);
  // A member declaration sits at the interface's own indentation level (two spaces) and opens its
  // parameter list right after the name — nested object types and comments never match.
  return [...body.matchAll(/^ {2}([a-zA-Z][A-Za-z0-9]*)\s*\(/gm)].map((m) => m[1]);
}

const ops = dataSourceOps();
const toolByOp = new Map<string, ToolRow[]>();
for (const t of artifact.tools) {
  if (!t.dataSourceOp) continue;
  const list = toolByOp.get(t.dataSourceOp) ?? [];
  list.push(t);
  toolByOp.set(t.dataSourceOp, list);
}
const omitted = new Map(artifact.omittedDataSourceOps.map((x) => [x.subject, x.reason]));

test('the parity list is a list: both sides are read, not restated', () => {
  assert.ok(ops.length >= 60, `only ${ops.length} DataSource operations parsed — the reader lost its grip`);
  assert.ok(artifact.tools.length >= 60, `only ${artifact.tools.length} tools in the artifact`);
  assert.ok(artifact.protocolVersion.length > 0, 'the artifact names no protocol revision');
});

test('forward: every UI capability is reachable over MCP (or omitted with a reason)', () => {
  const uncovered: string[] = [];
  for (const op of ops) {
    if (toolByOp.has(op)) continue;
    const reason = omitted.get(op);
    if (!reason) {
      uncovered.push(op);
      continue;
    }
    // An omission is only honest with a substantive reason, not a shrug.
    assert.ok(reason.length > 25, `the omission of "${op}" is not explained: "${reason}"`);
  }
  assert.deepEqual(
    uncovered,
    [],
    `UI capabilities an agent cannot perform (${uncovered.length}): ${uncovered.join(', ')}`,
  );
});

test('backward: every MCP tool mirrors a real UI capability (or says why it cannot)', () => {
  const strays: string[] = [];
  const known = new Set(ops);
  for (const t of artifact.tools) {
    if (!t.dataSourceOp) {
      // A tool with no counterpart must name the reason — the configuration write is the case the
      // architecture foresees: the UI deliberately does not maintain configuration.
      assert.ok(
        (t.note ?? '').length > 25,
        `tool "${t.name}" mirrors no UI capability and gives no reason`,
      );
      continue;
    }
    if (!known.has(t.dataSourceOp)) strays.push(`${t.name} → ${t.dataSourceOp}`);
  }
  assert.deepEqual(
    strays,
    [],
    `tools naming a DataSource operation that does not exist: ${strays.join(', ')}`,
  );
});

test('no similar siblings: one capability is exposed by exactly one tool', () => {
  const doubles = [...toolByOp.entries()]
    .filter(([, list]) => list.length > 1)
    .map(([op, list]) => `${op} → ${list.map((t) => t.name).join(' + ')}`);
  assert.deepEqual(doubles, [], `one capability exposed twice: ${doubles.join('; ')}`);
});

test('every exposed capability is covered by the rights system and its guard tier', () => {
  const tiers = new Set(['read', 'csrf', 'write']);
  for (const t of artifact.tools) {
    assert.ok(tiers.has(t.tier), `tool "${t.name}" carries the unknown tier "${t.tier}"`);
    // A read-only tool is exactly a GET; anything that changes state must sit above the read tier,
    // which is what makes the CSRF and link checks apply to it.
    if (t.readOnly) {
      assert.equal(t.method, 'GET', `tool "${t.name}" claims to be read-only but is a ${t.method}`);
      assert.equal(t.tier, 'read', `read-only tool "${t.name}" sits in tier "${t.tier}"`);
    } else {
      assert.notEqual(t.method, 'GET', `mutating tool "${t.name}" rides a GET route`);
      assert.notEqual(t.tier, 'read', `mutating tool "${t.name}" sits in the read tier`);
    }
  }
});

test('every tool is self-describing: name convention, description, route and argument schema', () => {
  const seen = new Set<string>();
  for (const t of artifact.tools) {
    assert.match(t.name, /^[a-z][a-z0-9]*(_[a-z0-9]+)+$/, `tool name "${t.name}" breaks the <subject>_<verb> convention`);
    assert.ok(!seen.has(t.name), `duplicate tool name "${t.name}"`);
    seen.add(t.name);
    assert.ok(t.description.length > 20, `tool "${t.name}" has no usable description`);
    assert.match(t.route, /^(GET|POST|PUT|DELETE) \/api\//, `tool "${t.name}" names no route: "${t.route}"`);
    assert.equal(t.inputSchema.type, 'object', `tool "${t.name}" has no object argument schema`);
    for (const required of t.inputSchema.required ?? []) {
      assert.ok(
        t.inputSchema.properties && required in t.inputSchema.properties,
        `tool "${t.name}" requires "${required}", which its schema does not declare`,
      );
    }
  }
});

test('a destructive capability is marked as destructive so an agent can hesitate', () => {
  // Deleting and rolling back are the irreversible-looking actions of the surface; a tool that
  // performs one must say so, exactly as the UI must explain effect and reversal.
  const shouldBeDestructive = artifact.tools.filter(
    (t) => /(_delete|_rollback|_reset|_restore|_clear)$/.test(t.name) && !t.destructive,
  );
  assert.deepEqual(
    shouldBeDestructive.map((t) => t.name),
    [],
    'destructive capabilities that are not marked destructive',
  );
});

test('the untooled routes are named with their reason, so the surface has no silent gap', () => {
  assert.ok(artifact.untooledRoutes.length > 0, 'no route exceptions at all — the stream routes must be listed');
  for (const ex of artifact.untooledRoutes) {
    assert.ok(ex.subject.length > 0, 'a route exception without a subject');
    assert.ok(ex.reason.length > 20, `the route exception "${ex.subject}" is not explained: "${ex.reason}"`);
  }
});
