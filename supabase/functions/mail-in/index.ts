// mail-in: the gateway core's inbound endpoint. A connector (docs/mail-connectors.md) POSTs a received mail here
// in the contract's shape; the core decides where the mail goes and sends it into the team as a Brigade message
// from the gateway session. The handler, with what it answers and why, is ../_shared/mail_in_serve.ts.

import { mailInHandler } from "../_shared/mail_in_serve.ts";

Deno.serve(mailInHandler());
