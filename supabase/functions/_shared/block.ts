// The directives a person or a session puts at the top of a text for the mail gateway, and the envelope block the
// gateway writes on the mail it sends (.context/plans/human-sessions-slack-email.md §4). Three forms are read, all
// at the very top of the text (blank lines before them are fine):
//
//   to: 3f9a2                       one line, the common case
//
//   to: 3f9a2                       header lines: `key: value` for the keys below, ended by a blank line or by the
//   summary: Login blank on Safari   first line that is not one of them; the rest is the body
//
//   [brigade]                        the block: `key: value` lines between `[brigade]` and `[/brigade]`; any key
//   to: 3f9a2                        is allowed and an unknown one is ignored (the protocol's own rule for unknown
//   [/brigade]                       members). The gateway writes this form on outbound mail, where it must survive
//                                    quoting and forwarding; mail clients hide text below `-- `, so not a footer.
//
// In every form a value runs to the end of its line and a key named twice is an error the gateway reports back.

export const OPEN = "[brigade]";
export const CLOSE = "[/brigade]";

/** HEADER_KEYS are the keys a header line may use. Only these count, so a lowercase word with a colon at the
 * start of someone's prose (`note: urgent`) ends the headers and stays in the body. */
export const HEADER_KEYS = ["to", "command", "summary", "subject", "reply-to"] as const;

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

/** parseLeading reads the directives at the top of a text: a block as the first thing in it, else one or more
 * header lines (HEADER_KEYS only), ended by a blank line or by the first line that is not a header. It returns the
 * fields, null when there are none, and the text that remains below them. */
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
  const fields: Fields = {};
  let j = i;
  for (; j < lines.length; j++) {
    const l = lines[j].trim();
    if (l === "") break;
    const m = KEY.exec(l);
    if (!m || !(HEADER_KEYS as readonly string[]).includes(m[1])) break;
    if (m[1] in fields) throw new BlockError(`the line "${m[1]}:" appears twice at the top of the text`);
    fields[m[1]] = m[2].trim();
  }
  if (j === i) return { fields: null, rest: text };
  return { fields, rest: trimLeadingBlank(lines.slice(j)) };
}

/** isBareCommand reads a text that is only a command word: `sessions` or `help`, with or without `brigade` in
 * front, in any case. It returns the command, or null. */
export function isBareCommand(text: string): string | null {
  const m = /^\s*(?:brigade\s+)?(sessions|help)\s*$/i.exec(text);
  return m ? m[1].toLowerCase() : null;
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
