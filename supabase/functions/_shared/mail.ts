// Mail shapes and the text rules every provider shares: address parsing, header lookup, Message-ID tokens,
// the split between what a person typed and what their client quoted, and a last-resort HTML-to-text.

export interface InboundMail {
  /** The provider's own id for the received mail. */
  providerId: string;
  /** The sender's address, lowercase; unverified text like every label in Brigade. */
  from: string;
  fromName: string | null;
  /** Every address the mail was delivered to, lowercase. */
  to: string[];
  subject: string;
  /** RFC 5322 Message-ID of the mail, with its angle brackets, when the provider exposes it. */
  messageId: string | null;
  inReplyTo: string | null;
  references: string[];
  /** The plain-text body; the HTML reduced to text when there was no text part. */
  text: string;
  receivedAt: string;
}

export interface OutboundMail {
  from: string;
  to: string;
  replyTo: string | null;
  subject: string;
  text: string;
  headers: Record<string, string>;
}

const ADDRESS = /^[^\s@<>]+@[^\s@<>]+\.[^\s@<>]+$/;

/** isAddress reports whether s is a plausible bare email address. */
export function isAddress(s: string): boolean {
  return ADDRESS.test(s);
}

/** parseAddress reads `Name <addr>` or a bare address. The address comes back lowercase. */
export function parseAddress(s: string): { address: string; name: string | null } {
  const m = /^\s*(?:"?([^"<]*?)"?\s*)?<([^<>]+)>\s*$/.exec(s);
  if (m) return { address: m[2].trim().toLowerCase(), name: m[1]?.trim() || null };
  return { address: s.trim().toLowerCase(), name: null };
}

/** headerValue finds a header by name, case-insensitively; a repeated header answers its first value. */
export function headerValue(
  headers: Record<string, unknown> | null | undefined,
  name: string,
): string | null {
  if (!headers) return null;
  const want = name.toLowerCase();
  for (const [k, v] of Object.entries(headers)) {
    if (k.toLowerCase() !== want || v === null || v === undefined) continue;
    return Array.isArray(v) ? String(v[0]) : String(v);
  }
  return null;
}

/** parseMessageIds extracts the `<…>` tokens of a Message-ID, In-Reply-To or References header. */
export function parseMessageIds(s: string | null | undefined): string[] {
  if (!s) return [];
  return [...s.matchAll(/<[^<>\s]+>/g)].map((m) => m[0]);
}

/** stripQuoted splits a reply into the part the person typed and the part their client quoted under it. The
 * quoted part begins at the first line that starts with `>`, at an "On … wrote:" attribution (one line, or
 * wrapped onto two), at an Outlook "-----Original Message-----" rule, or at a `-- ` signature separator. The
 * quoted half comes back with one level of `> ` removed from each line, so a block inside it can still be read. */
export function stripQuoted(text: string): { body: string; quoted: string } {
  const lines = text.replace(/\r\n?/g, "\n").split("\n");
  let cut = lines.length;
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i];
    const two = l + " " + (lines[i + 1] ?? "");
    if (
      /^>/.test(l) ||
      l === "-- " ||
      /^-----\s*Original Message\s*-----/i.test(l) ||
      /^On .{3,300}wrote:\s*$/.test(l) ||
      (/^On .{3,300}$/.test(l) && /^On .{3,400}wrote:\s*$/.test(two))
    ) {
      cut = i;
      break;
    }
  }
  const body = lines.slice(0, cut).join("\n").replace(/\s+$/, "");
  const quoted = lines
    .slice(cut)
    .map((l) => l.replace(/^>\s?/, ""))
    .join("\n");
  return { body, quoted };
}

/** htmlToText is the fallback for a mail with no text part: block tags become line breaks, every other tag is
 * dropped, the common entities are decoded, and runs of blank lines are folded. Not a renderer. */
export function htmlToText(html: string): string {
  let s = html.replace(/<(script|style)[\s\S]*?<\/\1>/gi, "");
  s = s.replace(/<br\s*\/?>/gi, "\n").replace(/<\/(p|div|li|tr|h[1-6]|blockquote|pre)>/gi, "\n");
  s = s.replace(/<blockquote[^>]*>/gi, "\n> ");
  s = s.replace(/<[^>]+>/g, "");
  s = s
    .replace(/&nbsp;/g, " ")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&#39;|&apos;/g, "'")
    .replace(/&amp;/g, "&");
  return s.replace(/[ \t]+\n/g, "\n").replace(/\n{3,}/g, "\n\n").trim();
}

/** domainOf returns the part of an address after its `@`, lowercase. */
export function domainOf(address: string): string {
  const at = address.lastIndexOf("@");
  return at < 0 ? "" : address.slice(at + 1).toLowerCase();
}

/** localOf returns the part of an address before its `@`. */
export function localOf(address: string): string {
  const at = address.lastIndexOf("@");
  return at < 0 ? address : address.slice(0, at);
}
