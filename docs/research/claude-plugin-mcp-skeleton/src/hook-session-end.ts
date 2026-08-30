// SessionEnd hook: best-effort close. All SessionEnd hooks share a 1.5 s budget and plugin hook timeouts cannot raise it.
import { readFileSync, unlinkSync } from 'node:fs';
import { join } from 'node:path';
import { runAdapter, log, readStdinJson } from './common.js';

async function main() {
  const input = readStdinJson<{ session_id: string; reason: 'clear' | 'resume' | 'logout' | 'prompt_input_exit' | 'other' }>();
  if (input?.reason === 'clear') return;               // process continues; watcher keeps running
  const claudePid = Number(process.env.CLAUDE_PID) || process.ppid;
  const lock = join(process.env.CLAUDE_PLUGIN_DATA ?? '', 'watchers', `${claudePid}.json`);
  let brigadeSessionId: string | undefined;
  try {
    const l = JSON.parse(readFileSync(lock, 'utf8')) as { pid: number; brigadeSessionId: string };
    brigadeSessionId = l.brigadeSessionId;
    try { process.kill(l.pid, 'SIGTERM'); } catch { /* already gone */ }
    unlinkSync(lock);
  } catch { /* no lock */ }
  if (brigadeSessionId) await runAdapter(['session', 'close', '--json'], { session_id: brigadeSessionId }, 1_000);
}
main().catch((e) => log('session-end error', e)).finally(() => process.exit(0));
