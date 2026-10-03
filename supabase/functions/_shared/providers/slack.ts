// The Slack Web API, as much of it as the gateway uses: posting, opening a DM, looking people and channels up,
// joining a public channel, a reaction, a permalink, and auth.test for the bot's own identity. Every call is one
// HTTPS request with the bot token; a Slack `ok: false` answer is a SlackError carrying Slack's error code.

export class SlackError extends Error {
  code: string;
  constructor(code: string, message?: string) {
    super(message ?? `slack: ${code}`);
    this.code = code;
  }
  /** retryable: Slack asked for a pause, or the platform failed; everything else is this request's fault. */
  get retryable(): boolean {
    return this.code === "ratelimited" || this.code === "internal_error" ||
      this.code === "service_unavailable" ||
      this.code === "fatal_error" || this.code.startsWith("http_5");
  }
}

export interface SlackUser {
  id: string;
  name: string | null;
  email: string | null;
  isBot: boolean;
  deleted: boolean;
}

export interface SlackChannel {
  id: string;
  name: string | null;
  isIm: boolean;
  isPrivate: boolean;
  isMember: boolean;
}

export interface SlackApi {
  authTest(): Promise<
    { userId: string; botId: string | null; team: string; teamDomain: string; url: string }
  >;
  postMessage(
    a: { channel: string; text: string; threadTs?: string | null; metadata?: Record<string, unknown> },
  ): Promise<{ ts: string; channel: string }>;
  postEphemeral(a: { channel: string; user: string; text: string; threadTs?: string | null }): Promise<void>;
  openDM(user: string): Promise<string>;
  userInfo(user: string): Promise<SlackUser>;
  lookupByEmail(email: string): Promise<SlackUser | null>;
  findUserByName(name: string): Promise<SlackUser | null>;
  findChannelByName(name: string): Promise<SlackChannel | null>;
  channelInfo(channel: string): Promise<SlackChannel | null>;
  joinChannel(channel: string): Promise<void>;
  addReaction(channel: string, ts: string, name: string): Promise<void>;
  permalink(channel: string, ts: string): Promise<string | null>;
}

const API = "https://slack.com/api";

export function slackApi(botToken: string, fetchFn: typeof fetch = fetch, base = API): SlackApi {
  async function call(
    method: string,
    body: Record<string, unknown> | null,
    form = false,
  ): Promise<Record<string, unknown>> {
    const init: RequestInit = {
      method: body === null ? "GET" : "POST",
      headers: { Authorization: `Bearer ${botToken}` },
    };
    let url = `${base}/${method}`;
    if (body !== null) {
      if (form) {
        (init.headers as Record<string, string>)["Content-Type"] = "application/x-www-form-urlencoded";
        init.body = new URLSearchParams(
          Object.fromEntries(Object.entries(body).map(([k, v]) => [k, String(v)])),
        ).toString();
      } else {
        (init.headers as Record<string, string>)["Content-Type"] = "application/json; charset=utf-8";
        init.body = JSON.stringify(body);
      }
    }
    if (body === null && method.includes("?")) url = `${base}/${method}`;
    const r = await fetchFn(url, init);
    if (!r.ok) throw new SlackError(`http_${r.status}`, `slack: ${method} answered ${r.status}`);
    const d = (await r.json()) as Record<string, unknown>;
    if (d.ok !== true) {
      throw new SlackError(
        String(d.error ?? "unknown_error"),
        `slack: ${method}: ${String(d.error ?? "unknown_error")}`,
      );
    }
    return d;
  }
  const toUser = (u: Record<string, unknown>): SlackUser => {
    const profile = (u.profile ?? {}) as Record<string, unknown>;
    const name = String(profile.display_name || profile.real_name || u.real_name || u.name || "") || null;
    return {
      id: String(u.id),
      name,
      email: typeof profile.email === "string" ? profile.email.toLowerCase() : null,
      isBot: !!u.is_bot,
      deleted: !!u.deleted,
    };
  };
  const toChannel = (c: Record<string, unknown>): SlackChannel => ({
    id: String(c.id),
    name: typeof c.name === "string" ? c.name : null,
    isIm: !!c.is_im,
    isPrivate: !!c.is_private,
    isMember: !!c.is_member,
  });

  return {
    async authTest() {
      const d = await call("auth.test", {});
      return {
        userId: String(d.user_id),
        botId: typeof d.bot_id === "string" ? d.bot_id : null,
        team: String(d.team ?? ""),
        teamDomain: String(d.url ?? "").replace(/^https?:\/\//, "").replace(/\/$/, ""),
        url: String(d.url ?? ""),
      };
    },
    async postMessage(a) {
      const body: Record<string, unknown> = {
        channel: a.channel,
        text: a.text,
        unfurl_links: false,
        unfurl_media: false,
      };
      if (a.threadTs) body.thread_ts = a.threadTs;
      if (a.metadata) body.metadata = a.metadata;
      const d = await call("chat.postMessage", body);
      return { ts: String(d.ts), channel: String(d.channel) };
    },
    async postEphemeral(a) {
      const body: Record<string, unknown> = { channel: a.channel, user: a.user, text: a.text };
      if (a.threadTs) body.thread_ts = a.threadTs;
      await call("chat.postEphemeral", body);
    },
    async openDM(user) {
      const d = await call("conversations.open", { users: user });
      return String((d.channel as Record<string, unknown>).id);
    },
    async userInfo(user) {
      const d = await call(`users.info?user=${encodeURIComponent(user)}`, null);
      return toUser(d.user as Record<string, unknown>);
    },
    async lookupByEmail(email) {
      try {
        const d = await call(`users.lookupByEmail?email=${encodeURIComponent(email)}`, null);
        return toUser(d.user as Record<string, unknown>);
      } catch (e) {
        if (e instanceof SlackError && e.code === "users_not_found") return null;
        throw e;
      }
    },
    async findUserByName(name) {
      const want = name.toLowerCase();
      let cursor = "";
      for (let page = 0; page < 20; page++) {
        const d = await call(
          `users.list?limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""}`,
          null,
        );
        for (const m of (d.members as Record<string, unknown>[]) ?? []) {
          const u = toUser(m);
          if (u.deleted || u.isBot) continue;
          const profile = (m.profile ?? {}) as Record<string, unknown>;
          const names = [
            m.name,
            profile.display_name,
            profile.real_name,
            profile.display_name_normalized,
            profile.real_name_normalized,
          ]
            .filter((x): x is string => typeof x === "string").map((x) => x.toLowerCase());
          if (names.includes(want)) return u;
        }
        cursor = String(((d.response_metadata ?? {}) as Record<string, unknown>).next_cursor ?? "");
        if (!cursor) break;
      }
      return null;
    },
    async findChannelByName(name) {
      const want = name.toLowerCase();
      let cursor = "";
      for (let page = 0; page < 20; page++) {
        const d = await call(
          `conversations.list?limit=200&types=public_channel,private_channel&exclude_archived=true${
            cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""
          }`,
          null,
        );
        for (const c of (d.channels as Record<string, unknown>[]) ?? []) {
          if (typeof c.name === "string" && c.name.toLowerCase() === want) return toChannel(c);
        }
        cursor = String(((d.response_metadata ?? {}) as Record<string, unknown>).next_cursor ?? "");
        if (!cursor) break;
      }
      return null;
    },
    async channelInfo(channel) {
      try {
        const d = await call(`conversations.info?channel=${encodeURIComponent(channel)}`, null);
        return toChannel(d.channel as Record<string, unknown>);
      } catch (e) {
        if (e instanceof SlackError && (e.code === "channel_not_found" || e.code === "missing_scope")) {
          return null;
        }
        throw e;
      }
    },
    async joinChannel(channel) {
      await call("conversations.join", { channel });
    },
    async addReaction(channel, ts, name) {
      try {
        await call("reactions.add", { channel, timestamp: ts, name });
      } catch (e) {
        if (e instanceof SlackError && (e.code === "already_reacted" || e.code === "missing_scope")) return;
        throw e;
      }
    },
    async permalink(channel, ts) {
      try {
        const d = await call(
          `chat.getPermalink?channel=${encodeURIComponent(channel)}&message_ts=${encodeURIComponent(ts)}`,
          null,
        );
        return typeof d.permalink === "string" ? d.permalink : null;
      } catch {
        return null;
      }
    },
  };
}
