// Probe plugin for opencode 2.x: logs events, learns the active session ID from events,
// and wakes that session from a trigger file through ctx.session.prompt.
import fs from "node:fs"

const LOG = process.env.PROBE_LOG
const TRIGGER = process.env.PROBE_TRIGGER
const log = (o) => fs.appendFileSync(LOG, JSON.stringify({ t: Date.now(), ...o }) + "\n")

// Find a session ID anywhere in an event payload (the v2 event shape is not documented).
const findSid = (o, d = 0) => {
  if (!o || typeof o !== "object" || d > 4) return null
  if (typeof o.sessionID === "string") return o.sessionID
  for (const v of Object.values(o)) {
    const r = findSid(v, d + 1)
    if (r) return r
  }
  return null
}

export default {
  id: "brigade-probe",
  async setup(ctx) {
    let last = null
    const ac = new AbortController()
    log({ ev: "plugin_setup", dir: ctx.location?.directory, sessionMethods: Object.keys(ctx.session ?? {}) })
    void (async () => {
      for await (const event of ctx.event.subscribe({ signal: ac.signal })) {
        const sid = findSid(event)
        if (sid) last = sid
        if (!/message\.|part\.|delta/.test(event.type)) log({ ev: "event", type: event.type, sid, keys: Object.keys(event) })
      }
    })()
    await ctx.session.hook("prompt", (e) => {
      log({ ev: "prompt_hook", keys: Object.keys(e), delivery: e.delivery, sid: findSid(e) })
      const s = findSid(e)
      if (s) last = s
    })
    await ctx.shell.hook("create.before", (e) => {
      log({ ev: "shell_create_before", keys: Object.keys(e), hasProbeSid: !!last })
      if (last) e.env.BRIGADE_PROBE_SESSION = last
    })
    let watcher
    if (TRIGGER) {
      fs.writeFileSync(TRIGGER, "")
      watcher = fs.watch(TRIGGER, async () => {
        const text = fs.readFileSync(TRIGGER, "utf-8").trim()
        if (!text) return
        fs.writeFileSync(TRIGGER, "")
        log({ ev: "trigger", session: last })
        try {
          const r = await ctx.session.prompt({ sessionID: last, text })
          log({ ev: "prompt_result", result: JSON.stringify(r).slice(0, 200) })
        } catch (e) {
          log({ ev: "prompt_throw", msg: String(e).slice(0, 300) })
        }
      })
    }
    return () => {
      watcher?.close()
      ac.abort()
      log({ ev: "plugin_cleanup" })
    }
  },
}
