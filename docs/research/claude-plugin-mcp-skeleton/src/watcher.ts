// Inbound watcher: long-lived, detached from the hook that spawned it, dies with its Claude Code process.
import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { connect } from 'node:net';
import { adapterPath, readSessionRecord, runAdapter, log } from './common.js';

interface InboundEvent {
  message_id: string;
  sender: { principal_ref: string; human_label?: string; session_id: string; session_name: string };
  body: string; summary?: string; created_at: string;
}

const sessionId = process.env.BRIGADE_SESSION_ID ?? '';
const claudePid = Number(process.env.BRIGADE_CLAUDE_PID);
const token = process.env.CLAUDE_CODE_MESSAGING_TOKEN ?? '';   // inherited from the hook environment
let socketPath = process.env.BRIGADE_SOCKET || '';

const seen = new Set<string>();
const seenOrder: string[] = [];
function remember(id: string): boolean {
  if (seen.has(id)) return false;
  seen.add(id); seenOrder.push(id);
  if (seenOrder.length > 5000) seen.delete(seenOrder.shift() as string);
  return true;
}

// from-name: harness strips control/format chars and the characters " < >, max 64 code points; do the same up front.
function sanitizeName(s: string): string {
  const cleaned = s.replace(/[\p{Cc}\p{Cf}"<>]/gu, '');
  return [...cleaned].slice(0, 64).join('') || 'teammate';
}

function inject(ev: InboundEvent): Promise<void> {
  // Native peer-message wrapper. Attribute order is fixed: from, from-session, hop-chain, from-name, from-mode (all optional).
  const from = `did:brigade:${encodeURIComponent(ev.sender.session_id)}`;
  const who = `${ev.sender.session_name}${ev.sender.human_label ? ` (${ev.sender.human_label})` : ''}`;
  const body =
    `${ev.body}\n\n[Brigade] From ${who}, message_id ${ev.message_id}. ` +
    `To reply use TeamSendMessage with recipient_session_id="${ev.sender.session_id}" (the built-in SendMessage cannot reach Brigade sessions).`;
  const content = `<cross-session-message from="${from}" from-name="${sanitizeName(ev.sender.session_name)}">\n${body}\n</cross-session-message>`;
  return new Promise((resolve, reject) => {
    const sock = connect(socketPath);                // Unix socket on macOS/Linux, named pipe path on Windows
    sock.once('error', reject);
    sock.once('connect', () => {
      sock.write(JSON.stringify({ type: 'auth', token }) + '\n');          // optional on macOS/Linux, required on Windows
      sock.end(JSON.stringify({ type: 'user', message: { role: 'user', content } }) + '\n', () => resolve());
    });
  });
}

async function handle(ev: InboundEvent) {
  if (!ev?.message_id || !remember(ev.message_id)) return;
  try { await inject(ev); }
  catch (e) { log(`inject failed for ${ev.message_id}: ${e}`); seen.delete(ev.message_id); return; }   // unacked: adapter redelivers
  const r = await runAdapter(['message', 'ack', '--json'], { session_id: sessionId, message_id: ev.message_id }, 8_000);
  if (r.code !== 0) log(`ack failed for ${ev.message_id}: ${r.stderr.trim()}`);
}

function watchLoop(attempt = 0) {
  const child = spawn(adapterPath(), ['message', 'watch', '--json', '--session', sessionId],
    { stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true, env: process.env });
  child.stderr.on('data', (d) => log('adapter:', String(d).trim()));
  createInterface({ input: child.stdout }).on('line', (line) => {
    try { void handle(JSON.parse(line) as InboundEvent); } catch { log('bad event line'); }
  });
  child.on('spawn', () => { attempt = 0; });
  child.on('exit', (code) => {
    const wait = Math.min(30_000, 1000 * 2 ** Math.min(attempt, 5));
    log(`watch exited ${code}; restart in ${wait}ms`);
    setTimeout(() => watchLoop(attempt + 1), wait);
  });
}

async function heartbeat() {
  const rec = readSessionRecord(claudePid);
  if (rec?.messagingSocketPath) socketPath = rec.messagingSocketPath;   // survives /clear and socket moves
  await runAdapter(['session', 'heartbeat', '--json'],
    { session_id: sessionId, state: rec?.status === 'busy' ? 'active' : 'idle', session_name: rec?.name }, 8_000);
}

function liveness() {
  try { process.kill(claudePid, 0); }
  catch {
    log(`claude ${claudePid} gone; closing`);
    void runAdapter(['session', 'close', '--json'], { session_id: sessionId }, 3_000).finally(() => process.exit(0));
  }
}

log(`watcher up pid=${process.pid} claude=${claudePid} session=${sessionId} socket=${socketPath}`);
watchLoop();
setInterval(liveness, 2_000);
setInterval(() => void heartbeat(), 30_000);
