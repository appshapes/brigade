import { assertEquals, assertRejects, assertStringIncludes } from "jsr:@std/assert@1";
import {
  authorized,
  basicPasswordOf,
  bearerOf,
  fromInboundWire,
  fromOutboundWire,
  pathSecretOf,
  type Provider,
  sendClient,
  SendError,
  toInboundWire,
  toOutboundWire,
  WebhookRejected,
} from "./connector.ts";
import { connectorHandler, routeOf } from "./connector_serve.ts";
import type { InboundMail, OutboundMail } from "./mail.ts";

const SECRET = "0123456789abcdef0123456789abcdef0123456789abcdef";
const CORE = "https://example.supabase.co/functions/v1/mail-in";

const inbound: InboundMail = {
  providerId: "pm-1",
  from: "alice@example.com",
  fromName: "Alice",
  to: ["a1b2c3d4+0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f@example.resend.app"],
  subject: "Re: hello",
  messageId: "<m1@example.com>",
  inReplyTo: "<m0@example.com>",
  references: ["<m0@example.com>"],
  text: "yes please",
  receivedAt: "2026-10-04T12:00:00.000Z",
};

const outbound: OutboundMail = {
  from: "Brigade <brigade@example.com>",
  to: "alice@example.com",
  replyTo: "a1b2c3d4+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@example.resend.app",
  subject: "[brigade/team] hello",
  text: "hello",
  headers: { "Auto-Submitted": "auto-generated" },
};

function post(url: string, body: unknown, headers: Record<string, string> = {}): Request {
  return new Request(url, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
}

Deno.test("the inbound shape round-trips and is validated", () => {
  const r = fromInboundWire(JSON.stringify(toInboundWire(inbound)));
  assertEquals(r.ok, true);
  if (r.ok) assertEquals(r.value, inbound);
  const bad = (body: unknown) => {
    const p = fromInboundWire(typeof body === "string" ? body : JSON.stringify(body));
    return p.ok ? "accepted" : p.reason;
  };
  assertStringIncludes(bad("nope"), "not JSON");
  assertStringIncludes(bad([]), "not a JSON object");
  assertStringIncludes(bad({ ...toInboundWire(inbound), brigade_mail: 2 }), "brigade_mail must be 1");
  assertStringIncludes(bad({ ...toInboundWire(inbound), id: "" }), "id is required");
  assertStringIncludes(bad({ ...toInboundWire(inbound), from: "alice" }), "from must be an email address");
  assertStringIncludes(bad({ ...toInboundWire(inbound), to: "x@example.com" }), "to must be a list");
  assertStringIncludes(bad({ ...toInboundWire(inbound), text: 5 }), "text must be a string");
  const minimal = fromInboundWire(
    JSON.stringify({
      brigade_mail: 1,
      id: "x",
      from: "ALICE@Example.com",
      to: ["B@Example.COM ", "b@example.com"],
    }),
  );
  assertEquals(minimal.ok, true);
  if (minimal.ok) {
    assertEquals(minimal.value.from, "alice@example.com");
    assertEquals(minimal.value.to, ["b@example.com"]);
    assertEquals(minimal.value.subject, "");
    assertEquals(minimal.value.text, "");
    assertEquals(minimal.value.references, []);
    assertEquals(minimal.value.fromName, null);
  }
});

Deno.test("the outbound shape round-trips and is validated", () => {
  const r = fromOutboundWire(JSON.stringify(toOutboundWire(outbound)));
  assertEquals(r.ok, true);
  if (r.ok) assertEquals(r.value, outbound);
  const bad = (body: unknown) => {
    const p = fromOutboundWire(JSON.stringify(body));
    return p.ok ? "accepted" : p.reason;
  };
  assertStringIncludes(bad({ ...toOutboundWire(outbound), to: "nobody" }), "to must be one email address");
  assertStringIncludes(bad({ ...toOutboundWire(outbound), from: "" }), "from is required");
  assertStringIncludes(bad({ ...toOutboundWire(outbound), headers: { a: 1 } }), "header a must be a string");
  assertStringIncludes(bad({ ...toOutboundWire(outbound), subject: null }), "subject must be a string");
  const noHeaders = fromOutboundWire(JSON.stringify({ ...toOutboundWire(outbound), headers: undefined }));
  if (noHeaders.ok) assertEquals(noHeaders.value.headers, {});
  else throw new Error(noHeaders.reason);
});

Deno.test("the secret is read as a bearer, a basic-auth password or a path segment, in constant time", () => {
  const bearer = post(CORE, {}, { Authorization: `Bearer ${SECRET}` });
  const basic = post(CORE, {}, { Authorization: "Basic " + btoa(`brigade:${SECRET}`) });
  const path = post(`${CORE}/${SECRET}`, {});
  const none = post(CORE, {});
  assertEquals(bearerOf(bearer), SECRET);
  assertEquals(basicPasswordOf(basic), SECRET);
  assertEquals(pathSecretOf(path, "mail-in"), SECRET);
  assertEquals(pathSecretOf(none, "mail-in"), null);
  for (const r of [bearer, basic, path]) assertEquals(authorized(r, "mail-in", SECRET), true);
  assertEquals(authorized(none, "mail-in", SECRET), false);
  assertEquals(authorized(bearer, "mail-in", SECRET + "x"), false);
  assertEquals(authorized(bearer, "mail-in", ""), false);
  assertEquals(authorized(post(CORE, {}, { Authorization: "Basic not-base64!" }), "mail-in", SECRET), false);
});

Deno.test("routeOf names the endpoint whatever prefix the platform leaves on the path", () => {
  assertEquals(routeOf(post("https://x.supabase.co/functions/v1/mail-connector-resend/send", {})), "send");
  assertEquals(routeOf(post("https://x.supabase.co/mail-connector-postmark/webhook", {})), "webhook");
  assertEquals(
    routeOf(post(`https://x.supabase.co/mail-connector-postmark/webhook/${SECRET}`, {})),
    "webhook",
  );
  assertEquals(routeOf(post("https://x.supabase.co/mail-connector-postmark", {})), null);
});

function fakeFetch(answer: (url: string, init: RequestInit) => Response | Error): typeof fetch {
  return ((url: string | URL | Request, init?: RequestInit) => {
    const a = answer(String(url), init ?? {});
    return a instanceof Error ? Promise.reject(a) : Promise.resolve(a);
  }) as typeof fetch;
}

Deno.test("sendClient POSTs the outbound shape with the bearer and maps the answers", async () => {
  let seen: { url: string; auth: string | null; body: unknown } | null = null;
  const ok = sendClient(
    "https://c.example/send",
    SECRET,
    fakeFetch((url, init) => {
      seen = {
        url,
        auth: new Headers(init.headers).get("authorization"),
        body: JSON.parse(String(init.body)),
      };
      return new Response(JSON.stringify({ id: "re_9" }), { status: 200 });
    }),
  );
  assertEquals((await ok.send(outbound)).providerId, "re_9");
  assertEquals(seen!.url, "https://c.example/send");
  assertEquals(seen!.auth, `Bearer ${SECRET}`);
  assertEquals(seen!.body, toOutboundWire(outbound));

  const final = sendClient(
    "https://c.example/send",
    SECRET,
    fakeFetch(() =>
      new Response(JSON.stringify({ error: "inactive recipient", retryable: false }), { status: 422 })
    ),
  );
  const e1 = await assertRejects(() => final.send(outbound), SendError);
  assertEquals(e1.retryable, false);
  assertStringIncludes(e1.message, "inactive recipient");

  const limited = sendClient(
    "https://c.example/send",
    SECRET,
    fakeFetch(() => new Response("", { status: 429 })),
  );
  assertEquals((await assertRejects(() => limited.send(outbound), SendError)).retryable, true);

  const down = sendClient("https://c.example/send", SECRET, fakeFetch(() => new Error("connection refused")));
  const e3 = await assertRejects(() => down.send(outbound), SendError);
  assertEquals(e3.retryable, true);
  assertEquals(e3.status, 0);

  const noId = sendClient(
    "https://c.example/send",
    SECRET,
    fakeFetch(() => new Response("{}", { status: 200 })),
  );
  assertEquals((await assertRejects(() => noId.send(outbound), SendError)).retryable, true);

  const unset = sendClient("", SECRET);
  assertEquals((await assertRejects(() => unset.send(outbound), SendError)).retryable, false);

  const saysRetry = sendClient(
    "https://c.example/send",
    SECRET,
    fakeFetch(() => new Response(JSON.stringify({ error: "busy", retryable: true }), { status: 400 })),
  );
  assertEquals((await assertRejects(() => saysRetry.send(outbound), SendError)).retryable, true);
});

function fakeProvider(over: Partial<Provider> = {}): Provider & { sent: OutboundMail[] } {
  const sent: OutboundMail[] = [];
  return {
    sent,
    name: "fake",
    webhook: () => Promise.resolve({ providerId: inbound.providerId, mail: inbound }),
    fetch: () => Promise.reject(new Error("fetch should not be called when the webhook carries the mail")),
    send: (m) => {
      sent.push(m);
      return Promise.resolve({ providerId: "sent-1" });
    },
    ...over,
  };
}

const CONNECTOR = "https://x.supabase.co/functions/v1/mail-connector-fake";

Deno.test("the connector forwards a webhook's mail to the core as the inbound shape", async () => {
  let forwarded: { url: string; auth: string | null; body: unknown } | null = null;
  const h = connectorHandler(fakeProvider(), {
    secret: SECRET,
    inboundUrl: CORE,
    fetchFn: fakeFetch((url, init) => {
      forwarded = {
        url,
        auth: new Headers(init.headers).get("authorization"),
        body: JSON.parse(String(init.body)),
      };
      return new Response(JSON.stringify({ message_id: "m1" }), { status: 200 });
    }),
  });
  const r = await h(post(`${CONNECTOR}/webhook`, { any: "provider shape" }));
  assertEquals(r.status, 200);
  assertEquals(await r.json(), { message_id: "m1" });
  assertEquals(forwarded!.url, CORE);
  assertEquals(forwarded!.auth, `Bearer ${SECRET}`);
  assertEquals(forwarded!.body, toInboundWire(inbound));
});

Deno.test("the connector fetches the mail when the webhook carried only the id", async () => {
  let fetched = 0;
  const p = fakeProvider({
    webhook: () => Promise.resolve({ providerId: "re_1" }),
    fetch: () => {
      fetched++;
      return Promise.resolve(inbound);
    },
  });
  const h = connectorHandler(p, {
    secret: SECRET,
    inboundUrl: CORE,
    fetchFn: fakeFetch(() => new Response("{}", { status: 200 })),
  });
  assertEquals((await h(post(`${CONNECTOR}/webhook`, {}))).status, 200);
  assertEquals(fetched, 1);
});

Deno.test("the connector answers the provider by what the core and the webhook said", async () => {
  const core = (status: number) => fakeFetch(() => new Response(JSON.stringify({ error: "x" }), { status }));
  const h = (status: number, p = fakeProvider()) =>
    connectorHandler(p, { secret: SECRET, inboundUrl: CORE, fetchFn: core(status) });
  assertEquals(
    (await h(401)(post(`${CONNECTOR}/webhook`, {}))).status,
    502,
    "a wrong secret: let the provider retry",
  );
  assertEquals((await h(503)(post(`${CONNECTOR}/webhook`, {}))).status, 502, "a core outage: retry");
  const dropped = await h(400)(post(`${CONNECTOR}/webhook`, {}));
  assertEquals(dropped.status, 200, "a mail the core refused for good is not retried");
  assertEquals(await dropped.json(), { dropped: 400 });
  const rejected = fakeProvider({ webhook: () => Promise.reject(new WebhookRejected("not signed", 403)) });
  assertEquals((await h(200, rejected)(post(`${CONNECTOR}/webhook`, {}))).status, 403);
  const ignored = fakeProvider({ webhook: () => Promise.resolve(null) });
  const ig = await h(200, ignored)(post(`${CONNECTOR}/webhook`, {}));
  assertEquals(ig.status, 200);
  assertEquals(await ig.json(), { ignored: true });
  const down = connectorHandler(fakeProvider(), {
    secret: SECRET,
    inboundUrl: CORE,
    fetchFn: fakeFetch(() => new Error("refused")),
  });
  assertEquals((await down(post(`${CONNECTOR}/webhook`, {}))).status, 502);
  assertEquals((await h(200)(new Request(`${CONNECTOR}/webhook`, { method: "GET" }))).status, 405);
  assertEquals((await h(200)(post(`${CONNECTOR}/other`, {}))).status, 404);
});

Deno.test("the connector's send endpoint checks the secret, validates the body and maps send errors", async () => {
  const p = fakeProvider();
  const h = connectorHandler(p, { secret: SECRET, inboundUrl: CORE });
  assertEquals((await h(post(`${CONNECTOR}/send`, toOutboundWire(outbound)))).status, 401);
  const bad = await h(post(`${CONNECTOR}/send`, { brigade_mail: 1 }, { Authorization: `Bearer ${SECRET}` }));
  assertEquals(bad.status, 400);
  const ok = await h(
    post(`${CONNECTOR}/send`, toOutboundWire(outbound), { Authorization: `Bearer ${SECRET}` }),
  );
  assertEquals(ok.status, 200);
  assertEquals(await ok.json(), { id: "sent-1" });
  assertEquals(p.sent, [outbound]);
  const byPath = await h(post(`${CONNECTOR}/send/${SECRET}`, toOutboundWire(outbound)));
  assertEquals(byPath.status, 200);

  const failing = connectorHandler(
    fakeProvider({ send: () => Promise.reject(new SendError(422, "inactive recipient", false)) }),
    { secret: SECRET, inboundUrl: CORE },
  );
  const f = await failing(
    post(`${CONNECTOR}/send`, toOutboundWire(outbound), { Authorization: `Bearer ${SECRET}` }),
  );
  assertEquals(f.status, 422);
  assertEquals(await f.json(), { error: "inactive recipient", retryable: false });

  const noAnswer = connectorHandler(
    fakeProvider({ send: () => Promise.reject(new SendError(0, "provider unreachable")) }),
    { secret: SECRET, inboundUrl: CORE },
  );
  const n = await noAnswer(
    post(`${CONNECTOR}/send`, toOutboundWire(outbound), { Authorization: `Bearer ${SECRET}` }),
  );
  assertEquals(n.status, 502);
  assertEquals((await n.json()).retryable, true);
});
