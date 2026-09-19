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
}

export interface ScriptedSlackServer {
  baseUrl: string;
  enqueue(...scripts: SlackScript[]): void;
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

export async function startScriptedSlackServer(): Promise<ScriptedSlackServer> {
  const queue: SlackScript[] = [];
  const received: SlackRequest[] = [];
  const server = http.createServer((request, response) => {
    received.push({
      method: request.method ?? '',
      path: request.url ?? '',
      bearerPresent: request.headers.authorization?.startsWith('Bearer ') === true,
    });
    const script = queue.shift() ?? {
      body: { ok: false, error: 'unexpected_auth_test' },
    };
    if (script.scopes !== undefined) {
      response.setHeader('X-OAuth-Scopes', script.scopes.join(','));
    }
    response.writeHead(script.status ?? 200, { 'Content-Type': 'application/json' });
    response.end(JSON.stringify(script.body));
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  if (address === null || typeof address === 'string') {
    server.close();
    throw new Error('scripted Slack server is not listening on an inet address');
  }

  return {
    baseUrl: `http://127.0.0.1:${address.port}/api/`,
    enqueue: (...scripts) => queue.push(...scripts),
    requests: () => received.map((request) => ({ ...request })),
    close: () =>
      new Promise<void>((resolve, reject) => {
        server.close((error) => {
          if (error === undefined) resolve();
          else reject(error);
        });
      }),
  };
}
