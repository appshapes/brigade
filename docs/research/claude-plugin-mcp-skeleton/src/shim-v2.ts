// Same shim on @modelcontextprotocol/server 2.0.0 (ESM only, node >= 20, zod 4). Only differences: import paths, z.object() schemas, serveStdio(factory).
import { McpServer } from '@modelcontextprotocol/server';
import { serveStdio } from '@modelcontextprotocol/server/stdio';
import * as z from 'zod';
import { runAdapter, log } from './common.js';

const ListOutput = z.object({ team_name: z.string().optional(), sessions: z.array(z.object({ session_id: z.string(), session_name: z.string(), state: z.enum(['active', 'idle', 'offline']) })) });

serveStdio(() => {
  const server = new McpServer({ name: 'brigade', version: '0.1.0' }, { instructions: 'Brigade team messaging; see TeamListSessions / TeamSendMessage.' });
  server.registerTool('TeamListSessions', {
    title: 'List Brigade team sessions', description: 'List sessions in the configured Brigade team.',
    inputSchema: z.object({ include_offline: z.boolean().optional() }), outputSchema: ListOutput,
    annotations: { readOnlyHint: true, destructiveHint: false, idempotentHint: true, openWorldHint: true },
  }, async ({ include_offline }) => {
    const r = await runAdapter(['session', 'list', '--json', ...(include_offline ? ['--include-offline'] : [])]);
    if (r.code !== 0) return { content: [{ type: 'text', text: `adapter exit ${r.code}: ${r.stderr.slice(0, 2000)}` }], isError: true };
    const data = ListOutput.parse(JSON.parse(r.stdout));
    return { content: [{ type: 'text', text: JSON.stringify(data) }], structuredContent: data };
  });
  log('brigade shim (sdk v2) ready');
  return server;
});
