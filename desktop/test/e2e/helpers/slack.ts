/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import http from 'node:http';

export const REQUIRED_SLACK_SCOPES = [
  'chat:write',
  'im:write',
  'im:history',
  'channels:history',
  'groups:history',
  'mpim:history',
  'channels:read',
  'groups:read',
  'users:read',
  'users:read.email',
  'reactions:read',
  'reactions:write',
  'files:write',
] as const;

export interface SlackScript {
  status?: number;
  body: Record<string, unknown>;
  scopes?: readonly string[];
}

export interface SlackRequest {
  method: string;
  path: string;
  bearerPresent: boolean;
  fields: Record<string, string>;
}

export interface ScriptedSlackServer {
  baseUrl: string;
  /** Shorthand for queuing auth.test responses. */
  enqueue(...scripts: SlackScript[]): void;
  enqueueMethod(method: string, ...scripts: SlackScript[]): void;
  requests(): SlackRequest[];
  close(): Promise<void>;
}

export function validBotAuthTest(): SlackScript {
  return {
    body: {
      ok: true,
      team: 'E2E Workspace',
      team_id: 'T-E2E',
      user: 'Agentico E2E',
      user_id: 'U-E2E',
      url: 'https://e2e-workspace.slack.com/',
      bot_id: 'B-E2E',
    },
    scopes: REQUIRED_SLACK_SCOPES,
  };
}

export function validUserAuthTest(): SlackScript {
  return {
    body: {
      ok: true,
      team: 'E2E Workspace',
      team_id: 'T-E2E',
      user: 'ada',
      user_id: 'U-OWNER-E2E',
      url: 'https://e2e-workspace.slack.com/',
    },
    scopes: REQUIRED_SLACK_SCOPES,
  };
}

export async function startScriptedSlackServer(): Promise<ScriptedSlackServer> {
  const queues = new Map<string, SlackScript[]>();
  const received: SlackRequest[] = [];
  const server = http.createServer((request, response) => {
    const chunks: Buffer[] = [];
    request.on('data', (chunk: Buffer) => chunks.push(chunk));
    request.on('end', () => {
      const path = new URL(request.url ?? '/', 'http://127.0.0.1').pathname;
      const method = path.startsWith('/api/')
        ? path.slice('/api/'.length)
        : path.replace(/^\//, '');
      const fields = decodeFields(Buffer.concat(chunks), request.headers['content-type']);
      received.push({
        method: request.method ?? '',
        path,
        bearerPresent: request.headers.authorization?.startsWith('Bearer ') === true,
        fields,
      });
      const queue = queues.get(method);
      const script = queue?.shift() ?? {
        body: { ok: false, error: 'unexpected_method' },
      };
      if (script.scopes !== undefined) {
        response.setHeader('X-OAuth-Scopes', script.scopes.join(','));
      }
      response.writeHead(script.status ?? 200, { 'Content-Type': 'application/json' });
      response.end(JSON.stringify(script.body));
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  if (address === null || typeof address === 'string') {
    server.close();
    throw new Error('scripted Slack server is not listening on an inet address');
  }

  return {
    baseUrl: `http://127.0.0.1:${address.port}/api/`,
    enqueue: (...scripts) => enqueue(queues, 'auth.test', scripts),
    enqueueMethod: (method, ...scripts) => enqueue(queues, method, scripts),
    requests: () => received.map((request) => ({ ...request, fields: { ...request.fields } })),
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((error) => {
          if (error === undefined) resolve();
          else reject(error);
        });
      }),
  };
}

function enqueue(
  queues: Map<string, SlackScript[]>,
  method: string,
  scripts: readonly SlackScript[],
): void {
  const queue = queues.get(method) ?? [];
  queue.push(...scripts);
  queues.set(method, queue);
}

function decodeFields(
  body: Buffer,
  contentType: string | string[] | undefined,
): Record<string, string> {
  if (body.length === 0) return {};
  const raw = body.toString('utf8');
  if (typeof contentType === 'string' && contentType.startsWith('application/json')) {
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    return Object.fromEntries(
      Object.entries(parsed).map(([key, value]) => [
        key,
        typeof value === 'string' ? value : JSON.stringify(value),
      ]),
    );
  }
  return Object.fromEntries(new URLSearchParams(raw));
}
