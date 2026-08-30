import { createClient } from '@supabase/supabase-js'
import { mkdirSync, readFileSync, writeFileSync, renameSync, unlinkSync, chmodSync, statSync } from 'node:fs'
import { dirname } from 'node:path'
import { randomBytes } from 'node:crypto'

const URL = 'http://127.0.0.1:54321'
const KEY = 'sb_publishable_ACJWlzQHlZjBrEguHvfOxg_3BJgxAaH'
const results = []
const ok = (name, cond, detail='') => { results.push([cond?'PASS':'FAIL', name, detail]); console.log((cond?'PASS ':'FAIL ') + name + (detail?'  -- '+detail:'')) }
const sleep = (ms) => new Promise(r => setTimeout(r, ms))
const decode = (jwt) => JSON.parse(Buffer.from(jwt.split('.')[1], 'base64url').toString())
const mk = (extra={}) => createClient(URL, KEY, { auth: { persistSession: true, autoRefreshToken: false, detectSessionInUrl: false, ...extra } })

function fileSessionStorage(path) {
  const read = () => { try { return JSON.parse(readFileSync(path, 'utf8')) } catch { return {} } }
  const write = (doc) => { mkdirSync(dirname(path), { recursive: true, mode: 0o700 }); const tmp = `${path}.${process.pid}.tmp`; writeFileSync(tmp, JSON.stringify(doc), { mode: 0o600 }); chmodSync(tmp, 0o600); renameSync(tmp, path) }
  return {
    getItem(k) { return read()[k] ?? null },
    setItem(k, v) { const d = read(); d[k] = v; write(d) },
    removeItem(k) { const d = read(); delete d[k]; if (Object.keys(d).length === 0) { try { unlinkSync(path) } catch {} } else write(d) },
  }
}

// --- 1. anonymous sign-in + claims ---
const A = mk(), B = mk(), C = mk(), D = mk()
const sa = await A.auth.signInAnonymously({ options: { data: { harness: 'claude-code' } } })
const sb = await B.auth.signInAnonymously()
const sc = await C.auth.signInAnonymously()
const sd = await D.auth.signInAnonymously()
ok('signInAnonymously returns session for A/B/C/D', !sa.error && !sb.error && !sc.error && !sd.error, JSON.stringify([sa.error?.code, sb.error?.code].filter(Boolean)))
const ca = decode(sa.data.session.access_token)
ok('A JWT role === authenticated', ca.role === 'authenticated', `role=${ca.role}`)
ok('A JWT is_anonymous === true (boolean)', ca.is_anonymous === true, `is_anonymous=${JSON.stringify(ca.is_anonymous)} typeof=${typeof ca.is_anonymous}`)
ok('A JWT aud === authenticated, has sub/session_id', ca.aud === 'authenticated' && !!ca.sub && !!ca.session_id, `aud=${ca.aud} claims=${Object.keys(ca).sort().join(',')}`)
ok('A user.is_anonymous true; refresh token present', sa.data.user.is_anonymous === true && !!sa.data.session.refresh_token, `rt length=${sa.data.session.refresh_token.length}`)
const uidA = ca.sub, uidB = decode(sb.data.session.access_token).sub, uidC = decode(sc.data.session.access_token).sub

// --- 2. teams via RPC ---
const secretAlpha = randomBytes(32).toString('base64url'), secretBravo = randomBytes(32).toString('base64url')
const t1 = await A.rpc('create_team', { p_name: 'alpha', p_join_secret: secretAlpha, p_human_label: 'alice@example.com' })
const t2 = await C.rpc('create_team', { p_name: 'bravo', p_join_secret: secretBravo, p_human_label: 'carol@example.com' })
ok('create_team (A: alpha, C: bravo)', !t1.error && !t2.error && t1.data.status === 'created', JSON.stringify(t1.error || t1.data))
const alpha = t1.data.team_id, bravo = t2.data.team_id
const j1 = await B.rpc('join_team', { p_team_id: alpha, p_join_secret: 'wrong-secret-1' })
const j2 = await B.rpc('join_team', { p_team_id: alpha, p_join_secret: 'wrong-secret-2' })
ok('join_team wrong secret -> status invalid_secret (no exception)', !j1.error && j1.data.status === 'invalid_secret' && j2.data.status === 'invalid_secret', JSON.stringify(j1.error || j1.data))
const j3 = await B.rpc('join_team', { p_team_id: alpha, p_join_secret: secretAlpha, p_human_label: 'bob@example.com' })
ok('join_team correct secret -> joined', !j3.error && j3.data.status === 'joined' && j3.data.team_id === alpha, JSON.stringify(j3.error || j3.data))
const jx = await B.rpc('join_team', { p_team_id: '00000000-0000-0000-0000-000000000000', p_join_secret: secretAlpha })
ok('join_team unknown team id -> invalid_secret (no enumeration)', jx.data?.status === 'invalid_secret', JSON.stringify(jx.error || jx.data))
// rate limit: D fails 5 times, 6th is rate_limited, and a correct secret while limited is also refused
let statuses = []
for (let i = 0; i < 6; i++) { const r = await D.rpc('join_team', { p_team_id: alpha, p_join_secret: 'nope-' + i }); statuses.push(r.error ? 'ERR:' + r.error.code : r.data.status + (r.data.retry_after_seconds ? `(${r.data.retry_after_seconds}s)` : '')) }
const jd = await D.rpc('join_team', { p_team_id: alpha, p_join_secret: secretAlpha })
ok('join_team rate limit: 5 x invalid_secret then rate_limited; correct secret refused while limited', statuses.slice(0,5).every(s => s === 'invalid_secret') && statuses[5].startsWith('rate_limited') && jd.data?.status === 'rate_limited', statuses.join(' ') + ' then ' + jd.data?.status)
// anon (no session) cannot call join_team or read tables
const ANON = createClient(URL, KEY, { auth: { persistSession: false, autoRefreshToken: false } })
const anonRpc = await ANON.rpc('join_team', { p_team_id: alpha, p_join_secret: secretAlpha })
const anonSel = await ANON.from('sessions').select('id')
ok('publishable key without session: join_team and select sessions denied', !!anonRpc.error && !!anonSel.error, `rpc=${anonRpc.error?.code} ${anonRpc.error?.message} | select=${anonSel.error?.code} ${anonSel.error?.message}`)

// --- 3. sessions and cross-team isolation ---
const regA = await A.from('sessions').insert({ team_id: alpha, name: 'payments-api', harness: 'claude-code', harness_version: '2.1.251' }).select('id, owner_user_id, team_id').single()
const regB = await B.from('sessions').insert({ team_id: alpha, name: 'payments-api' }).select('id, owner_user_id').single()   // same name, different session
const regC = await C.from('sessions').insert({ team_id: bravo, name: 'other-team' }).select('id, owner_user_id').single()
ok('session register for A/B (alpha) and C (bravo); owner stamped by DB', !regA.error && !regB.error && !regC.error && regA.data.owner_user_id === uidA && regB.data.owner_user_id === uidB, JSON.stringify(regA.error || regB.error || regC.error || 'ok'))
const SA = regA.data.id, SB = regB.data.id, SC = regC.data.id
const spoof = await A.from('sessions').insert({ team_id: alpha, name: 'spoof', owner_user_id: uidB }).select('id')
ok('session insert naming owner_user_id -> 42501 permission denied (column privilege)', spoof.error?.code === '42501', `${spoof.error?.code} ${spoof.error?.message}`)
const foreign = await A.from('sessions').insert({ team_id: bravo, name: 'sneak-into-bravo' }).select('id')
ok('A cannot register a session in team bravo', !!foreign.error, `${foreign.error?.code} ${foreign.error?.message}`)
const listA = await A.from('sessions').select('id, name, team_id')
const listC = await C.from('sessions').select('id, name, team_id')
ok('A lists alpha sessions only (SA, SB), not SC', !listA.error && listA.data.length === 2 && listA.data.every(s => s.team_id === alpha), JSON.stringify(listA.error || listA.data.map(s => s.name)))
ok('C lists bravo sessions only (SC)', !listC.error && listC.data.length === 1 && listC.data[0].id === SC, JSON.stringify(listC.error || listC.data.map(s => s.name)))
const teamsStar = await A.from('teams').select('*')
const teamsCols = await A.from('teams').select('id, name, secret_version')
ok('teams select * denied (column privileges), select id,name works and shows only alpha', !!teamsStar.error && !teamsCols.error && teamsCols.data.length === 1 && teamsCols.data[0].id === alpha, `star=${teamsStar.error?.code} cols=${JSON.stringify(teamsCols.data)}`)
const hb = await B.from('sessions').update({ state: 'idle', last_seen_at: new Date().toISOString() }).eq('id', SA).select('id')
ok('B cannot heartbeat A session (0 rows)', !hb.error && hb.data.length === 0, JSON.stringify(hb.error || hb.data))
const membersA = await A.from('team_members').select('user_id, human_label')
ok('A sees alpha roster (A, B) with unverified labels', !membersA.error && membersA.data.length === 2, JSON.stringify(membersA.error || membersA.data.map(m => m.human_label)))

// --- 4. messages: stamping, rejection, isolation ---
const m1 = await A.from('messages').insert({ sender_session_id: SA, recipient_session_id: SB, body: 'hello bob', summary: 'hi', idempotency_key: 'k1' }).select('id, team_id, sender_user_id, created_at').single()
ok('A -> SB message accepted; sender_user_id and team_id stamped by DB (team_id omitted by client)', !m1.error && m1.data.sender_user_id === uidA && m1.data.team_id === alpha, JSON.stringify(m1.error || m1.data))
const m1dup = await A.from('messages').insert({ sender_session_id: SA, recipient_session_id: SB, body: 'hello bob again', idempotency_key: 'k1' }).select('id')
ok('duplicate idempotency_key -> 23505 unique violation', m1dup.error?.code === '23505', `${m1dup.error?.code}`)
const m2 = await A.from('messages').insert({ sender_session_id: SA, recipient_session_id: SB, body: 'x', idempotency_key: 'k2', sender_user_id: uidB }).select('id')
ok('client-supplied sender_user_id -> 42501 permission denied', m2.error?.code === '42501', `${m2.error?.code} ${m2.error?.message}`)
const m3 = await A.from('messages').insert({ sender_session_id: SB, recipient_session_id: SA, body: 'x', idempotency_key: 'k3' }).select('id')
ok('sender_session_id not owned by caller -> rejected', !!m3.error, `${m3.error?.code} ${m3.error?.message}`)
const m4 = await A.from('messages').insert({ sender_session_id: SA, recipient_session_id: SC, body: 'x', idempotency_key: 'k4' }).select('id')
ok('recipient in another team -> rejected', !!m4.error, `${m4.error?.code} ${m4.error?.message}`)
const m5 = await A.from('messages').insert({ sender_session_id: SA, recipient_session_id: SB, body: 'x', idempotency_key: 'k5', created_at: '2000-01-01T00:00:00Z' }).select('id')
ok('client-supplied created_at -> 42501 permission denied', m5.error?.code === '42501', `${m5.error?.code}`)
const inboxB = await B.from('messages').select('id, body, sender_user_id, sender_session_id').eq('recipient_session_id', SB)
const inboxC = await C.from('messages').select('id')
const allC = await C.from('messages').select('id')
ok('B reads its inbox (1 msg from A); C sees no messages', !inboxB.error && inboxB.data.length === 1 && inboxB.data[0].sender_user_id === uidA && allC.data.length === 0, JSON.stringify(inboxB.error || inboxB.data.map(m => m.body)) + ' C=' + allC.data.length)
const ackWrong = await A.from('messages').update({ acked_at: new Date().toISOString() }).eq('id', m1.data.id).select('id')
const ackB = await B.from('messages').update({ acked_at: new Date().toISOString() }).eq('id', m1.data.id).select('id, acked_at')
const ackBody = await B.from('messages').update({ body: 'tampered' }).eq('id', m1.data.id).select('id')
ok('ack: sender cannot ack (0 rows); recipient can; recipient cannot edit body (42501)', ackWrong.data?.length === 0 && ackB.data?.length === 1 && !!ackB.data[0].acked_at && ackBody.error?.code === '42501', `wrong=${ackWrong.data?.length} b=${ackB.data?.length} body=${ackBody.error?.code}`)

// --- 5. shared session file: restore in a second process-like client ---
const path = `${process.cwd()}/.state/profile1/session.json`
const F1 = createClient(URL, KEY, { auth: { persistSession: true, autoRefreshToken: false, detectSessionInUrl: false, storage: fileSessionStorage(path), storageKey: 'brigade-session' } })
const sf = await F1.auth.signInAnonymously()
const mode = (statSync(path).mode & 0o777).toString(8)
const F2 = createClient(URL, KEY, { auth: { persistSession: true, autoRefreshToken: false, detectSessionInUrl: false, storage: fileSessionStorage(path), storageKey: 'brigade-session' } })
const restored = await F2.auth.getSession()
ok('file storage: session written 0600 and restored by a second client without network', !sf.error && mode === '600' && restored.data.session?.access_token === sf.data.session.access_token, `mode=${mode} restored=${!!restored.data.session}`)
const stored = JSON.parse(readFileSync(path, 'utf8'))['brigade-session']
ok('stored JSON has access_token, refresh_token, expires_at, user', ['access_token','refresh_token','expires_at','user'].every(k => k in JSON.parse(stored)), Object.keys(JSON.parse(stored)).join(','))

// --- 6. refresh-token rotation and reuse detection ---
const R = mk()
const sr = await R.auth.signInAnonymously()
const rt0 = sr.data.session.refresh_token
const r1 = await R.auth.refreshSession({ refresh_token: rt0 })
const rt1 = r1.data.session?.refresh_token
ok('refresh with rt0 -> new session with rotated token', !r1.error && rt1 && rt1 !== rt0, `${r1.error?.code || ''} rotated=${rt1 !== rt0}`)
const r1b = await R.auth.refreshSession({ refresh_token: rt0 })
ok('immediate reuse of rt0 inside 10s reuse interval -> allowed, returns active token', !r1b.error && r1b.data.session?.refresh_token === rt1, `${r1b.error?.code || 'ok'} sameAsActive=${r1b.data.session?.refresh_token === rt1}`)
const r2 = await R.auth.refreshSession({ refresh_token: rt1 })
const rt2 = r2.data.session?.refresh_token
ok('refresh with rt1 -> rt2', !r2.error && rt2 && rt2 !== rt1)
console.log('   sleeping 12s to leave the reuse interval...')
await sleep(12000)
const r2b = await R.auth.refreshSession({ refresh_token: rt1 })
ok('reuse of rt1 (one step behind active rt2) after 12s -> allowed (fail-to-save rule), returns active token', !r2b.error && r2b.data.session?.refresh_token === rt2, `${r2b.error?.code || 'ok'} ${r2b.error?.message || ''}`)
const r0c = await R.auth.refreshSession({ refresh_token: rt0 })
ok('reuse of rt0 (two steps behind) after interval -> refresh_token_already_used', r0c.error?.code === 'refresh_token_already_used', `${r0c.error?.status} ${r0c.error?.code} ${r0c.error?.message}`)
const r2c = await R.auth.refreshSession({ refresh_token: rt2 })
ok('after reuse detection the whole session is terminated: active rt2 now fails too', !!r2c.error, `${r2c.error?.status} ${r2c.error?.code} ${r2c.error?.message}`)
const gs = await R.auth.getSession()
ok('client cleared the stored session after terminal refresh failure (getSession -> null)', gs.data.session === null, `session=${gs.data.session ? 'present' : 'null'} err=${gs.error?.code || 'none'}`)

// --- 7. realtime delivery under RLS (anonymous user, postgres_changes filtered by recipient) ---
const RB = mk()
await RB.auth.setSession({ access_token: sb.data.session.access_token, refresh_token: sb.data.session.refresh_token })
let received = null
const ch = RB.channel('inbox-' + SB).on('postgres_changes', { event: 'INSERT', schema: 'public', table: 'messages', filter: `recipient_session_id=eq.${SB}` }, (p) => { received = p.new })
const subStatus = await new Promise((res) => { ch.subscribe((s, e) => { if (s === 'SUBSCRIBED' || s === 'CHANNEL_ERROR' || s === 'TIMED_OUT') res(s + (e ? ' ' + e.message : '')) }); setTimeout(() => res('TIMEOUT'), 8000) })
await sleep(500)
const m6 = await A.from('messages').insert({ sender_session_id: SA, recipient_session_id: SB, body: 'realtime hello', idempotency_key: 'k6' }).select('id').single()
const m7 = await C.from('messages').insert({ sender_session_id: SC, recipient_session_id: SC, body: 'bravo self-note', idempotency_key: 'k7' }).select('id').single()
for (let i = 0; i < 40 && !received; i++) await sleep(250)
ok('realtime: B (anonymous user) receives A->SB insert via postgres_changes; payload carries stamped sender', subStatus === 'SUBSCRIBED' && received?.id === m6.data?.id && received?.sender_user_id === uidA, `sub=${subStatus} received=${received ? received.body : 'none'} m6err=${m6.error?.message || ''} m7err=${m7.error?.message || ''}`)
await RB.removeAllChannels()

console.log('\nSUMMARY: ' + results.filter(r => r[0] === 'PASS').length + ' pass, ' + results.filter(r => r[0] === 'FAIL').length + ' fail')
process.exit(0)
