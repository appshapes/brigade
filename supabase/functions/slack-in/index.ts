// slack-in: Slack's Events API and slash-command endpoint. It reads a person's DM, mention, reply in a Brigade
// thread or /brigade command, decides where it goes and sends it into the team as a Brigade message from that
// team's gateway session. The handler, with what it answers and why, is ../_shared/slack_in_serve.ts.

import { slackInHandler } from "../_shared/slack_in_serve.ts";

Deno.serve(slackInHandler());
