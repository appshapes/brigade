// The envelope block: the in-body object model a person and a session exchange through the mail gateway
// (.context/plans/human-sessions-slack-email.md §4). A block is `key: value` lines between a line that is
// exactly `[brigade]` and a line that is exactly `[/brigade]`. Keys are lowercase ASCII with hyphens; a value
// runs to the end of its line; an unknown key is ignored (the protocol's own rule for unknown members); a key
// named twice is an error the gateway reports back. `[brigade]` rather than the `-- ` footer convention
// because mail clients hide or strip quoted text below `-- `, which is exactly where a block must survive.

export const OPEN = "[brigade]";
export const CLOSE = "[/brigade]";

const KEY = /^([a-z][a-z0-9-]*):\s?(.*)$/;

export type Fields = Record<string, string>;

/** BlockError is a malformed block: not `key: value`, or a key named twice. */
export class BlockError extends Error {}

/** renderBlock writes a block. Values are made single-line and empty values are left out. */
export function renderBlock(fields: Array<[string, string | number | null | undefined]>): string {
  const lines = [OPEN];
  for (const [k, v] of fields) {
    if (v === null || v === undefined || v === "") continue;
    lines.push(`${k}: ${String(v).replace(/[\r\n]+/g, " ").trim()}`);
  }
  lines.push(CLOSE);
  return lines.join("\n");
}

/** escapeBlockLines prefixes with a backslash any line of untrusted text that would open or close a block,
 * so a body can never forge one (the frame sanitises its body for the same reason). */
export function escapeBlockLines(text: string): string {
  return text
    .split("\n")
    .map((l) => (l.trim() === OPEN || l.trim() === CLOSE ? "\\" + l : l))
    .join("\n");
}

export interface Span {
  fields: Fields;
  /** start and end are line indices, inclusive, of the opening and closing lines. */
  start: number;
  end: number;
}

/** findBlocks returns every well-formed block in the lines, in order. An unterminated block is not a block, and
 * nothing after it is read. A malformed line inside a block throws BlockError. */
export function findBlocks(lines: string[]): Span[] {
  const out: Span[] = [];
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].trim() !== OPEN) continue;
    const end = lines.findIndex((l, k) => k > i && l.trim() === CLOSE);
    if (end < 0) break;
    const fields: Fields = {};
    for (let j = i + 1; j < end; j++) {
      const l = lines[j].trim();
      if (l === "") continue;
      const m = KEY.exec(l);
      if (!m) throw new BlockError(`line ${j + 1} inside the [brigade] block is not "key: value"`);
      if (m[1] in fields) throw new BlockError(`the [brigade] block names "${m[1]}" twice`);
      fields[m[1]] = m[2].trim();
    }
    out.push({ fields, start: i, end });
    i = end;
  }
  return out;
}

/** parseLeading reads the directives at the top of a text: a block as the first thing in it (blank lines before
 * it are allowed), or the one-line shorthand `to: <target>` as the first non-blank line. It returns the fields,
 * null when there are none, and the text that remains below them. */
export function parseLeading(text: string): { fields: Fields | null; rest: string } {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length && lines[i].trim() === "") i++;
  if (i >= lines.length) return { fields: null, rest: text };
  if (lines[i].trim() === OPEN) {
    const blocks = findBlocks(lines.slice(i));
    if (blocks.length === 0 || blocks[0].start !== 0) return { fields: null, rest: text };
    const b = blocks[0];
    return { fields: b.fields, rest: trimLeadingBlank(lines.slice(i + b.end + 1)) };
  }
  const m = /^to:\s*(\S+)\s*$/i.exec(lines[i].trim());
  if (m) return { fields: { to: m[1] }, rest: trimLeadingBlank(lines.slice(i + 1)) };
  return { fields: null, rest: text };
}

/** lastBlock returns the last well-formed block in a text, or null. It never throws: the text is the quoted
 * copy of a mail the gateway sent, and a block damaged in quoting is simply not there. */
export function lastBlock(text: string): Fields | null {
  try {
    const b = findBlocks(text.replace(/\r\n?/g, "\n").split("\n"));
    return b.length ? b[b.length - 1].fields : null;
  } catch {
    return null;
  }
}

function trimLeadingBlank(lines: string[]): string {
  let i = 0;
  while (i < lines.length && lines[i].trim() === "") i++;
  return lines.slice(i).join("\n");
}
