import { assertEquals } from "jsr:@std/assert@1";
import { headerValue, htmlToText, isAddress, parseAddress, parseMessageIds, stripQuoted } from "./mail.ts";

Deno.test("parseAddress reads display names and bare addresses, lowercasing the address", () => {
  assertEquals(parseAddress('"Alice Example" <Alice@Example.com>'), {
    address: "alice@example.com",
    name: "Alice Example",
  });
  assertEquals(parseAddress("Alice <alice@example.com>"), { address: "alice@example.com", name: "Alice" });
  assertEquals(parseAddress("ALICE@example.com"), { address: "alice@example.com", name: null });
});

Deno.test("isAddress accepts a plausible address and refuses a session id", () => {
  assertEquals(isAddress("qa@example.com"), true);
  assertEquals(isAddress("3f9a2"), false);
  assertEquals(isAddress("a@b"), false);
});

Deno.test("headerValue is case-insensitive and takes the first of a repeated header", () => {
  assertEquals(headerValue({ "In-Reply-To": "<a@x>" }, "in-reply-to"), "<a@x>");
  assertEquals(headerValue({ received: ["one", "two"] }, "Received"), "one");
  assertEquals(headerValue({}, "x"), null);
});

Deno.test("parseMessageIds extracts angle-bracketed ids", () => {
  assertEquals(parseMessageIds("<a@x> <b@y>\n <c@z>"), ["<a@x>", "<b@y>", "<c@z>"]);
  assertEquals(parseMessageIds(null), []);
});

Deno.test("stripQuoted cuts at a quoted line, an attribution line and a signature", () => {
  const gmail =
    "Looks good, ship it.\n\nOn Fri, Oct 3, 2026 at 9:00 AM Brigade <brigade@appshapes.com> wrote:\n> frank wrote:\n> body\n> [brigade]\n> to: abc\n> [/brigade]";
  const r = stripQuoted(gmail);
  assertEquals(r.body, "Looks good, ship it.");
  assertEquals(r.quoted.includes("[brigade]\nto: abc\n[/brigade]"), true);

  const wrapped = "Yes.\nOn Fri, Oct 3, 2026 at 9:00 AM Brigade\n<brigade@appshapes.com> wrote:\n> q";
  assertEquals(stripQuoted(wrapped).body, "Yes.");

  const outlook = "Agreed\r\n\r\n-----Original Message-----\r\nFrom: x\r\n";
  assertEquals(stripQuoted(outlook).body, "Agreed");

  const sig = "Thanks\n-- \nAlice\nQA";
  assertEquals(stripQuoted(sig).body, "Thanks");

  assertEquals(stripQuoted("plain\ntext").body, "plain\ntext");
});

Deno.test("htmlToText keeps the words and the line breaks", () => {
  const t = htmlToText("<div>Hello<br>there</div><p>Second &amp; last &lt;tag&gt;</p><style>p{}</style>");
  assertEquals(t, "Hello\nthere\nSecond & last <tag>");
});
