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

import { spawn } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs';
import { createInterface } from 'node:readline';
import { expect, test } from 'vitest';
import {
  createWorld,
  destroyWorld,
  providerInvocationCount,
  SUPERVISOR_E2E_MARKERS,
  supervisorStubReply,
  type WorldOptions,
} from './world';

const variants: [string, WorldOptions][] = [
  ['utility', {}],
  ['workflow', { workflowProvider: true }],
  ['attention', { attentionProvider: true }],
  ['rebase', { rebaseProvider: true }],
  ['supervisor', { supervisorProvider: true }],
];

test.each(variants)(
  '%s stub initializes without a user prompt or workflow activity',
  async (name, options) => {
    const world = createWorld(`catalog-${name}`, options);
    const child = spawn(world.claudeStub, [
      '-p',
      '--input-format',
      'stream-json',
      '--output-format',
      'stream-json',
    ]);
    const closed = once(child, 'close');
    const lines = createInterface({ input: child.stdout });
    try {
      const response = once(lines, 'line', { signal: AbortSignal.timeout(3000) });
      child.stdin.write(
        JSON.stringify({
          type: 'control_request',
          request_id: 'catalog-regression-test',
          request: { subtype: 'initialize' },
        }) + '\n',
      );
      const [line] = await response;
      const message = JSON.parse(line as string);
      expect(message).toMatchObject({
        type: 'control_response',
        response: {
          subtype: 'success',
          request_id: 'catalog-regression-test',
          response: {
            models: expect.arrayContaining([
              expect.objectContaining({ value: 'claude-fable-5-1[1m]' }),
              expect.objectContaining({
                value: 'sonnet',
                supportsEffort: true,
                supportedEffortLevels: ['low', 'medium', 'high'],
              }),
            ]),
          },
        },
      });
      expect(fs.existsSync(world.providerInvocationLog)).toBe(false);
    } finally {
      child.kill('SIGKILL');
      await closed;
      lines.close();
      destroyWorld(world);
    }
  },
);

test('supervisor stub serves streamed, permission and held turns from one process', async () => {
  const world = createWorld('supervisor-stub', { supervisorProvider: true });
  const child = spawn(world.claudeStub, ['--input-format', 'stream-json']);
  const closed = once(child, 'close');
  const lines = createInterface({ input: child.stdout });
  const received: Array<Record<string, unknown>> = [];
  const waiters: Array<() => void> = [];
  lines.on('line', (line) => {
    received.push(JSON.parse(line) as Record<string, unknown>);
    waiters.splice(0).forEach((wake) => wake());
  });
  const next = async (count: number): Promise<Array<Record<string, unknown>>> => {
    const deadline = Date.now() + 3000;
    while (received.length < count) {
      if (Date.now() > deadline) throw new Error(`timed out waiting for ${count} lines`);
      await new Promise<void>((resolve) => {
        waiters.push(resolve);
        setTimeout(resolve, 100);
      });
    }
    return received.splice(0, count);
  };
  const write = (message: unknown): void => {
    child.stdin.write(`${JSON.stringify(message)}\n`);
  };
  const user = (text: string) => ({
    type: 'user',
    message: { role: 'user', content: [{ type: 'text', text }] },
  });
  try {
    write({ type: 'control_request', request_id: 'init', request: { subtype: 'initialize' } });
    const [initResponse, init] = await next(2);
    expect(initResponse).toMatchObject({ type: 'control_response' });
    expect(init).toMatchObject({ type: 'system', subtype: 'init' });

    write(user('hello'));
    const firstTurn = await next(5);
    expect(firstTurn.map((line) => line['type'])).toEqual([
      'stream_event',
      'stream_event',
      'stream_event',
      'assistant',
      'result',
    ]);
    expect(JSON.stringify(firstTurn[3])).toContain(supervisorStubReply(1));
    expect(JSON.stringify(firstTurn[3])).toContain('msg-e2e-supervisor-1');

    write(user(`please ${SUPERVISOR_E2E_MARKERS.permission}`));
    const [request] = await next(1);
    expect(request).toMatchObject({
      type: 'control_request',
      request_id: 'supervisor-perm-2',
      request: { subtype: 'can_use_tool', tool_name: 'Bash' },
    });
    write({
      type: 'control_response',
      response: { subtype: 'success', request_id: 'supervisor-perm-2', response: {} },
    });
    const permissionTurn = await next(5);
    expect(JSON.stringify(permissionTurn[3])).toContain(supervisorStubReply(2));
    expect(permissionTurn[4]).toMatchObject({ type: 'result', subtype: 'success' });

    write(user(`wait ${SUPERVISOR_E2E_MARKERS.hold}`));
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(received).toHaveLength(0);
    write({ type: 'control_request', request_id: 'stop', request: { subtype: 'interrupt' } });
    const [interrupted] = await next(1);
    expect(interrupted).toMatchObject({ type: 'result', is_error: true });

    child.stdin.end();
    const [code] = await closed;
    expect(code).toBe(0);
    expect(providerInvocationCount(world.providerInvocationLog)).toBe(1);
  } finally {
    child.kill('SIGKILL');
    lines.close();
    destroyWorld(world);
  }
});
