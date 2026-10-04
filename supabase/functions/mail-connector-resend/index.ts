// mail-connector-resend: the shipped Resend connector (docs/mail-connectors.md). Resend's webhook arrives at
// …/webhook and the mail it names is fetched and forwarded to the gateway core; the core's sends arrive at …/send.

import { connectorHandler } from "../_shared/connector_serve.ts";
import { resendProvider } from "../_shared/providers/resend.ts";

const env = (k: string) => Deno.env.get(k) ?? "";

Deno.serve(connectorHandler(
  resendProvider({ apiKey: env("RESEND_API_KEY"), webhookSecret: env("RESEND_WEBHOOK_SECRET") || null }),
  {
    secret: env("BRIGADE_MAIL_CONNECTOR_SECRET"),
    inboundUrl: env("BRIGADE_MAIL_INBOUND_URL") || `${env("SUPABASE_URL")}/functions/v1/mail-in`,
  },
));
