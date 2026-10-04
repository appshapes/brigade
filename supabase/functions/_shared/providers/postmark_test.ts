import { assertEquals, assertRejects, assertStringIncludes } from "jsr:@std/assert@1";
import { postmarkInbound, postmarkProvider, withHash } from "./postmark.ts";
import { SendError, WebhookRejected } from "../connector.ts";
import type { OutboundMail } from "../mail.ts";

const INBOX = "0123456789abcdef0123456789abcdef01234567@inbound.postmarkapp.com";
const TAG = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e";
const SECRET = "fedcba9876543210fedcba9876543210fedcba9876543210";
const HOOK = "https://x.supabase.co/functions/v1/mail-connector-postmark/webhook";

// The shape of Postmark's documented inbound webhook example, with placeholder values.
function payload(over: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    FromName: "Alice Example",
    MessageStream: "inbound",
    From: "alice@example.com",
    FromFull: { Email: "alice@example.com", Name: "Alice Example", MailboxHash: "" },
    To: `"Brigade" <${INBOX.replace("@", `+${TAG}@`)}>`,
    ToFull: [{ Email: INBOX, Name: "Brigade", MailboxHash: TAG }],
    Cc: "",
    CcFull: [],
    Bcc: "",
    BccFull: [],
    OriginalRecipient: INBOX,
    Subject: "Re: [brigade/team] Please retest",
    MessageID: "73e6d360-66eb-11e1-8e72-a8904824019b",
    ReplyTo: "",
    MailboxHash: TAG,
    Date: "Sat, 4 Oct 2026 10:00:00 -0400",
    TextBody: "Retested, works.\n\nOn Sat, Oct 4, 2026 Brigade wrote:\n> please retest",
    HtmlBody: "<p>Retested, works.</p>",
    StrippedTextReply: "Retested, works.",
    Tag: "",
    Headers: [
      { Name: "Message-ID", Value: "<CA+abc@mail.example.com>" },
      { Name: "In-Reply-To", Value: "<0f0f0f0f@mtasv.net>" },
      { Name: "References", Value: "<0f0f0f0f@mtasv.net>" },
      { Name: "X-Spam-Status", Value: "No" },
    ],
    Attachments: [],
    ...over,
  };
}

Deno.test("withHash puts a MailboxHash back as the +tag and lowercases", () => {
  assertEquals(withHash("Hash@Inbound.Postmarkapp.com", "ABC"), "hash+abc@inbound.postmarkapp.com");
  assertEquals(withHash("hash+abc@inbound.postmarkapp.com", "abc"), "hash+abc@inbound.postmarkapp.com");
  assertEquals(withHash("hash@inbound.postmarkapp.com", ""), "hash@inbound.postmarkapp.com");
  assertEquals(withHash("", "abc"), "");
});

Deno.test("postmarkInbound maps the webhook payload to the gateway's inbound mail", () => {
  const m = postmarkInbound(payload());
  assertEquals(m.providerId, "73e6d360-66eb-11e1-8e72-a8904824019b");
  assertEquals(m.from, "alice@example.com");
  assertEquals(m.fromName, "Alice Example");
  assertEquals(m.to, [INBOX.replace("@", `+${TAG}@`)]);
  assertEquals(m.subject, "Re: [brigade/team] Please retest");
  assertEquals(m.messageId, "<CA+abc@mail.example.com>");
  assertEquals(m.inReplyTo, "<0f0f0f0f@mtasv.net>");
  assertEquals(m.references, ["<0f0f0f0f@mtasv.net>"]);
  assertStringIncludes(m.text, "Retested, works.");
  assertEquals(m.receivedAt, "2026-10-04T14:00:00.000Z");
});

Deno.test("postmarkInbound falls back to the HTML body, to From, and to now", () => {
  const m = postmarkInbound(payload({
    TextBody: "",
    FromFull: undefined,
    From: "Bob <bob@example.com>",
    FromName: "",
    Date: "not a date",
    ToFull: [{ Email: "cc@example.com", Name: "", MailboxHash: "" }],
    CcFull: [{ Email: INBOX, Name: "", MailboxHash: "" }],
    OriginalRecipient: "",
  }));
  assertEquals(m.text, "Retested, works.");
  assertEquals(m.from, "bob@example.com");
  assertEquals(m.fromName, "Bob");
  assertEquals(m.to, ["cc@example.com", INBOX]);
  assertEquals(Number.isNaN(new Date(m.receivedAt).getTime()), false);
});

function hook(body: unknown, headers: Record<string, string> = {}, url = HOOK): Request {
  return new Request(url, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
}

Deno.test("the webhook is authenticated by the basic-auth password or the path, and refused with 403", async () => {
  const p = postmarkProvider({ serverToken: "t", webhookSecret: SECRET });
  const raw = JSON.stringify(payload());
  const basic = { Authorization: "Basic " + btoa(`brigade:${SECRET}`) };
  const got = await p.webhook(hook(raw, basic), raw);
  assertEquals(got?.providerId, "73e6d360-66eb-11e1-8e72-a8904824019b");
  assertEquals(got?.mail?.from, "alice@example.com");
  assertEquals((await p.webhook(hook(raw, {}, `${HOOK}/${SECRET}`), raw))?.providerId, got?.providerId);
  const e = await assertRejects(() => p.webhook(hook(raw), raw), WebhookRejected);
  assertEquals(e.status, 403);
  const wrong = { Authorization: "Basic " + btoa("brigade:nope") };
  assertEquals((await assertRejects(() => p.webhook(hook(raw, wrong), raw), WebhookRejected)).status, 403);
  const unset = postmarkProvider({ serverToken: "t", webhookSecret: null });
  assertEquals(
    (await assertRejects(() => unset.webhook(hook(raw, basic), raw), WebhookRejected)).status,
    403,
  );
  assertEquals(await p.webhook(hook("not json", basic), "not json"), null);
  const bounce = JSON.stringify({ RecordType: "Bounce", MessageID: "x", MessageStream: "outbound" });
  assertEquals(await p.webhook(hook(bounce, basic), bounce), null);
});

function fakeFetch(answer: (url: string, init: RequestInit) => Response): typeof fetch {
  return ((url: string | URL | Request, init?: RequestInit) =>
    Promise.resolve(answer(String(url), init ?? {}))) as typeof fetch;
}

const outbound: OutboundMail = {
  from: "Brigade <brigade@example.com>",
  to: "alice@example.com",
  replyTo: INBOX.replace("@", `+${TAG}@`),
  subject: "[brigade/team] hello",
  text: "hello",
  headers: { "Auto-Submitted": "auto-generated", "In-Reply-To": "<x@example.com>" },
};

Deno.test("send POSTs Postmark's shape with the server token and maps its answers", async () => {
  let seen: { url: string; token: string | null; body: Record<string, unknown> } | null = null;
  const p = postmarkProvider({
    serverToken: "server-token",
    webhookSecret: SECRET,
    fetchFn: fakeFetch((url, init) => {
      seen = {
        url,
        token: new Headers(init.headers).get("x-postmark-server-token"),
        body: JSON.parse(String(init.body)),
      };
      return new Response(JSON.stringify({ ErrorCode: 0, Message: "OK", MessageID: "pm-9" }), {
        status: 200,
      });
    }),
  });
  assertEquals((await p.send(outbound)).providerId, "pm-9");
  assertEquals(seen!.url, "https://api.postmarkapp.com/email");
  assertEquals(seen!.token, "server-token");
  assertEquals(seen!.body.From, outbound.from);
  assertEquals(seen!.body.To, outbound.to);
  assertEquals(seen!.body.ReplyTo, outbound.replyTo);
  assertEquals(seen!.body.TextBody, "hello");
  assertEquals(seen!.body.MessageStream, "outbound");
  assertEquals(seen!.body.Headers, [
    { Name: "Auto-Submitted", Value: "auto-generated" },
    { Name: "In-Reply-To", Value: "<x@example.com>" },
  ]);
  assertEquals("HtmlBody" in seen!.body, false);

  const inactive = postmarkProvider({
    serverToken: "t",
    webhookSecret: SECRET,
    fetchFn: fakeFetch(() =>
      new Response(JSON.stringify({ ErrorCode: 406, Message: "inactive recipient" }), { status: 422 })
    ),
  });
  const e1 = await assertRejects(() => inactive.send(outbound), SendError);
  assertEquals(e1.retryable, false);
  assertStringIncludes(e1.message, "(error 406): inactive recipient");

  const limited = postmarkProvider({
    serverToken: "t",
    webhookSecret: SECRET,
    fetchFn: fakeFetch(() => new Response("", { status: 429 })),
  });
  assertEquals((await assertRejects(() => limited.send(outbound), SendError)).retryable, true);

  const oddOk = postmarkProvider({
    serverToken: "t",
    webhookSecret: SECRET,
    fetchFn: fakeFetch(() =>
      new Response(JSON.stringify({ ErrorCode: 300, Message: "invalid" }), { status: 200 })
    ),
  });
  assertEquals((await assertRejects(() => oddOk.send(outbound), SendError)).retryable, false);
});

Deno.test("fetch reads an inbound message's details through the same mapping", async () => {
  const p = postmarkProvider({
    serverToken: "t",
    webhookSecret: SECRET,
    fetchFn: fakeFetch((url) => {
      assertStringIncludes(url, "/messages/inbound/abc/details");
      return new Response(JSON.stringify({ ...payload(), MessageID: undefined }), { status: 200 });
    }),
  });
  const m = await p.fetch("abc");
  assertEquals(m.providerId, "abc");
  assertEquals(m.from, "alice@example.com");
});
