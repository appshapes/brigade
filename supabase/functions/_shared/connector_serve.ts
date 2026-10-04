// connectorHandler is the HTTP half of a shipped connector (docs/mail-connectors.md): the provider's webhook
// arrives at …/webhook and leaves for the gateway core as the contract's inbound shape; the core's sends arrive
// at …/send, authenticated by the connector secret, and leave through the provider. A connector in any other
// language does the same two things.

import {
  authorized,
  fromOutboundWire,
  type Provider,
  SendError,
  toInboundWire,
  WebhookRejected,
} from "./connector.ts";

export interface ConnectorEnv {
  /** The connector secret: checked on /send, sent as a bearer to the core's inbound URL. */
  secret: string;
  /** The core's inbound endpoint, …/functions/v1/mail-in. */
  inboundUrl: string;
  fetchFn?: typeof fetch;
}

const JSON_HEADERS = { "Content-Type": "application/json" };

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: JSON_HEADERS });
}

/** routeOf names the endpoint a request is for: the path holds `send` or `webhook` after the function's name. */
export function routeOf(req: Request): "send" | "webhook" | null {
  const segs = new URL(req.url).pathname.split("/").filter(Boolean);
  if (segs.includes("send")) return "send";
  if (segs.includes("webhook")) return "webhook";
  return null;
}

export function connectorHandler(provider: Provider, env: ConnectorEnv): (req: Request) => Promise<Response> {
  const f = env.fetchFn ?? fetch;
  return async (req: Request): Promise<Response> => {
    if (req.method !== "POST") return json(405, { error: "method not allowed" });
    const route = routeOf(req);
    const raw = await req.text();

    if (route === "webhook") {
      let hook: { providerId: string; mail?: Awaited<ReturnType<Provider["fetch"]>> } | null;
      try {
        hook = await provider.webhook(req, raw);
      } catch (e) {
        if (e instanceof WebhookRejected) return json(e.status, { error: e.message });
        throw e;
      }
      if (!hook) return json(200, { ignored: true });
      let mail: Awaited<ReturnType<Provider["fetch"]>>;
      try {
        mail = hook.mail ?? await provider.fetch(hook.providerId);
      } catch (e) {
        console.error(
          `${provider.name}: could not read the mail`,
          e instanceof Error ? e.message : String(e),
        );
        return json(502, { error: "the mail could not be read from the provider" });
      }
      let r: Response;
      try {
        r = await f(env.inboundUrl, {
          method: "POST",
          headers: { Authorization: `Bearer ${env.secret}`, ...JSON_HEADERS },
          body: JSON.stringify(toInboundWire(mail)),
        });
      } catch (e) {
        console.error("the gateway core did not answer", e instanceof Error ? e.message : String(e));
        return json(502, { error: "the gateway core did not answer" });
      }
      const body = await r.text();
      if (r.ok) return new Response(body, { status: 200, headers: JSON_HEADERS });
      if (r.status === 401 || r.status >= 500) {
        // A wrong secret or a core outage: let the provider retry, and let the failure show in its dashboard.
        console.error(`the gateway core answered ${r.status}`, body.slice(0, 300));
        return json(502, { error: `the gateway core answered ${r.status}` });
      }
      console.error(`the gateway core refused the mail with ${r.status}`, body.slice(0, 300));
      return json(200, { dropped: r.status });
    }

    if (route === "send") {
      if (!authorized(req, "send", env.secret)) {
        return json(401, { error: "the request does not carry this connector's secret" });
      }
      const parsed = fromOutboundWire(raw);
      if (!parsed.ok) return json(400, { error: parsed.reason });
      try {
        const sent = await provider.send(parsed.value);
        return json(200, { id: sent.providerId });
      } catch (e) {
        if (e instanceof SendError) {
          const status = e.status >= 400 && e.status <= 599 ? e.status : 502;
          return json(status, { error: e.message, retryable: e.retryable });
        }
        console.error(`${provider.name}: send failed`, e instanceof Error ? e.message : String(e));
        return json(500, { error: e instanceof Error ? e.message : String(e), retryable: true });
      }
    }

    return json(404, { error: "no such endpoint: POST …/webhook or …/send" });
  };
}
