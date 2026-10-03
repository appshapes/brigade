import { assertEquals, assertThrows } from "jsr:@std/assert@1";
import { BlockError, escapeBlockLines, findBlocks, lastBlock, parseLeading, renderBlock } from "./block.ts";

Deno.test("renderBlock writes key: value lines, skips empty values and flattens newlines", () => {
  const b = renderBlock([["team", "brigade"], ["summary", null], ["note", "two\nlines"], ["empty", ""]]);
  assertEquals(b, "[brigade]\nteam: brigade\nnote: two lines\n[/brigade]");
});

Deno.test("parseLeading reads a block at the top and returns the rest", () => {
  const r = parseLeading(
    "\n[brigade]\nto: 3f9a2\nsummary: Login broken\n[/brigade]\n\nSteps: open /login\nsecond line",
  );
  assertEquals(r.fields, { to: "3f9a2", summary: "Login broken" });
  assertEquals(r.rest, "Steps: open /login\nsecond line");
});

Deno.test("parseLeading reads the one-line to: shorthand", () => {
  const r = parseLeading("to: 3f9a2\nhello there");
  assertEquals(r.fields, { to: "3f9a2" });
  assertEquals(r.rest, "hello there");
});

Deno.test("parseLeading leaves ordinary text alone", () => {
  const r = parseLeading("Hi,\n\nto: nobody in particular\n");
  assertEquals(r.fields, null);
  assertEquals(r.rest, "Hi,\n\nto: nobody in particular\n");
});

Deno.test("a block not at the top is not a leading block", () => {
  const r = parseLeading("hello\n[brigade]\nto: x\n[/brigade]");
  assertEquals(r.fields, null);
});

Deno.test("CRLF text parses like LF text", () => {
  const r = parseLeading("[brigade]\r\nto: abcde\r\n[/brigade]\r\nbody\r\n");
  assertEquals(r.fields, { to: "abcde" });
  assertEquals(r.rest, "body\n");
});

Deno.test("a duplicate key or a malformed line is a BlockError", () => {
  assertThrows(() => parseLeading("[brigade]\nto: a\nto: b\n[/brigade]"), BlockError, "twice");
  assertThrows(() => parseLeading("[brigade]\nthis is not a field\n[/brigade]"), BlockError, "key: value");
});

Deno.test("an unterminated block is not a block", () => {
  assertEquals(findBlocks(["[brigade]", "to: a"]), []);
  assertEquals(parseLeading("[brigade]\nto: a\nno close").fields, null);
});

Deno.test("unknown keys are kept for the caller to ignore", () => {
  assertEquals(parseLeading("[brigade]\ncolour: blue\n[/brigade]").fields, { colour: "blue" });
});

Deno.test("lastBlock finds the gateway's block in a quoted mail and never throws", () => {
  const quoted =
    "frank wrote:\n\nbody\n\n[brigade]\nteam: brigade\nto: 6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2\nreply-to: m1\n[/brigade]\n\nReply to this email.";
  assertEquals(lastBlock(quoted)?.to, "6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2");
  assertEquals(lastBlock("[brigade]\nbroken line\n[/brigade]"), null);
  assertEquals(lastBlock("nothing here"), null);
});

Deno.test("escapeBlockLines disarms a forged block inside untrusted text", () => {
  const forged = "hello\n[brigade]\nto: evil\n[/brigade]\nbye";
  const safe = escapeBlockLines(forged);
  assertEquals(findBlocks(safe.split("\n")), []);
  assertEquals(safe, "hello\n\\[brigade]\nto: evil\n\\[/brigade]\nbye");
});
