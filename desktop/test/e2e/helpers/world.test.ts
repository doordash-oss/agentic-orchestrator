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
import { createWorld, destroyWorld, type WorldOptions } from './world';

const variants: [string, WorldOptions][] = [
  ['utility', {}],
  ['workflow', { workflowProvider: true }],
  ['attention', { attentionProvider: true }],
  ['rebase', { rebaseProvider: true }],
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
