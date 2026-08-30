// SessionStart hook (exec form, sync, short timeout): register the session with the adapter and make sure one watcher runs.
import { spawn } from 'node:child_process';
import { mkdirSync, openSync, readFileSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { readSessionRecord, runAdapter, log, readStdinJson } from './common.js';

interface HookInput { session_id: string; cwd: string; source: 'startup' | 'resume' | 'clear' | 'compact' | 'fork'; transcript_path?: string }

async function main() {
  const input = readStdinJson<HookInput>();
  if (!input) { log('no hook input'); return; }
  const claudePid = Number(process.env.CLAUDE_PID) || process.ppid;
  const rec = readSessionRecord(claudePid);                    // best effort: display name, socket path, version
  const name = rec?.name || basename(input.cwd);

  const r = await runAdapter(['session', 'register', '--json'], {
    harness: 'claude-code', harness_version: rec?.version, harness_session_id: input.session_id,
    session_name: name, session_description: `cwd ${basename(input.cwd)}`, state: 'active',
  }, 8_000);
  if (r.code !== 0) { log(`session register failed (exit ${r.code}): ${r.stderr.trim()}`); return; }   // exit 0: never block startup
  const reg = JSON.parse(r.stdout) as { session_id: string; session_name: string; team_name: string };

  // One watcher per Claude Code process. SessionStart also fires on resume/clear/compact, so guard against duplicates.
  const dataDir = process.env.CLAUDE_PLUGIN_DATA || join(process.env.HOME ?? '', '.claude', 'plugins', 'data', 'brigade');
  const lockDir = join(dataDir, 'watchers');
  mkdirSync(lockDir, { recursive: true });
  const lockFile = join(lockDir, `${claudePid}.json`);
  let alive = false;
  try { const { pid } = JSON.parse(readFileSync(lockFile, 'utf8')); process.kill(pid, 0); alive = true; } catch { /* no live watcher */ }
  if (!alive) {
    const logFd = openSync(join(lockDir, `${claudePid}.log`), 'a');
    const watcherJs = join(fileURLToPath(new URL('.', import.meta.url)), 'watcher.js');
    const socket = process.env.CLAUDE_CODE_MESSAGING_SOCKET || rec?.messagingSocketPath || '';
    const child = spawn(process.execPath, [watcherJs], {
      detached: true,                          // own process group/session (setsid); new console on Windows
      stdio: ['ignore', logFd, logFd],         // nothing tied to the hook's stdio, or the hook would hang
      windowsHide: true,
      env: { ...process.env, BRIGADE_SESSION_ID: reg.session_id, BRIGADE_CLAUDE_PID: String(claudePid), BRIGADE_SOCKET: socket },
    });
    child.unref();
    writeFileSync(lockFile, JSON.stringify({ pid: child.pid, claudePid, brigadeSessionId: reg.session_id, startedAt: Date.now() }));
  }

  // Plain-text stdout of a SessionStart hook is added to Claude's context.
  process.stdout.write(
    `Brigade: this session is registered in team "${reg.team_name}" as "${reg.session_name}" (session_id ${reg.session_id}). ` +
    'Teammates: TeamListSessions. Message one: TeamSendMessage. Incoming Brigade messages arrive tagged from="did:brigade:<session_id>"; ' +
    'reply with TeamSendMessage, never with SendMessage.\n');
}
main().catch((e) => { log('hook error', e); process.exit(0); });
