// The two inbound handlers run against the local stack's database (work item 388): mail-in and slack-in as they
// are served, with a stand-in for the connector and for Slack's API and nothing else stood in. What is held here
// is what a unit test cannot hold: that a person's message whose delivery failed is carried by the provider's
// next delivery, with the marks as the database keeps them.
//
// It needs the stack (`make supabase-start supabase-env`) and is run by `make test-integration-functions`, which
// gives it the stack's database address; `deno task test` does not see this directory.

import { assert, assertEquals, assertStringIncludes } from "jsr:@std/assert@1";
import postgres from "npm:postgres@3.4.9";
import {
  asMember,
  BEING_CARRIED,
  CARRYING_SECONDS,
  claim,
  done,
  release,
  type Sql,
  take,
} from "../_shared/db.ts";
import { mailInHandler } from "../_shared/mail_in_serve.ts";
import { signSlack } from "../_shared/slack.ts";
import { slackInHandler } from "../_shared/slack_in_serve.ts";

// The stack's database and no other: the address `make supabase-env` wrote, never one this shell happens to
// hold for a hosted project under the name the functions read. These tests write users, teams and gateways.
const DB = Deno.env.get("SUPABASE_DB_URL") ?? "";
if (!DB) throw new Error("the local stack's database address is not set: run this through make");
if (!["127.0.0.1", "localhost", "[::1]"].includes(new URL(DB).hostname)) {
  throw new Error(
    "SUPABASE_DB_URL does not name a database on this machine: these tests run on the local stack",
  );
}
Deno.env.set("BRIGADE_DB_URL", DB);

const CONNECTOR_SECRET = "the-test-connector-secret";
const SIGNING_SECRET = "the-test-signing-secret";
Deno.env.set("BRIGADE_MAIL_CONNECTOR_SECRET", CONNECTOR_SECRET);
Deno.env.set("BRIGADE_SLACK_SIGNING_SECRET", SIGNING_SECRET);
Deno.env.set("BRIGADE_SLACK_BOT_TOKEN", "xoxb-test");

/** One team with a gateway of one kind and one member's session, written as the installers write them. */
interface World {
  sql: Sql;
  tag: string;
  team: string;
  creator: string;
  gateway: string;
  /** The session a person writes to. */
  worker: string;
  inbox: string;
  /** Every user and team this world made, removed again by close. */
  close(): Promise<void>;
  /** The team's creator evicts everybody who joined with the first secret: the gateway member and the member. */
  evict(): Promise<void>;
  /** What running the gateway's installer again does: the member is active again, with a session of its own. */
  install(): Promise<void>;
  /** The member joins again and starts a session named as the one before. */
  rejoin(): Promise<void>;
  rows(id: string): Promise<string[]>;
  messages(): Promise<{ id: string; body: string }[]>;
}

async function world(kind: "email" | "slack"): Promise<World> {
  const sql: Sql = postgres(DB, { prepare: false, max: 1, idle_timeout: 5, onnotice: () => {} });
  const tag = crypto.randomUUID().slice(0, 8);
  // What a run that was stopped left behind: a Slack gateway of its team would be read by every test after it.
  await sql`delete from brigade.teams where name like 'functions-live-%' and created_at < now() - interval '2 minutes'`;
  const user = async () =>
    (await sql`insert into auth.users (id, aud, role, is_anonymous, created_at)
               values (gen_random_uuid(), 'authenticated', 'authenticated', true, now()) returning id`)[0]
      .id as string;
  const creator = await user();
  const gateway = await user();
  const member = await user();
  const created = await asMember<Sql>(
    sql,
    creator,
    (tx) => tx`select brigade.create_team(${"functions-live-" + tag}, 'creator') as r`,
  );
  const team = created[0].r.team_id as string;
  const inbox = `brigade-${tag}@in.example.com`;
  const w: World = {
    sql,
    tag,
    team,
    creator,
    gateway,
    worker: "",
    inbox,
    async close() {
      // What an eviction broadcast to the sessions it closed: left behind, these rows fail a database test that
      // counts them on a database it does not own (work item 58).
      await sql`delete from realtime.messages
                 where topic in (select 'brigade:session:' || id from brigade.sessions where team_id = ${team}::uuid)`;
      await sql`delete from brigade.teams where id = ${team}::uuid`;
      await sql`delete from auth.users where id = any(${[creator, gateway, member]}::uuid[])`;
      await sql`delete from brigade_gateway.received where provider_mail_id like ${"%" + tag + "%"}`;
      await sql.end({ timeout: 2 });
    },
    async evict() {
      const r = await asMember<Sql>(
        sql,
        creator,
        (tx) => tx`select brigade.revoke_memberships_by_version(${team}::uuid, 1) as r`,
      );
      assertEquals(r[0].r.revoked, 2, "the eviction revokes the gateway member and the member");
    },
    async install() {
      // scripts/gateway-install.sh and scripts/slack-gateway-install.sh, the SQL block of each.
      await sql`insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version)
                values (${team}::uuid, ${gateway}::uuid, 'gateway', 1)
                on conflict (team_id, user_id) do update set status = 'active', revoked_at = null`;
      const s = await asMember<Sql>(
        sql,
        gateway,
        (tx) =>
          tx`select brigade.register_session(${team}::uuid, ${
            kind + "-gateway"
          }, 'a gateway', 'idle', 'accept',
                                           ${"gateway-" + kind}, '0.1.0', null, 600, null) as r`,
      );
      const session = s[0].r.session_id as string;
      if (kind === "email") {
        await sql`insert into brigade_gateway.gateways
                    (team_id, user_id, session_id, kind, inbox, public_address, from_address, provider, send_url)
                  values (${team}::uuid, ${gateway}::uuid, ${session}::uuid, 'email', ${inbox}, ${inbox},
                          ${"Brigade <" + inbox + ">"}, 'resend', 'https://connector.invalid/send')
                  on conflict (team_id, kind) do update set session_id = excluded.session_id`;
      } else {
        await sql`insert into brigade_gateway.gateways (team_id, user_id, session_id, kind)
                  values (${team}::uuid, ${gateway}::uuid, ${session}::uuid, 'slack')
                  on conflict (team_id, kind) do update set session_id = excluded.session_id`;
      }
    },
    async rejoin() {
      await sql`insert into brigade.memberships (team_id, user_id, human_label, joined_secret_version)
                values (${team}::uuid, ${member}::uuid, 'pat', 1)
                on conflict (team_id, user_id) do update set status = 'active', revoked_at = null`;
      const s = await asMember<Sql>(
        sql,
        member,
        (tx) =>
          tx`select brigade.register_session(${team}::uuid, ${"worker-" + tag}, null, 'idle', 'accept',
                                           'claude-code', '0.57.0', null, 600, null) as r`,
      );
      w.worker = s[0].r.session_id as string;
    },
    async rows(id: string) {
      const rows = await sql`select provider_mail_id from brigade_gateway.received
                              where provider_mail_id = ${id} or provider_mail_id = 'carrying:' || md5(${id})
                              order by 1`;
      return rows.map((r: { provider_mail_id: string }) => r.provider_mail_id === id ? "done" : "carrying");
    },
    async messages() {
      const rows =
        await sql`select id, body from brigade.messages where recipient_session_id = ${w.worker}::uuid
                              order by created_at`;
      return rows as { id: string; body: string }[];
    },
  };
  await w.install();
  await w.rejoin();
  return w;
}

/** The connector's send endpoint, stood in: what was mailed, and a gate a test may hold a send at. */
function connector() {
  const sent: { to: string; subject: string; text: string }[] = [];
  let gate: Promise<void> | null = null;
  let arrived: (() => void) | null = null;
  const fetchFn = (async (_url: string | URL | Request, init?: RequestInit) => {
    const mail = JSON.parse(String(init?.body));
    arrived?.();
    if (gate) await gate;
    sent.push({ to: mail.to, subject: mail.subject, text: mail.text });
    return new Response(JSON.stringify({ id: "sent-" + sent.length }), { status: 200 });
  }) as typeof fetch;
  return {
    sent,
    fetchFn,
    /** hold makes the next send wait: `reached` settles when it has arrived, `let_go` lets it through. */
    hold() {
      let open = () => {};
      gate = new Promise<void>((resolve) => (open = resolve));
      const reached = new Promise<void>((resolve) => (arrived = resolve));
      return { reached, let_go: () => (gate = null, open()) };
    },
  };
}

function mail(w: World, id: string, text: string, subject = "deploy?", from = "pat@example.com"): Request {
  return new Request("http://localhost/mail-in", {
    method: "POST",
    headers: { Authorization: `Bearer ${CONNECTOR_SECRET}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      brigade_mail: 1,
      id,
      from,
      from_name: "Pat Example",
      to: [w.inbox],
      subject,
      message_id: `<${id}@mail.example.com>`,
      text,
      received_at: new Date().toISOString(),
    }),
  });
}

async function answered(handler: (req: Request) => Promise<Response>, req: Request) {
  const r = await handler(req);
  return { status: r.status, said: await r.json() as Record<string, unknown> };
}

Deno.test("mail-in: a mail whose delivery failed is carried by the provider's next delivery", async () => {
  const w = await world("email");
  const c = connector();
  const handle = mailInHandler({ fetchFn: c.fetchFn });
  const id = `mail-${w.tag}-1`;
  const letter = () => mail(w, id, `to: ${w.worker}\n\nshall we deploy today?`);
  try {
    // The standing way to meet a failure after the mail is taken: the join secret was rotated and everybody who
    // joined with the old one evicted, the gateway member among them. Its read of the roster is refused.
    await w.evict();
    const failed = await answered(handle, letter());
    assertEquals(failed.status, 500, JSON.stringify(failed.said));
    assertStringIncludes(String(failed.said.error), "brigade:unauthorized");
    assertEquals(await w.rows(id), [], "a failed delivery leaves neither the mark nor its claim");

    // The installer is run again and the member is back; the provider delivers the mail again.
    await w.install();
    await w.rejoin();
    const carried = await answered(handle, letter());
    assertEquals(carried.status, 200, JSON.stringify(carried.said));
    assertEquals(carried.said.to, w.worker);
    assertEquals(carried.said.duplicate, false);
    const held = await w.messages();
    assertEquals(held.length, 1);
    assertEquals(held[0].id, carried.said.message_id);
    assertStringIncludes(held[0].body, "shall we deploy today?");
    assertEquals(await w.rows(id), ["done"]);

    // And only now is a delivery of it a duplicate.
    assertEquals(await answered(handle, letter()), { status: 200, said: { duplicate: true } });
    assertEquals((await w.messages()).length, 1);
    assertEquals(c.sent, [], "nobody was mailed anything");
  } finally {
    await w.close();
  }
});

Deno.test("mail-in: a delivery that comes while the mail is being carried is told to come again", async () => {
  const w = await world("email");
  const c = connector();
  const handle = mailInHandler({ fetchFn: c.fetchFn });
  const id = `mail-${w.tag}-2`;
  const letter = () => mail(w, id, "nobody is named here");
  try {
    const bounce = c.hold();
    const first = answered(handle, letter());
    await bounce.reached;
    assertEquals(await w.rows(id), ["carrying"]);
    const second = await answered(handle, letter());
    assertEquals(second, { status: 500, said: { error: BEING_CARRIED } });
    bounce.let_go();
    assertEquals(await first, { status: 200, said: { bounced: "unaddressed" } });
    assertEquals(await w.rows(id), ["done"]);
    assertEquals(await answered(handle, letter()), { status: 200, said: { duplicate: true } });
    assertEquals(c.sent.length, 1, "the person was answered once");
    assertEquals(c.sent[0].to, "pat@example.com");
  } finally {
    await w.close();
  }
});

Deno.test("mail-in: a claim that a cut-off delivery left ends by itself", async () => {
  const w = await world("email");
  const handle = mailInHandler({ fetchFn: connector().fetchFn });
  const id = `mail-${w.tag}-3`;
  const letter = () => mail(w, id, `to: ${w.worker}\n\nstill there?`);
  const claimed = (seconds: number) =>
    w.sql`insert into brigade_gateway.received (provider_mail_id, received_at)
          values ('carrying:' || md5(${id}), now() - make_interval(secs => ${seconds}))
          on conflict (provider_mail_id) do update set received_at = excluded.received_at`;
  try {
    await claimed(CARRYING_SECONDS - 5);
    assertEquals((await answered(handle, letter())).status, 500);
    assertEquals(
      await w.rows(id),
      ["carrying"],
      "a delivery that was told to come again takes no claim away",
    );
    await claimed(CARRYING_SECONDS + 1);
    const carried = await answered(handle, letter());
    assertEquals(carried.status, 200, JSON.stringify(carried.said));
    assertEquals(await w.rows(id), ["done"]);
    assertEquals((await w.messages()).length, 1);
  } finally {
    await w.close();
  }
});

Deno.test("mail-in: a mail that was sent on and not written down is carried once, whatever it says now", async () => {
  const w = await world("email");
  const c = connector();
  const handle = mailInHandler({ fetchFn: c.fetchFn });
  const id = `mail-${w.tag}-4`;
  const unmark = () => w.sql`delete from brigade_gateway.received where provider_mail_id = ${id}`;
  try {
    const first = await answered(handle, mail(w, id, `to: ${w.worker}\n\nfirst words`));
    assertEquals(first.status, 200, JSON.stringify(first.said));
    // The delivery was cut off between the send and the mark: Brigade holds the message, the mark is missing.
    await unmark();
    const again = await answered(handle, mail(w, id, `to: ${w.worker}\n\nfirst words`));
    assertEquals(again.status, 200);
    assertEquals([again.said.message_id, again.said.duplicate], [first.said.message_id, true]);
    // The same with other words, which Brigade refuses under the mail's key: the row says which message it is.
    await unmark();
    const other = await answered(
      handle,
      mail(w, id, `to: ${w.worker}\n\nfirst words`, "another subject"),
    );
    assertEquals(other, {
      status: 200,
      said: { message_id: first.said.message_id, to: w.worker, duplicate: true },
    });
    // And with the row gone too: Brigade holds the message all the same.
    await unmark();
    await w.sql`delete from brigade_gateway.threads where provider_mail_id = ${id}`;
    const bare = await answered(handle, mail(w, id, `to: ${w.worker}\n\nfirst words`, "another subject"));
    assertEquals(bare, { status: 200, said: { duplicate: true } });
    assertEquals(await w.rows(id), ["done"]);
    assertEquals((await w.messages()).length, 1);
    assertEquals(c.sent, [], "nobody was told that a mail which arrived was refused");
  } finally {
    await w.close();
  }
});

Deno.test("mail-in: a mail that is not the gateway's to carry is not written down", async () => {
  const w = await world("email");
  const handle = mailInHandler({ fetchFn: connector().fetchFn });
  try {
    await w.sql`update brigade_gateway.gateways set allowed_senders = array['@example.org']
                 where team_id = ${w.team}::uuid`;
    const stranger = await answered(handle, mail(w, `mail-${w.tag}-5`, "to: anyone\n\nhello"));
    assertEquals(stranger.status, 200);
    assert(typeof stranger.said.ignored === "string", JSON.stringify(stranger.said));
    assertEquals(await w.rows(`mail-${w.tag}-5`), []);
    assertEquals((await w.messages()).length, 0);
  } finally {
    await w.close();
  }
});

Deno.test("mail-in: the gateway answers none of its own addresses, by a bounce or by a command's reply", async () => {
  // Work item 271. The usual setup: people write to an address of the team's own, which forwards to the inbox,
  // and the gateway's mail is from that address. A bounce to it would come back and be bounced again, without end.
  const w = await world("email");
  const c = connector();
  const handle = mailInHandler({ fetchFn: c.fetchFn });
  const team = `team-${w.tag}@example.com`;
  const bot = `bot-${w.tag}@example.com`;
  try {
    await w.sql`update brigade_gateway.gateways set public_address = ${team}, from_address = ${
      "Brigade <" + bot + ">"
    }
                 where team_id = ${w.team}::uuid`;
    const own = [team, bot, w.inbox, w.inbox.replace("@", "+r.0123@")];
    for (const [n, from] of own.entries()) {
      const bounced = await answered(
        handle,
        mail(w, `mail-${w.tag}-8-${n}`, "nobody is named here", "x", from),
      );
      assertEquals(bounced, { status: 200, said: { bounced: "unaddressed" } }, from);
      const asked = await answered(handle, mail(w, `mail-${w.tag}-9-${n}`, "sessions", "x", from));
      assertEquals(asked, { status: 200, said: { command: "sessions" } }, from);
    }
    assertEquals(c.sent, [], "nothing was mailed to an address of the gateway's own");
    // A person is answered as before, by both.
    await answered(handle, mail(w, `mail-${w.tag}-8-p`, "nobody is named here"));
    await answered(handle, mail(w, `mail-${w.tag}-9-p`, "sessions"));
    assertEquals(c.sent.map((m) => m.to), ["pat@example.com", "pat@example.com"]);
  } finally {
    await w.close();
  }
});

Deno.test("the marks: a delivery that looked before another finished does not carry the message again", async () => {
  const w = await world("email");
  const id = `mail-${w.tag}-6`;
  try {
    const first = await take(w.sql, id);
    assert(typeof first === "object", "the first delivery takes the message");
    // A second delivery looks for the mark now and finds none; the first finishes; the second goes on.
    await done(w.sql, id, first.token);
    assertEquals(await take(w.sql, id), "done");
    assertEquals(await w.rows(id), ["done"], "and the claim it took on the way is given back");
  } finally {
    await w.close();
  }
});

Deno.test("the marks: a delivery that outlived its claim does not take away the claim of the one after it", async () => {
  const w = await world("email");
  const id = `mail-${w.tag}-7`;
  try {
    const slow = await claim(w.sql, id);
    assert(slow !== null);
    assertEquals(await claim(w.sql, id), null, "a claim that holds is nobody else's to take");
    await w.sql`update brigade_gateway.received set received_at = now() - make_interval(secs => ${
      CARRYING_SECONDS + 1
    }) where provider_mail_id = 'carrying:' || md5(${id})`;
    const next = await claim(w.sql, id);
    assert(next !== null && next !== slow, "the claim that ended is taken over, under a token of its own");
    await release(w.sql, id, slow);
    assertEquals(await w.rows(id), ["carrying"], "the slow delivery's failure leaves the next one's claim");
    await done(w.sql, id, slow);
    assertEquals(
      await w.rows(id),
      ["carrying", "done"],
      "and its end writes the mark and leaves that claim too",
    );
    await done(w.sql, id, next);
    assertEquals(await w.rows(id), ["done"]);
  } finally {
    await w.close();
  }
});

/** Slack's API, stood in: what was posted, and a gate a test may hold a post at. */
function slack() {
  const posted: { method: string; text: string }[] = [];
  let gate: Promise<void> | null = null;
  let arrived: (() => void) | null = null;
  const fetchFn = (async (url: string | URL | Request, init?: RequestInit) => {
    const method = String(url).split("/api/")[1].split("?")[0];
    let said: Record<string, unknown> = { ok: true };
    if (method === "auth.test") said = { ok: true, user_id: "UBOT", team: "T1", url: "https://t.slack.com/" };
    if (method === "users.info") said = { ok: true, user: { id: "UPAT", name: "pat" } };
    if (method === "chat.getPermalink") said = { ok: true, permalink: "https://t.slack.com/archives/D1/p1" };
    if (method === "chat.postMessage" || method === "chat.postEphemeral") {
      arrived?.();
      if (gate) await gate;
      posted.push({ method, text: String(JSON.parse(String(init?.body)).text) });
      said = { ok: true, ts: "1.1", channel: "D1" };
    }
    return new Response(JSON.stringify(said), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  }) as typeof fetch;
  return {
    posted,
    fetchFn,
    hold() {
      let open = () => {};
      gate = new Promise<void>((resolve) => (open = resolve));
      const reached = new Promise<void>((resolve) => (arrived = resolve));
      return { reached, let_go: () => (gate = null, open()) };
    },
  };
}

/** A person's direct message to the app, signed as Slack signs an event. */
async function dm(eventId: string, text: string): Promise<Request> {
  const raw = JSON.stringify({
    type: "event_callback",
    event_id: eventId,
    event: {
      type: "message",
      channel_type: "im",
      channel: "D1",
      user: "UPAT",
      text,
      ts: "1700000000.000100",
    },
  });
  const ts = String(Math.floor(Date.now() / 1000));
  return new Request("http://localhost/slack-in", {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "x-slack-request-timestamp": ts,
      "x-slack-signature": await signSlack(SIGNING_SECRET, ts, raw),
    },
    body: raw,
  });
}

Deno.test("slack-in: a team whose roster cannot be read is left out, and the others are served", async () => {
  const evicted = await world("slack");
  const served = await world("slack");
  const s = slack();
  const handle = slackInHandler({ fetchFn: s.fetchFn });
  const event = `Ev${served.tag}1`;
  try {
    await evicted.evict();
    const carried = await answered(handle, await dm(event, `to: ${served.worker}\n\nare you there?`));
    assertEquals(carried.status, 200, JSON.stringify(carried.said));
    assertEquals(carried.said.to, served.worker);
    const held = await served.messages();
    assertEquals(held.length, 1);
    assertStringIncludes(held[0].body, "are you there?");
    assertEquals(await served.rows(`slack:${event}`), ["done"]);
    assertEquals(
      await answered(handle, await dm(event, `to: ${served.worker}\n\nare you there?`)),
      { status: 200, said: { duplicate: true } },
    );
  } finally {
    await evicted.close();
    await served.close();
  }
});

Deno.test("slack-in: an event whose delivery failed is carried by Slack's next delivery", async () => {
  const w = await world("slack");
  const s = slack();
  const handle = slackInHandler({ fetchFn: s.fetchFn });
  const event = `Ev${w.tag}2`;
  const message = () => dm(event, `to: ${w.worker}\n\nship it`);
  try {
    // No team of the project lets its gateway read its roster: there is nobody to carry the event to, and
    // nobody is told anything. This needs the database to hold no Slack gateway but this test's own.
    const others = await w.sql`select count(*)::int as n from brigade_gateway.gateways
                                where kind = 'slack' and team_id <> ${w.team}::uuid`;
    assertEquals(others[0].n, 0, "the local database holds Slack gateways of other teams");
    await w.evict();
    const failed = await answered(handle, await message());
    assertEquals(failed.status, 500, JSON.stringify(failed.said));
    assertEquals(
      await w.rows(`slack:${event}`),
      [],
      "a failed delivery leaves neither the mark nor its claim",
    );
    assertEquals(s.posted, []);

    await w.install();
    await w.rejoin();
    const carried = await answered(handle, await message());
    assertEquals(carried.status, 200, JSON.stringify(carried.said));
    assertEquals(carried.said.to, w.worker);
    assertEquals((await w.messages()).length, 1);
    assertEquals(await w.rows(`slack:${event}`), ["done"]);
    assertEquals(await answered(handle, await message()), { status: 200, said: { duplicate: true } });
    assertEquals((await w.messages()).length, 1);
  } finally {
    await w.close();
  }
});

Deno.test("slack-in: a delivery that comes while the event is being carried is told to come again", async () => {
  const w = await world("slack");
  const s = slack();
  const handle = slackInHandler({ fetchFn: s.fetchFn });
  const event = `Ev${w.tag}3`;
  const message = () => dm(event, "nobody is named here");
  try {
    // Slack delivers an event again when three seconds pass without an answer: here the answer to the person
    // is what takes long.
    const post = s.hold();
    const first = answered(handle, await message());
    await post.reached;
    assertEquals(await w.rows(`slack:${event}`), ["carrying"]);
    assertEquals(await answered(handle, await message()), { status: 500, said: { error: BEING_CARRIED } });
    post.let_go();
    assertEquals(await first, { status: 200, said: { told: true } });
    assertEquals(await w.rows(`slack:${event}`), ["done"]);
    assertEquals(await answered(handle, await message()), { status: 200, said: { duplicate: true } });
    assertEquals(s.posted.length, 1, "the person was answered once");
  } finally {
    await w.close();
  }
});
