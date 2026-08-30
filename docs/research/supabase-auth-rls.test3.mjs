import { createClient } from '@supabase/supabase-js'
import { execSync } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
const URL = 'http://127.0.0.1:54321', KEY = 'sb_publishable_ACJWlzQHlZjBrEguHvfOxg_3BJgxAaH'
const sleep = (ms) => new Promise(r => setTimeout(r, ms))
const decode = (jwt) => JSON.parse(Buffer.from(jwt.split('.')[1], 'base64url').toString())
const psql = (sql) => execSync(`docker exec -i supabase_db_sbtest psql -U postgres -d postgres -At -c "${sql.replace(/"/g, '\\"')}"`).toString().trim()
const mk = (extra={}) => createClient(URL, KEY, { auth: { persistSession: true, autoRefreshToken: false, detectSessionInUrl: false, ...extra } })

// A. reuse detection, then wait out the grace window
const R = mk()
const s = await R.auth.signInAnonymously()
const sid = decode(s.data.session.access_token).session_id
const rt0 = s.data.session.refresh_token
const rt1 = (await R.auth.refreshSession({ refresh_token: rt0 })).data.session.refresh_token
const r2 = await R.auth.refreshSession({ refresh_token: rt1 }); const rt2 = r2.data.session.refresh_token; const at2 = r2.data.session.access_token
await sleep(11500)
const bad = await R.auth.refreshSession({ refresh_token: rt0 })
console.log(`A1 rt0 (2 behind) after 11.5s -> ${bad.error?.status} ${bad.error?.code}`)
console.log('A2 refresh_tokens after detection: ' + psql(`select string_agg(left(token,4)||':rev='||revoked, ' ' order by id) from auth.refresh_tokens where session_id='${sid}'`))
await sleep(11000)
const again = await R.auth.refreshSession({ refresh_token: rt2 })
console.log(`A3 active rt2 used 11s after detection -> ${again.error ? again.error.status + ' ' + again.error.code + ' (' + again.error.message + ')' : 'SUCCESS'}`)
console.log('A4 session row still exists: ' + psql(`select exists(select 1 from auth.sessions where id='${sid}')`))

// B. client-side clearing: expired access token + dead refresh token via setSession on a file-backed client
const path = `${process.cwd()}/.state/dead/session.json`
const [h, p, sig] = at2.split('.')
const payload = decode(at2); payload.exp = Math.floor(Date.now()/1000) - 600
const expiredJwt = h + '.' + Buffer.from(JSON.stringify(payload)).toString('base64url') + '.' + sig
const F = createClient(URL, KEY, { auth: { persistSession: true, autoRefreshToken: false, detectSessionInUrl: false, storageKey: 'brigade-session', storage: {
  getItem: (k) => { try { return JSON.parse(readFileSync(path,'utf8'))[k] ?? null } catch { return null } },
  setItem: (k, v) => { const { mkdirSync, writeFileSync } = require('node:fs'); },
  removeItem: () => {} } } })
let signedOut = false
F.auth.onAuthStateChange((ev) => { if (ev === 'SIGNED_OUT') signedOut = true })
const ss = await F.auth.setSession({ access_token: expiredJwt, refresh_token: rt2 })
const gs = await F.auth.getSession()
console.log(`B1 setSession(expired access token, dead refresh token) -> error=${ss.error?.code || ss.error?.name}; getSession -> ${gs.data.session ? 'present' : 'null'}; SIGNED_OUT emitted=${signedOut}`)

// C. realtime under RLS, inspect subscription while open
const B = mk(); const sb = await B.auth.signInAnonymously(); const uidB = sb.data.user.id
const A = mk(); const sa = await A.auth.signInAnonymously()
const secret = 'a-sufficiently-long-secret-for-testing'
const t = await A.rpc('create_team', { p_name: 'rt-team', p_join_secret: secret })
await B.rpc('join_team', { p_team_id: t.data.team_id, p_join_secret: secret })
const SA = (await A.from('sessions').insert({ team_id: t.data.team_id, name: 'a' }).select('id').single()).data.id
const SB = (await B.from('sessions').insert({ team_id: t.data.team_id, name: 'b' }).select('id').single()).data.id
for (const variant of [{ label: 'filtered', filter: `recipient_session_id=eq.${SB}` }, { label: 'unfiltered', filter: undefined }]) {
  let got = []
  const ch = B.channel('inbox-' + variant.label)
  ch.on('postgres_changes', { event: 'INSERT', schema: 'public', table: 'messages', ...(variant.filter ? { filter: variant.filter } : {}) }, (p) => got.push(p.new?.body + (p.errors ? ' errors=' + JSON.stringify(p.errors) : '')))
  const st = await new Promise((res) => { ch.subscribe((s, e) => { if (s !== 'SUBSCRIBED' || true) res(s + (e ? ' ' + e.message : '')) }); setTimeout(() => res('TIMEOUT'), 8000) })
  await sleep(1000)
  console.log(`C1[${variant.label}] status=${st}; realtime.subscription rows while open: ` + psql(`select count(*)||' rows; roles='||coalesce(string_agg(distinct claims_role::text, ','),'')||'; anon='||coalesce(string_agg(distinct claims->>'is_anonymous', ','),'')||'; filters='||coalesce(string_agg(filters::text, ' | '),'') from realtime.subscription`))
  const ins = await A.from('messages').insert({ sender_session_id: SA, recipient_session_id: SB, body: 'rt-' + variant.label, idempotency_key: 'rt-' + variant.label }).select('id').single()
  for (let i = 0; i < 32 && got.length === 0; i++) await sleep(250)
  console.log(`C2[${variant.label}] insert err=${ins.error?.message || 'none'}; received=${JSON.stringify(got)}`)
  await B.removeChannel(ch)
}
process.exit(0)
