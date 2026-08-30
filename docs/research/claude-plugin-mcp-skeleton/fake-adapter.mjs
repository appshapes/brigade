const [cmd, sub] = process.argv.slice(2);
const out = (o) => { console.log(JSON.stringify(o)); process.exit(0); };
if (cmd === 'message' && sub === 'watch') {
  process.stdin.resume();
  setTimeout(() => {
    console.log(JSON.stringify({ message_id: 'msg_in_1', sender: { principal_ref: 'p2', human_label: 'bob@example.com', session_id: 'ses_1', session_name: 'payments-api' }, recipient_session_id: 'ses_me', body: 'Migration finished on main; the new column is tenant_id. Safe to rebase now.', summary: 'Migration finished', created_at: '2026-08-30T12:00:02Z' }));
  }, 2500);
  setTimeout(() => process.exit(0), 90_000);
}

if (!(cmd === 'message' && sub === 'watch')) {
let stdin = '';
process.stdin.on('data', (d) => (stdin += d)).on('end', async () => {
  const inp = stdin.trim() ? JSON.parse(stdin) : {};
  if (cmd === 'describe') return out({ protocol_version: '1', adapter: 'fake', capabilities: ['watch'] });
  if (cmd === 'session' && sub === 'register') return out({ session_id: 'ses_me', session_name: inp.session_name ?? 'unnamed', team_name: 'demo' });
  if (cmd === 'session' && sub === 'heartbeat') return out({ ok: true });
  if (cmd === 'session' && sub === 'close') return out({ ok: true });
  if (cmd === 'session' && sub === 'list') return out({ team_name: 'demo', sessions: [{ session_id: 'ses_1', session_name: 'payments-api', principal_ref: 'p1', human_label: 'alice@example.com', state: 'active', last_seen_at: '2026-08-30T12:00:00Z' }] });
  if (cmd === 'message' && sub === 'send') { if (inp.recipient_session_id === 'nope') { console.error('unauthorized: recipient not in team'); process.exit(4); } return out({ status: 'accepted', message_id: 'msg_' + String(inp.idempotency_key).slice(0, 8), recipient_session_id: inp.recipient_session_id, created_at: '2026-08-30T12:00:01Z' }); }
  if (cmd === 'message' && sub === 'ack') { (await import('node:fs')).appendFileSync(process.env.FAKE_ACK_LOG || '/dev/null', JSON.stringify(inp) + '\n'); return out({ ok: true }); }
  if (cmd === 'message' && sub === 'receive') return out({ messages: [] });
  console.error('unknown command ' + cmd + ' ' + sub); process.exit(2);
});

}
