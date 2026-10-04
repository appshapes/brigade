// mail-connector-postmark: the shipped Postmark connector (docs/mail-connectors.md). Postmark's inbound webhook
// arrives at …/webhook with the whole mail and is forwarded to the gateway core; the core's sends arrive at …/send.

import { connectorHandler } from "../_shared/connector_serve.ts";
import { postmarkProvider } from "../_shared/providers/postmark.ts";

const env = (k: string) => Deno.env.get(k) ?? "";

Deno.serve(connectorHandler(
  postmarkProvider({
    serverToken: env("POSTMARK_SERVER_TOKEN"),
    webhookSecret: env("POSTMARK_WEBHOOK_SECRET") || null,
  }),
  {
    secret: env("BRIGADE_MAIL_CONNECTOR_SECRET"),
    inboundUrl: env("BRIGADE_MAIL_INBOUND_URL") || `${env("SUPABASE_URL")}/functions/v1/mail-in`,
  },
));
