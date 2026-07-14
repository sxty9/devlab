// The axiom document is markdown with XML-ish semantic tags: top-level sections, each holding prose
// and/or <maxim name="..."> blocks. There is no front-matter, no ids and no schema — the structure
// is the tag nesting, and the file itself is the single source of truth.
//
// So this is a PROJECTION, not a model: parse() reads the document into something navigable, and
// splice() writes a single block's bytes back in place. Nothing is ever re-serialised from the
// parsed form, so a Mercury edit shows up in `git diff` as exactly the block that changed and the
// rest of the document is byte-identical. Anything else would make Mercury a second source of truth
// for the constitution, and would reformat it on every save.

const OPEN_NAMED = /^<([a-z_][a-z0-9_]*)\s+name="([^"]*)"\s*>\s*$/i;
const OPEN_BARE = /^<([a-z_][a-z0-9_]*)\s*>\s*$/i;
const CLOSE = /^<\/([a-z_][a-z0-9_]*)\s*>\s*$/i;

/** One addressable block: a named <maxim>, or a bare nested tag treated as a block of its own. */
export interface AxiomEntry {
  /** Stable within a document: `<section>/<name>`. */
  id: string;
  /** The tag this block is written with (`maxim`, `klaerung_im_nachtlauf`, …). */
  tag: string;
  /** What to show in the navigator — the name attribute, else the tag. */
  name: string;
  /** The block's inner text, verbatim. */
  body: string;
  /** Byte offsets of `body` in the source — the splice anchors. */
  start: number;
  end: number;
}

/** A top-level tag of the axiom document. */
export interface AxiomSection {
  tag: string;
  /** Prose sitting directly in the section, outside any block. */
  intro: string;
  entries: AxiomEntry[];
}

/** Read the axiom document into sections and blocks. Text outside any top-level tag is ignored. */
export function parseAxioms(md: string): AxiomSection[] {
  const sections: AxiomSection[] = [];

  let section: AxiomSection | null = null;
  let sectionIntroStart = 0;
  let block: { tag: string; name: string; start: number } | null = null;

  let offset = 0;
  for (const line of md.split('\n')) {
    const lineStart = offset;
    const lineEnd = offset + line.length + 1; // +1 for the '\n' we split on
    offset = lineEnd;

    const text = line.trim();
    const close = CLOSE.exec(text);
    const named = OPEN_NAMED.exec(text);
    const bare = !named && !close ? OPEN_BARE.exec(text) : null;

    if (!section) {
      // Only a bare tag opens a section; a stray </tag> or prose at top level is ignored.
      if (bare) {
        section = { tag: bare[1], intro: '', entries: [] };
        sectionIntroStart = lineEnd;
      }
      continue;
    }

    if (block) {
      if (close && close[1].toLowerCase() === block.tag.toLowerCase()) {
        section.entries.push({
          id: `${section.tag}/${block.name}`,
          tag: block.tag,
          name: block.name,
          body: md.slice(block.start, lineStart),
          start: block.start,
          end: lineStart,
        });
        block = null;
      }
      continue; // everything else belongs to the block's body, which we slice by offset
    }

    if (close && close[1].toLowerCase() === section.tag.toLowerCase()) {
      if (!section.intro) section.intro = md.slice(sectionIntroStart, lineStart).trim();
      sections.push(section);
      section = null;
      continue;
    }

    if (named) {
      // The prose seen so far is the section's intro (only the run before its first block counts).
      if (!section.entries.length && !section.intro) section.intro = md.slice(sectionIntroStart, lineStart).trim();
      block = { tag: named[1], name: named[2], start: lineEnd };
      continue;
    }

    if (bare) {
      if (!section.entries.length && !section.intro) section.intro = md.slice(sectionIntroStart, lineStart).trim();
      block = { tag: bare[1], name: bare[1], start: lineEnd };
    }
  }

  return sections;
}

/** Replace one block's body, leaving every other byte of the document untouched. */
export function spliceAxiom(md: string, entry: AxiomEntry, body: string): string {
  const normalised = body.endsWith('\n') ? body : `${body}\n`;
  return md.slice(0, entry.start) + normalised + md.slice(entry.end);
}

/** Prepare a block's body for the markdown renderer.
 *
 *  Bodies can nest further tags (the nightly run's phases wrap a dozen of them). Left as-is they are
 *  parsed as unknown HTML elements and sanitised away, so their headings would silently vanish and
 *  the text would run together. Promote them to bold labels instead. This is a render-time
 *  projection — the document on disk is never touched. */
export function axiomBodyToMarkdown(body: string): string {
  return body
    .split('\n')
    .map((line) => {
      const text = line.trim();
      if (CLOSE.test(text)) return '';
      const named = OPEN_NAMED.exec(text);
      if (named) return `\n**${named[2]}**\n`;
      const bare = OPEN_BARE.exec(text);
      if (bare) return `\n**${bare[1]}**\n`;
      return line;
    })
    .join('\n');
}
