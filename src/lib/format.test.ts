import test from 'node:test';
import assert from 'node:assert/strict';
import { fmtCost, fmtDateTime, fmtNum, fmtTime, setUiLocale, uiLocale } from './format.ts';

// A minimal document stand-in: the surface's language lives in ONE attribute, so that is all the
// locale access point needs to read and write.
function withDocumentLang(lang: string | null): void {
  (globalThis as { document?: unknown }).document = {
    documentElement: {
      attrs: new Map<string, string>(lang === null ? [] : [['lang', lang]]),
      getAttribute(name: string) {
        return this.attrs.get(name) ?? null;
      },
      setAttribute(name: string, value: string) {
        this.attrs.set(name, value);
      },
    },
  };
}

test('the chosen UI language is read off the document, and an unusable declaration is neutral', () => {
  withDocumentLang('en');
  assert.equal(uiLocale(), 'en');

  withDocumentLang('de-DE');
  assert.equal(uiLocale(), 'de-DE');

  // Neutral: no declaration, an empty one, and the standard's own "undetermined".
  for (const lang of [null, '', '  ', 'und', 'not a tag!']) {
    withDocumentLang(lang);
    assert.equal(uiLocale(), undefined, `lang=${JSON.stringify(lang)} must resolve to neutral`);
  }

  // The switch is the one place a choice is applied — and back to neutral.
  withDocumentLang('en');
  setUiLocale('fr');
  assert.equal(uiLocale(), 'fr');
  setUiLocale('');
  assert.equal(uiLocale(), undefined);
});

test('every formatter renders in the CHOSEN language, never in one nailed into the code', () => {
  const iso = '2026-07-30T14:05:00Z';

  withDocumentLang('en-GB');
  const en = { date: fmtDateTime(iso), num: fmtNum(1234567) };
  withDocumentLang('de-DE');
  const de = { date: fmtDateTime(iso), num: fmtNum(1234567) };

  assert.notEqual(en.date, de.date, 'the date does not follow the surface language');
  assert.notEqual(en.num, de.num, 'the number grouping does not follow the surface language');
  assert.equal(en.num, '1,234,567');
  assert.equal(de.num, '1.234.567');

  // Neutral formatting still answers — a surface without a declared language is not broken.
  withDocumentLang(null);
  assert.ok(fmtDateTime(iso).length > 0);
  assert.ok(fmtTime(iso).length > 0);
  assert.ok(fmtNum(1234567).length > 0);
});

test('an absent or unparseable instant is an em dash, never an invalid date', () => {
  withDocumentLang('en');
  assert.equal(fmtDateTime(undefined), '—');
  assert.equal(fmtDateTime(''), '—');
  assert.equal(fmtDateTime('not a date'), '—');
});

test('a cost is money, not a locale decision — four decimals with its symbol', () => {
  withDocumentLang('de-DE');
  assert.equal(fmtCost(0.5), '$0.5000');
  assert.equal(fmtCost(12.3456789), '$12.3457');
});
