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
import { afterEach, describe, expect, it } from 'vitest';
import { startScriptedSlackServer, type ScriptedSlackServer } from './slack';

function post(baseUrl: string, method: string, fields: Record<string, string>) {
  const url = new URL(method, baseUrl);
  const body = new URLSearchParams(fields).toString();
  return new Promise<Record<string, unknown>>((resolve, reject) => {
    const request = http.request(
      url,
      {
        method: 'POST',
        headers: {
          authorization: 'Bearer xoxb-test',
          'content-type': 'application/x-www-form-urlencoded',
          'content-length': Buffer.byteLength(body),
        },
      },
      (response) => {
        const chunks: Buffer[] = [];
        response.on('data', (chunk: Buffer) => chunks.push(chunk));
        response.on('end', () => {
          resolve(JSON.parse(Buffer.concat(chunks).toString('utf8')) as Record<string, unknown>);
        });
      },
    );
    request.on('error', reject);
    request.end(body);
  });
}

describe('scripted Slack server', () => {
  let server: ScriptedSlackServer | null = null;

  afterEach(async () => {
    if (server !== null) await server.close();
    server = null;
  });

  it('serves independent method queues and records decoded fields', async () => {
    server = await startScriptedSlackServer();
    server.enqueueMethod('users.lookupByEmail', {
      body: { ok: true, user: { id: 'U12345678' } },
    });
    server.enqueueMethod('conversations.list', {
      body: { ok: true, channels: [{ id: 'C12345678' }] },
    });

    await expect(
      post(server.baseUrl, 'conversations.list', { limit: '200', cursor: 'next' }),
    ).resolves.toMatchObject({ ok: true });
    await expect(
      post(server.baseUrl, 'users.lookupByEmail', { email: 'ada@example.com' }),
    ).resolves.toMatchObject({ ok: true });
    await expect(post(server.baseUrl, 'chat.postMessage', { channel: 'C999' })).resolves.toEqual({
      ok: false,
      error: 'unexpected_method',
    });

    expect(server.requests()).toStrictEqual([
      {
        method: 'POST',
        path: '/api/conversations.list',
        bearerPresent: true,
        fields: { limit: '200', cursor: 'next' },
      },
      {
        method: 'POST',
        path: '/api/users.lookupByEmail',
        bearerPresent: true,
        fields: { email: 'ada@example.com' },
      },
      {
        method: 'POST',
        path: '/api/chat.postMessage',
        bearerPresent: true,
        fields: { channel: 'C999' },
      },
    ]);
  });
});
