// Probe extension: logs lifecycle events and wakes an idle session from a trigger file.
import * as fs from "node:fs";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const LOG = process.env.PROBE_LOG ?? "/dev/null";
const TRIGGER = process.env.PROBE_TRIGGER ?? "";
const log = (o: Record<string, unknown>) =>
	fs.appendFileSync(LOG, JSON.stringify({ t: Date.now(), ...o }) + "\n");

export default function (pi: ExtensionAPI) {
	const inst = Math.random().toString(36).slice(2, 8);
	let watcher: fs.FSWatcher | undefined;
	pi.on("session_start", async (event, ctx) => {
		log({
			ev: "session_start",
			inst,
			reason: (event as { reason?: string }).reason,
			prev: (event as { previousSessionFile?: string }).previousSessionFile ?? null,
			sid: ctx.sessionManager.getSessionId(),
			file: ctx.sessionManager.getSessionFile() ?? null,
			env_sid: process.env.PI_SESSION_ID ?? null,
			hasUI: ctx.hasUI,
		});
		if (!TRIGGER) return;
		fs.writeFileSync(TRIGGER, "");
		watcher?.close();
		watcher = fs.watch(TRIGGER, () => {
			const body = fs.readFileSync(TRIGGER, "utf-8").trim();
			if (!body) return;
			fs.writeFileSync(TRIGGER, "");
			const [mode, ...rest] = body.split(" ");
			const text = rest.join(" ");
			log({ ev: "trigger", mode, inst });
			const opts =
				mode === "idle"
					? { triggerTurn: true }
					: { triggerTurn: true, deliverAs: mode as "steer" | "followUp" | "nextTurn" };
			pi.sendMessage({ customType: "brigade-probe", content: text, display: true }, opts);
		});
	});
	pi.on("session_shutdown", async () => {
		watcher?.close();
		log({ ev: "session_shutdown", inst });
	});
	pi.on("session_compact", async () => log({ ev: "session_compact" }));
	pi.on("before_agent_start", async (e) =>
		log({ ev: "before_agent_start", messageType: (e as { messageType?: string }).messageType }),
	);
	pi.on("agent_start", async () => log({ ev: "agent_start" }));
	pi.on("agent_end", async () => log({ ev: "agent_end" }));
	pi.on("tool_result", async (e) => {
		const ev = e as { toolName?: string; content?: unknown };
		log({ ev: "tool_result", tool: ev.toolName, content: JSON.stringify(ev.content).slice(0, 600) });
	});
}
