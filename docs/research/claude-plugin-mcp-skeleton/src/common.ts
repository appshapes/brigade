import { execFile } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

/** Adapter executable: .mcp.json env (from ${user_config.adapter_path}) for the shim, CLAUDE_PLUGIN_OPTION_* for hooks, PATH lookup otherwise. */
export function adapterPath(): string {
  return process.env.BRIGADE_ADAPTER?.trim() || process.env.CLAUDE_PLUGIN_OPTION_ADAPTER_PATH?.trim() || 'brigade-supabase';
}

export function configDir(): string {
  return process.env.CLAUDE_CONFIG_DIR || join(process.env.HOME ?? '', '.claude');
}

/** Undocumented but observed shape of $CLAUDE_CONFIG_DIR/sessions/<pid>.json (Claude Code 2.1.251). Treat every field as optional. */
export interface SessionRecord {
  pid: number; sessionId: string; cwd: string; name?: string; status?: string;
  messagingSocketPath?: string; version?: string; kind?: string; entrypoint?: string;
}

export function readSessionRecord(pid: number): SessionRecord | null {
  try { return JSON.parse(readFileSync(join(configDir(), 'sessions', `${pid}.json`), 'utf8')) as SessionRecord; } catch { return null; }
}

export interface AdapterResult { code: number; stdout: string; stderr: string }

/** Spawn the adapter directly (argv array, no shell). Structured input goes on stdin, JSON result comes back on stdout. */
export function runAdapter(args: string[], stdinJson?: unknown, timeoutMs = 20_000): Promise<AdapterResult> {
  return new Promise((resolve) => {
    const child = execFile(
      adapterPath(), args,
      { env: process.env, timeout: timeoutMs, maxBuffer: 8 * 1024 * 1024, windowsHide: true, encoding: 'utf8' },
      (err, stdout, stderr) => {
        const e = err as (NodeJS.ErrnoException & { code?: number | string }) | null;
        if (e && e.code === 'ENOENT') return resolve({ code: 127, stdout: '', stderr: `adapter not found: ${adapterPath()}` });
        resolve({ code: typeof e?.code === 'number' ? e.code : e ? 1 : 0, stdout: String(stdout), stderr: String(stderr) });
      },
    );
    child.stdin?.end(stdinJson === undefined ? '' : JSON.stringify(stdinJson) + '\n');
  });
}

/** stdout of a stdio MCP server is the JSON-RPC channel; all diagnostics go to stderr. */
export function log(...parts: unknown[]): void {
  process.stderr.write('[brigade] ' + parts.map(String).join(' ') + '\n');
}

export function readStdinJson<T>(): T | null {
  try { return JSON.parse(readFileSync(0, 'utf8')) as T; } catch { return null; }
}
