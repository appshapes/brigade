import { assertEquals, assertStringIncludes } from "jsr:@std/assert@1";
import {
  addressedToInbox,
  brigadeBody,
  description,
  type Envelope,
  gatewayForMail,
  MAX_BODY_BYTES,
  renderOutbound,
  replyTag,
  resolveInbound,
  resolveOutbound,
  resolveRecipient,
  resolveRecipientAcross,
  rosterText,
  senderAllowed,
  type SessionRecord,
  tagOf,
  type TeamSessions,
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

function errOf(r: ReturnType<typeof resolveRecipient>): string {
  return "error" in r ? r.error : "";
}

function pick(r: ReturnType<typeof resolveRecipient>): { id: string; how: string; others: string[] } {
  if ("error" in r) throw new Error(r.error);
  return { id: r.session.session_id, how: r.how, others: r.others.map((s) => s.session_id) };
}

Deno.test("resolveRecipient: full id, unique tail, ambiguous tail, too short, none", () => {
  assertEquals(pick(resolveRecipient(S1, sessions)), { id: S1, how: "id", others: [] });
  assertEquals(pick(resolveRecipient("3F9A2", sessions)), { id: S1, how: "tail", others: [] });
  const amb = resolveRecipient("9c9c9", [
    sessions[2],
    { ...sessions[2], session_id: "aaaaaaaa-0000-4000-8000-00000009c9c9" },
  ]);
  assertStringIncludes(errOf(amb), "2 session ids");
  assertStringIncludes(errOf(resolveRecipient("3f9", sessions)), "too short");
  assertStringIncludes(errOf(resolveRecipient("3f9", sessions)), "no member has that label");
  assertStringIncludes(errOf(resolveRecipient("zzzzz", sessions)), "no session ends");
  assertStringIncludes(errOf(resolveRecipient("", sessions)), "no session or member");
});

const sam = (over: Partial<SessionRecord>): SessionRecord => ({
  session_id: "cccccccc-0000-4000-8000-0000000000c1",
  session_name: "sam-one",
  principal_ref: "9f3c1a20-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
  human_label: "Sam@Example.com",
  state: "idle",
  last_seen_at: "2026-10-15T10:00:00Z",
  ...over,
});

Deno.test("resolveRecipient: a member's label picks their best session, active before idle before offline", () => {
  const idle = sam({});
  const active = sam({
    session_id: "cccccccc-0000-4000-8000-0000000000c2",
    session_name: "sam-two",
    state: "active",
  });
  const offline = sam({
    session_id: "cccccccc-0000-4000-8000-0000000000c3",
    session_name: "sam-old",
    state: "offline",
  });
  // sessions[0] carries the same label under another principal; the rest are the unlabelled one and the gateways.
  const r = pick(resolveRecipient("sam@example.com", [offline, idle, active, ...sessions.slice(1)]));
  assertEquals(r, { id: active.session_id, how: "label", others: [idle.session_id, offline.session_id] });
  // Among equals, the most recently seen.
  const older = sam({
    session_id: "cccccccc-0000-4000-8000-0000000000c4",
    last_seen_at: "2026-10-15T09:00:00Z",
  });
  assertEquals(pick(resolveRecipient("sam@example.com", [older, idle])).id, idle.session_id);
  // Every session offline: the most recently seen one, and the message waits.
  const r2 = resolveRecipient("sam@example.com", [offline, { ...older, state: "offline" }]);
  assertEquals(pick(r2).id, offline.session_id);
  if ("error" in r2) throw new Error(r2.error);
  assertEquals(r2.who, "sam@example.com");
});

Deno.test("resolveRecipient: refusing sessions and gateways are never picked; a shared label is refused", () => {
  const refusing = sam({ inbound: "refuse", state: "active" });
  const holding = sam({ session_id: "cccccccc-0000-4000-8000-0000000000c5", inbound: "hold" });
  assertEquals(pick(resolveRecipient("sam@example.com", [refusing, holding])).id, holding.session_id);
  assertStringIncludes(errOf(resolveRecipient("sam@example.com", [refusing])), "can receive");
  assertStringIncludes(errOf(resolveRecipient("mail gateway", sessions)), "can receive");
  const impostor = sam({
    session_id: "dddddddd-0000-4000-8000-0000000000d1",
    principal_ref: "0b0b0b0b-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
    state: "active",
  });
  const shared = errOf(resolveRecipient("sam@example.com", [sam({}), impostor]));
  assertStringIncludes(shared, "2 members");
  assertStringIncludes(shared, "9f3c1a20, 0b0b0b0b");
});

Deno.test("resolveRecipient: eight or more characters of a principal reference name the member", () => {
  const one = sam({});
  const two = sam({ session_id: "cccccccc-0000-4000-8000-0000000000c2", state: "active", human_label: null });
  assertEquals(pick(resolveRecipient("9F3C1A20", [one, two, ...sessions])), {
    id: two.session_id,
    how: "principal",
    others: [one.session_id],
  });
  assertStringIncludes(errOf(resolveRecipient("9f3c1a2", [one])), "no session ends");
  // A session's name is never an address.
  assertStringIncludes(errOf(resolveRecipient("frank-reviewer", sessions)), "no member has that label");
});

// Several teams on one gateway (card 128). Alpha carries the shared fixture's two people; Beta carries Sam under
// the second principal, as a person on two teams is (one credential per team, so one principal per team).
const alpha: TeamSessions = { teamId: "t-alpha", teamName: "Alpha", sessions: sessions.slice(0, 2) };
const bob = sam({ human_label: "bob@example.com", principal_ref: "b0b0b0b0-bbbb-4bbb-8bbb-bbbbbbbbbbbb" });

function pickAcross(r: ReturnType<typeof resolveRecipientAcross>) {
  if ("error" in r) throw new Error(r.error);
  return {
    id: r.session.session_id,
    how: r.how,
    team: r.teamName,
    others: r.others.map((s) => s.session_id),
  };
}

Deno.test("resolveRecipientAcross: an id or a tail is found on whichever team has it; a label on one team is that team's", () => {
  const beta: TeamSessions = { teamId: "t-beta", teamName: "Beta", sessions: [bob] };
  assertEquals(pickAcross(resolveRecipientAcross(S1, [alpha, beta])), {
    id: S1,
    how: "id",
    team: "Alpha",
    others: [],
  });
  assertEquals(pickAcross(resolveRecipientAcross("3F9A2", [alpha, beta])), {
    id: S1,
    how: "tail",
    team: "Alpha",
    others: [],
  });
  assertEquals(pickAcross(resolveRecipientAcross("000c1", [alpha, beta])), {
    id: bob.session_id,
    how: "tail",
    team: "Beta",
    others: [],
  });
  assertEquals(pickAcross(resolveRecipientAcross("Bob@Example.com", [alpha, beta])), {
    id: bob.session_id,
    how: "label",
    team: "Beta",
    others: [],
  });
  assertEquals(pickAcross(resolveRecipientAcross("b0b0b0b0", [alpha, beta])), {
    id: bob.session_id,
    how: "principal",
    team: "Beta",
    others: [],
  });
  // A tail that ends an id on each team is refused with both ids, as inside one team.
  const twin = { ...sessions[2], session_id: "aaaaaaaa-0000-4000-8000-00000009c9c9" };
  const amb = resolveRecipientAcross("9c9c9", [{ ...alpha, sessions: [sessions[2]] }, {
    ...beta,
    sessions: [twin],
  }]);
  assertStringIncludes(errOf(amb), "2 session ids");
  assertStringIncludes(errOf(amb), twin.session_id);
  // Nobody of that name, anywhere.
  assertStringIncludes(errOf(resolveRecipientAcross("zzzzz", [alpha, beta])), "no session ends");
  assertStringIncludes(errOf(resolveRecipientAcross("3f9", [alpha, beta])), "too short");
  assertStringIncludes(errOf(resolveRecipientAcross("", [alpha, beta])), "no session or member");
  assertStringIncludes(
    errOf(resolveRecipientAcross("frank-reviewer", [alpha, beta])),
    "no member has that label",
  );
});

Deno.test("resolveRecipientAcross: a label on two teams is answered with each team's best sessions by tail; inside one team the rules are unchanged", () => {
  const beta: TeamSessions = {
    teamId: "t-beta",
    teamName: "Beta",
    sessions: [
      sam({ state: "active" }),
      sam({ session_id: "cccccccc-0000-4000-8000-0000000000c2", session_name: "sam-two", state: "offline" }),
      sam({ session_id: "cccccccc-0000-4000-8000-0000000000c3", session_name: "sam-three", state: "idle" }),
      sam({ session_id: "cccccccc-0000-4000-8000-0000000000c4", session_name: "sam-four", state: "offline" }),
      sam({
        session_id: "cccccccc-0000-4000-8000-0000000000c5",
        session_name: "sam-quiet",
        inbound: "refuse",
      }),
    ],
  };
  // Alpha's frank-reviewer carries the label under principal p1; Beta's Sam under another: two teams, the person
  // picks by tail. The best three of a team are listed, state first; a refusing session is not a candidate.
  const two = errOf(resolveRecipientAcross("sam@example.com", [beta, alpha]));
  assertStringIncludes(two, '"sam@example.com" is a member of 2 teams');
  assertStringIncludes(two, "Alpha: 3f9a2 frank-reviewer (idle)");
  assertStringIncludes(two, "Beta: 000c1 sam-one (active), 000c3 sam-three (idle), 000c2 sam-two (offline)");
  assertEquals(two.includes("sam-four"), false);
  assertEquals(two.includes("sam-quiet"), false);
  // A team whose member cannot receive is still named, so the person knows where the label lives.
  const quiet = errOf(
    resolveRecipientAcross("sam@example.com", [alpha, { ...beta, sessions: [beta.sessions[4]] }]),
  );
  assertStringIncludes(quiet, "Beta: no session can receive a message");
  // On one team, the member's best session with the others, and the team carried.
  const delta: TeamSessions = { teamId: "t-delta", teamName: "Delta", sessions: [sessions[1]] };
  assertEquals(pickAcross(resolveRecipientAcross("sam@example.com", [beta, delta])), {
    id: beta.sessions[0].session_id,
    how: "label",
    team: "Beta",
    others: [beta.sessions[2].session_id, beta.sessions[1].session_id, beta.sessions[3].session_id],
  });
  // Two principals sharing a label inside one team: refused with the principal prefixes, as before.
  const gamma: TeamSessions = { teamId: "t-gamma", teamName: "Gamma", sessions: [sessions[0], sam({})] };
  const shared = errOf(resolveRecipientAcross("sam@example.com", [gamma, delta]));
  assertStringIncludes(shared, "2 members");
  assertStringIncludes(shared, "p1, 9f3c1a20");
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

Deno.test("rosterText shows the sessions online now: both gateways and offline sessions left out", () => {
  const offline: SessionRecord = {
    session_id: "bbbbbbbb-0000-4000-8000-0000000000bb",
    session_name: "gone-home",
    principal_ref: "p5",
    human_label: "pat@example.com",
    state: "offline",
  };
  const t = rosterText("brigade", [...sessions, offline], GW, INBOX);
  assertStringIncludes(t, "or a member's label");
  assertStringIncludes(t, "3f9a2  frank-reviewer  idle  sam@example.com (unverified)");
  assertStringIncludes(t, "↳ reviewing the login fix");
  assertEquals(t.includes("gateway"), false);
  assertEquals(t.includes("gone-home"), false);
  assertStringIncludes(rosterText("brigade", [offline], GW, INBOX), "(none)");
});

Deno.test("the gateway description fits the protocol's cap", () => {
  assertEquals(Array.from(description(INBOX)).length <= 256, true);
});

Deno.test("gatewayForMail picks the gateway whose inbox the mail reached, tags included", () => {
  const gws = [{ inbox: INBOX, team: "brigade" }, { inbox: "other@example.resend.app", team: "telder" }];
  assertEquals(gatewayForMail(gws, ["someone@example.com", "other@example.resend.app"])?.team, "telder");
  assertEquals(gatewayForMail(gws, [replyTag(INBOX, M1)])?.team, "brigade");
  assertEquals(gatewayForMail(gws, ["nobody@example.com"]), null);
  assertEquals(gatewayForMail([], [INBOX]), null);
});

Deno.test("a mail that says only sessions, in its text or its subject, is the sessions command", async () => {
  const t = threads([]);
  const bodyOnly = await resolveInbound(mail({ text: "sessions\n", subject: "anything" }), INBOX, t);
  assertEquals(bodyOnly.kind, "command");
  if (bodyOnly.kind === "command") assertEquals(bodyOnly.command, "sessions");
  const prefixed = await resolveInbound(mail({ text: "Brigade sessions", subject: "x" }), INBOX, t);
  assertEquals(prefixed.kind === "command" && prefixed.command, "sessions");
  const subjectOnly = await resolveInbound(mail({ text: "", subject: "help" }), INBOX, t);
  assertEquals(subjectOnly.kind === "command" && subjectOnly.command, "help");
  const quoted = await resolveInbound(
    mail({ text: "sessions\n\nOn Sat, Oct 4, 2026 Brigade wrote:\n> hi", subject: "Re: x" }),
    INBOX,
    t,
  );
  assertEquals(quoted.kind, "command", "the quoted part does not count");
  const prose = await resolveInbound(
    mail({ text: "sessions are slow today", subject: "sessions" }),
    INBOX,
    t,
  );
  assertEquals(prose.kind, "unaddressed");
});

Deno.test("header lines address a mail and carry a summary", async () => {
  const r = await resolveInbound(
    mail({
      text: "to: 3f9a2\nsummary: Login blank on Safari 18\n\nSteps: open /login, sign in.\n",
      subject: "x",
    }),
    INBOX,
    threads([]),
  );
  assertEquals(r.kind, "message");
  if (r.kind === "message") {
    assertEquals(r.target, "3f9a2");
    assertEquals(r.summary, "Login blank on Safari 18");
    assertEquals(r.text, "Steps: open /login, sign in.");
  }
});

Deno.test("senderAllowed: an empty list is open; a listed address or @domain gets in; nobody else does", () => {
  assertEquals(senderAllowed("anyone@stranger.example", []), true);
  assertEquals(senderAllowed("anyone@stranger.example", null), true);
  assertEquals(senderAllowed("anyone@stranger.example", [" ", ""]), true);
  const list = ["QA@Example.com", "@AppShapes.com", ""];
  assertEquals(senderAllowed("qa@example.com", list), true);
  assertEquals(senderAllowed("Rjae@AppShapes.com", list), true);
  assertEquals(senderAllowed("dev@example.com", list), false, "the address was listed, not its domain");
  assertEquals(senderAllowed("rjae@mail.appshapes.com", list), false, "the domain exactly, not a subdomain");
  assertEquals(senderAllowed("x@appshapes.com.evil.example", list), false);
  assertEquals(senderAllowed("@appshapes.com", list), false, "no local part is no address");
  assertEquals(senderAllowed("", list), false);
});
