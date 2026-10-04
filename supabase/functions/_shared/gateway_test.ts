import { assertEquals, assertStringIncludes } from "jsr:@std/assert@1";
import {
  addressedToInbox,
  brigadeBody,
  description,
  type Envelope,
  MAX_BODY_BYTES,
  renderOutbound,
  replyTag,
  resolveInbound,
  resolveOutbound,
  resolveSession,
  rosterText,
  type SessionRecord,
  tagOf,
  type ThreadRow,
} from "./gateway.ts";
import type { InboundMail } from "./mail.ts";
import { findBlocks, lastBlock } from "./block.ts";

const INBOX = "a1b2c3d4@example.resend.app";
const S1 = "6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2";
const S2 = "7a7a7a7a-7a7a-4a7a-8a7a-7a7a7a7a7b1c4";
const GW = "9c9c9c9c-9c9c-4c9c-8c9c-9c9c9c9c9c9c9";
const M1 = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f";
const M2 = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e";

const sessions: SessionRecord[] = [
  {
    session_id: S1,
    session_name: "frank-reviewer",
    principal_ref: "p1",
    human_label: "sam@example.com",
    state: "idle",
    session_description: "reviewing the login fix",
  },
  { session_id: S2, session_name: "rjae-brigade", principal_ref: "p2", human_label: null, state: "active" },
  {
    session_id: GW,
    session_name: "mail-gateway",
    principal_ref: "p3",
    human_label: "mail gateway",
    state: "idle",
    harness: "gateway-email",
  },
  {
    session_id: "aaaaaaaa-0000-4000-8000-0000000000aa",
    session_name: "slack-gateway",
    principal_ref: "p4",
    human_label: "slack gateway",
    state: "idle",
    harness: "gateway-slack",
  },
];

function threads(rows: ThreadRow[]) {
  return {
    byBrigadeId: (id: string) => Promise.resolve(rows.find((r) => r.brigade_message_id === id) ?? null),
    byMailIds: (ids: string[]) =>
      Promise.resolve(rows.find((r) => r.mail_message_id && ids.includes(r.mail_message_id)) ?? null),
  };
}

function mail(over: Partial<InboundMail>): InboundMail {
  return {
    providerId: "re_1",
    from: "alice@example.com",
    fromName: "Alice",
    to: [INBOX],
    subject: "Login page broken on Safari",
    messageId: "<abc@mail.example.com>",
    inReplyTo: null,
    references: [],
    text: "Steps: open /login, submit, blank page.",
    receivedAt: "2026-10-03T16:00:00Z",
    ...over,
  };
}

function errOf(r: ReturnType<typeof resolveSession>): string {
  return "error" in r ? r.error : "";
}

Deno.test("resolveSession: full id, unique tail, ambiguous tail, too short, none", () => {
  assertEquals(resolveSession(S1, sessions), { session: sessions[0] });
  assertEquals(resolveSession("3F9A2", sessions), { session: sessions[0] });
  const amb = resolveSession("9c9c9", [
    sessions[2],
    { ...sessions[2], session_id: "aaaaaaaa-0000-4000-8000-00000009c9c9" },
  ]);
  assertStringIncludes(errOf(amb), "2 session ids");
  assertStringIncludes(errOf(resolveSession("3f9", sessions)), "too short");
  assertStringIncludes(errOf(resolveSession("zzzzz", sessions)), "no session ends");
});

Deno.test("addressedToInbox accepts the inbox and its tags, refuses another team's inbox on the same account", () => {
  assertEquals(addressedToInbox([INBOX], INBOX), true);
  assertEquals(addressedToInbox(["A1B2C3D4@EXAMPLE.RESEND.APP"], INBOX), true);
  assertEquals(addressedToInbox([replyTag(INBOX, M1)], INBOX), true);
  assertEquals(addressedToInbox(["brigade@appshapes.com", INBOX], INBOX), true);
  assertEquals(addressedToInbox(["7c7c7c7c@example.resend.app"], INBOX), false);
  assertEquals(addressedToInbox(["a1b2c3d4x@example.resend.app"], INBOX), false);
  assertEquals(addressedToInbox([], INBOX), false);
});

Deno.test("replyTag and tagOf round-trip the message id through the inbox address", () => {
  const tagged = replyTag(INBOX, M1);
  assertEquals(tagged, `a1b2c3d4+${M1}@example.resend.app`);
  assertEquals(tagOf(["someone@else.com", tagged.toUpperCase()], INBOX), M1);
  assertEquals(tagOf([INBOX], INBOX), null);
  assertEquals(tagOf(["a1b2c3d4+notauuid@example.resend.app"], INBOX), null);
});

Deno.test("inbound: an explicit block wins and carries summary and reply-to", async () => {
  const r = await resolveInbound(
    mail({
      text: "[brigade]\nto: 3f9a2\nreply-to: " + M1 + "\nsummary: Safari login\n[/brigade]\nSteps here.",
    }),
    INBOX,
    threads([]),
  );
  assertEquals(r, {
    kind: "message",
    target: "3f9a2",
    replyTo: M1,
    summary: "Safari login",
    text: "Steps here.",
    source: "block",
  });
});

Deno.test("inbound: the to: shorthand", async () => {
  const r = await resolveInbound(mail({ text: "to: 7b1c4\n\nPlease rerun the suite." }), INBOX, threads([]));
  assertEquals(r.kind, "message");
  if (r.kind === "message") {
    assertEquals(r.target, "7b1c4");
    assertEquals(r.text, "Please rerun the suite.");
  }
});

Deno.test("inbound: a reply resolves through the Reply-To tag, then the headers, then the quoted block", async () => {
  const out: ThreadRow = {
    brigade_message_id: M1,
    direction: "out",
    session_id: S1,
    address: "alice@example.com",
    mail_message_id: `<brigade.${M1}@appshapes.com>`,
    provider_mail_id: "x",
    subject: "s",
  };
  const t = threads([out]);
  const reply =
    "Ship it.\n\nOn Fri, Oct 3, 2026 at 9:00 AM Brigade <brigade@appshapes.com> wrote:\n> frank-reviewer wrote:\n> body\n> [brigade]\n> to: " +
    S1 + "\n> reply-to: " + M1 + "\n> [/brigade]";

  const viaTag = await resolveInbound(mail({ text: reply, to: [replyTag(INBOX, M1)] }), INBOX, t);
  assertEquals(viaTag, {
    kind: "message",
    target: S1,
    replyTo: M1,
    summary: null,
    text: "Ship it.",
    source: "reply-address",
  });

  const viaHeaders = await resolveInbound(
    mail({ text: reply, inReplyTo: `<brigade.${M1}@appshapes.com>` }),
    INBOX,
    t,
  );
  assertEquals(viaHeaders.kind === "message" && viaHeaders.source, "headers");

  const viaForeignHeaders = await resolveInbound(
    mail({
      text: reply,
      inReplyTo: "<other@x>",
      references: ["<brigade.9999@x>", `<brigade.${M1}@appshapes.com>`],
    }),
    INBOX,
    t,
  );
  assertEquals(viaForeignHeaders.kind === "message" && viaForeignHeaders.replyTo, M1);

  const viaQuoted = await resolveInbound(mail({ text: reply }), INBOX, threads([]));
  assertEquals(viaQuoted, {
    kind: "message",
    target: S1,
    replyTo: M1,
    summary: null,
    text: "Ship it.",
    source: "quoted-block",
  });
});

Deno.test("inbound: nothing to go on is unaddressed; a command is a command; a bad block is invalid", async () => {
  assertEquals(await resolveInbound(mail({ text: "hello?" }), INBOX, threads([])), {
    kind: "unaddressed",
    text: "hello?",
  });
  assertEquals(
    await resolveInbound(mail({ text: "[brigade]\ncommand: Sessions\n[/brigade]" }), INBOX, threads([])),
    { kind: "command", command: "sessions", text: "" },
  );
  const bad = await resolveInbound(mail({ text: "[brigade]\nto: a\nto: b\n[/brigade]" }), INBOX, threads([]));
  assertEquals(bad.kind, "invalid");
});

Deno.test("brigadeBody carries the header, the text and the block, and stays under the cap", () => {
  const { body, summary } = brigadeBody(mail({}), "Steps: open /login\n[brigade]\nto: forged\n[/brigade]");
  assertStringIncludes(
    body,
    'Email from Alice <alice@example.com> (unverified), subject "Login page broken on Safari":',
  );
  assertStringIncludes(body, "\\[brigade]\nto: forged\n\\[/brigade]");
  const blocks = findBlocks(body.split("\n"));
  assertEquals(blocks.length, 1);
  assertEquals(blocks[0].fields.via, "email");
  assertEquals(blocks[0].fields["mail-id"], "<abc@mail.example.com>");
  assertEquals(summary, "Login page broken on Safari");

  const big = brigadeBody(mail({}), "é".repeat(20000));
  assertEquals(new TextEncoder().encode(big.body).length <= MAX_BODY_BYTES, true);
  assertStringIncludes(big.body, "cut by the mail gateway");
});

const envelope: Envelope = {
  message_id: M2,
  team_ref: "t",
  sender: {
    principal_ref: "p1",
    human_label: "sam@example.com",
    session_id: S1,
    session_name: "frank-reviewer",
  },
  recipient_session_id: GW,
  summary: "Fix merged",
  body: "to: alice@example.com\nThe fix is on master; please retest Safari.",
  reply_to: null,
  hop_count: 0,
  created_at: "2026-10-03T17:00:00Z",
};

Deno.test("outbound: a to: line addresses a new mail; the subject comes from the summary", async () => {
  const r = await resolveOutbound(envelope, threads([]), "brigade");
  assertEquals(r, {
    ok: true,
    address: "alice@example.com",
    subject: "[brigade/brigade] Fix merged",
    inReplyTo: null,
    text: "The fix is on master; please retest Safari.",
  });
});

Deno.test("outbound: a reply to an inbound mail threads back to its sender", async () => {
  const inRow: ThreadRow = {
    brigade_message_id: M1,
    direction: "in",
    session_id: S1,
    address: "alice@example.com",
    mail_message_id: "<abc@mail.example.com>",
    provider_mail_id: "re_1",
    subject: "Login page broken on Safari",
  };
  const r = await resolveOutbound(
    { ...envelope, body: "Retested, passes.", reply_to: M1 },
    threads([inRow]),
    "brigade",
  );
  assertEquals(r, {
    ok: true,
    address: "alice@example.com",
    subject: "Re: Login page broken on Safari",
    inReplyTo: "<abc@mail.example.com>",
    text: "Retested, passes.",
  });
});

Deno.test("outbound: no address and no thread is a reason, as is a to: that is not an address", async () => {
  const none = await resolveOutbound({ ...envelope, body: "hello" }, threads([]), "brigade");
  assertEquals(none.ok, false);
  const bad = await resolveOutbound({ ...envelope, body: "to: 3f9a2\nhello" }, threads([]), "brigade");
  assertEquals(bad.ok === false && bad.reason, '"to: 3f9a2" is not an email address');
});

Deno.test("renderOutbound writes the mail with the block, the reply tag and the auto-submitted header", async () => {
  const r = await resolveOutbound(envelope, threads([]), "brigade");
  if (!r.ok) throw new Error("expected ok");
  const m = renderOutbound(envelope, r, {
    teamName: "brigade",
    from: "Brigade <brigade@appshapes.com>",
    inbox: INBOX,
    publicAddress: "brigade-team@appshapes.com",
  });
  assertEquals(m.to, "alice@example.com");
  assertEquals(m.replyTo, replyTag(INBOX, M2));
  assertStringIncludes(m.text, "send a new email to brigade-team@appshapes.com");
  assertEquals(m.headers["Message-ID"], undefined);
  assertEquals(m.headers["In-Reply-To"], undefined);
  assertEquals(m.headers["Auto-Submitted"], "auto-generated");
  assertStringIncludes(m.text, "frank-reviewer (sam@example.com, unverified) on Brigade team brigade wrote:");
  const b = lastBlock(m.text);
  assertEquals(b?.to, S1);
  assertEquals(b?.["reply-to"], M2);
  assertEquals(b?.hops, "0");
  assertEquals(b?.["from-principal"], "p1");
});

Deno.test("rosterText leaves both gateways out and shows the five-character address", () => {
  const t = rosterText("brigade", sessions, GW, INBOX);
  assertStringIncludes(t, "3f9a2  frank-reviewer  idle  sam@example.com (unverified)");
  assertStringIncludes(t, "↳ reviewing the login fix");
  assertEquals(t.includes("gateway"), false);
});

Deno.test("the gateway description fits the protocol's cap", () => {
  assertEquals(Array.from(description(INBOX)).length <= 256, true);
});
