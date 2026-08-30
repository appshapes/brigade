// UserPromptSubmit hook: optional polling fallback for hosts without an inbox socket. Plain-text stdout becomes context.
import { runAdapter, readStdinJson } from './common.js';

async function main() {
  if (process.env.CLAUDE_PLUGIN_OPTION_POLL_ON_PROMPT !== 'true') return;
  readStdinJson();
  const r = await runAdapter(['message', 'receive', '--json', '--max', '10'], undefined, 4_000);
  if (r.code !== 0) return;
  const { messages } = JSON.parse(r.stdout) as { messages: Array<{ message_id: string; sender: { session_id: string; session_name: string }; body: string }> };
  if (!messages?.length) return;
  process.stdout.write(
    'Brigade messages received while idle (reply with TeamSendMessage to the given session_id):\n' +
    messages.map((m) => `- from ${m.sender.session_name} [${m.sender.session_id}] (${m.message_id}): ${m.body}`).join('\n') + '\n');
}
main().finally(() => process.exit(0));
