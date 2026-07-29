import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  AGENT_ENGINE,
  AGENT_MODES,
  ENGINES,
  TRANSCRIPT_CAP,
  answerMark,
  capTranscript,
  changeNote,
  modelLabel,
  modelsForEngine,
} from './aiChat.ts';

test('an answer is marked with the model that produced it', () => {
  assert.equal(answerMark({ model: 'claude-opus-4-8', engine: 'claude-cli' }), 'claude-opus-4-8');
});

test('an answer whose engine named no model is marked with the engine, never a made-up model', () => {
  assert.equal(answerMark({ model: '', engine: 'ollama' }), 'ollama');
  assert.equal(answerMark({ engine: 'claude-api' }), 'claude-api');
  assert.equal(answerMark({}), ''); // unknown stays unknown
  assert.equal(answerMark({ model: '   ' }), '');
});

test('the model picker shows which version it selects', () => {
  assert.equal(modelLabel({ id: 'claude-opus-4-8', label: 'Opus' }), 'Opus 4.8');
  assert.equal(modelLabel({ id: 'claude-fable-5', label: 'Fable' }), 'Fable 5');
  // Already carries the version → untouched.
  assert.equal(modelLabel({ id: 'claude-opus-4-8', label: 'Opus 4.8' }), 'Opus 4.8');
  // No version in the id (a local tag) → untouched.
  assert.equal(modelLabel({ id: 'llama3:latest', label: 'llama3:latest' }), 'llama3:latest');
});

test('each engine offers exactly the models it can serve', () => {
  const catalog = { claude: [{ id: 'claude-opus-4-8', label: 'Opus' }], ollama: ['llama3'] };
  assert.deepEqual(modelsForEngine('choose', catalog), []); // the router picks
  assert.deepEqual(modelsForEngine('ollama', catalog), [{ id: 'llama3', label: 'llama3' }]);
  assert.deepEqual(modelsForEngine('claude-cli', catalog), catalog.claude);
  assert.deepEqual(modelsForEngine(AGENT_ENGINE, catalog), catalog.claude);
  assert.deepEqual(modelsForEngine('claude-cli', null), []);
});

test('the transcript is capped to its newest turns — the same cap the store enforces', () => {
  const msgs = Array.from({ length: TRANSCRIPT_CAP + 40 }, (_, i) => i);
  const capped = capTranscript(msgs);
  assert.equal(capped.length, TRANSCRIPT_CAP);
  assert.equal(capped[capped.length - 1], msgs[msgs.length - 1]);
  assert.equal(capped[0], 40);
  // Under the cap nothing is dropped, and the input is not mutated.
  assert.deepEqual(capTranscript([1, 2, 3]), [1, 2, 3]);
});

test('file changes made by the agent are always stated', () => {
  assert.equal(changeNote(0), '');
  assert.match(changeNote(1), /1 file changed/);
  assert.match(changeNote(3), /3 files changed/);
});

test('the offered engines and agent modes are named in English and unique', () => {
  const ids = [...ENGINES.map((e) => e.id), ...AGENT_MODES.map((m) => m.id)];
  assert.equal(new Set(ids).size, ids.length);
  for (const { label } of [...ENGINES, ...AGENT_MODES]) {
    assert.doesNotMatch(label, /[äöüß]|lokal|nur lesen|bearbeiten|autonom\b/i, label);
  }
  assert.ok(ENGINES.some((e) => e.id === AGENT_ENGINE));
});
