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

import { execFileSync, spawn } from 'node:child_process';
import { once } from 'node:events';
import fs from 'node:fs';
import path from 'node:path';
import { createInterface } from 'node:readline';
import { expect, test } from 'vitest';
import {
  createWorld,
  destroyWorld,
  providerInvocationCount,
  SUPERVISOR_E2E_HELPER_LOG_PREFIX,
  SUPERVISOR_E2E_MARKERS,
  SUPERVISOR_E2E_OPERATE_CONFIG,
  SUPERVISOR_E2E_OPERATE_STARTED_REPLY,
  supervisorOperateCreatedReply,
  supervisorOperateCreateMarker,
  supervisorStubAttachmentLogLines,
  supervisorStubPartialReply,
  supervisorStubReply,
  supervisorStubResumedReply,
  type WorldOptions,
} from './world';

const variants: [string, WorldOptions][] = [
  ['utility', {}],
  ['workflow', { workflowProvider: true }],
  ['attention', { attentionProvider: true }],
  ['rebase', { rebaseProvider: true }],
  ['supervisor', { supervisorProvider: true }],
];

test('unlaunchable Codex is detectable and records app-server attempts', () => {
  const world = createWorld('codex-unlaunchable', { unlaunchableCodex: true });
  try {
    expect(execFileSync(world.codexStub, ['--version'], { encoding: 'utf8' })).toContain(
      'codex-cli',
    );
    expect(execFileSync(world.codexStub, ['login', 'status'], { encoding: 'utf8' })).toContain(
      'Logged in',
    );
    expect(
      execFileSync(world.codexStub, ['debug', 'models', '--bundled'], { encoding: 'utf8' }),
    ).toContain('gpt-5.4');
    expect(() => execFileSync(world.codexStub, ['app-server'], { stdio: 'ignore' })).toThrow();
    expect(fs.readFileSync(world.codexInvocationLog, 'utf8')).toContain('app-server');
    expect(fs.readFileSync(world.configPath, 'utf8')).toContain(world.codexStub);
  } finally {
    destroyWorld(world);
  }
});

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

/** A line-oriented driver over one spawned stub process. */
function driveStub(
  stub: string,
  env: NodeJS.ProcessEnv,
  options: { args?: string[]; cwd?: string } = {},
) {
  const child = spawn(stub, ['--input-format', 'stream-json', ...(options.args ?? [])], {
    env,
    cwd: options.cwd,
  });
  const closed = once(child, 'close');
  const lines = createInterface({ input: child.stdout });
  const received: Array<Record<string, unknown>> = [];
  const waiters: Array<() => void> = [];
  lines.on('line', (line) => {
    received.push(JSON.parse(line) as Record<string, unknown>);
    waiters.splice(0).forEach((wake) => wake());
  });
  return {
    child,
    closed,
    lines,
    received,
    write(message: unknown): void {
      child.stdin.write(`${JSON.stringify(message)}\n`);
    },
    async next(count: number): Promise<Array<Record<string, unknown>>> {
      const deadline = Date.now() + 5000;
      while (received.length < count) {
        if (Date.now() > deadline) throw new Error(`timed out waiting for ${count} lines`);
        await new Promise<void>((resolve) => {
          waiters.push(resolve);
          setTimeout(resolve, 100);
        });
      }
      return received.splice(0, count);
    },
  };
}

const userTurn = (text: string) => ({
  type: 'user',
  message: { role: 'user', content: [{ type: 'text', text }] },
});

test('supervisor stub operates Agentico through the helper with tool records', async () => {
  const world = createWorld('supervisor-operate', {
    supervisorProvider: true,
    workflowProvider: true,
  });
  // A stand-in for the bundled binary: records argv, answers like the helper.
  const argvLog = path.join(world.stubDir, 'fake-agentico-argv.log');
  const fakeBin = path.join(world.stubDir, 'fake-agentico');
  fs.writeFileSync(
    fakeBin,
    [
      '#!/bin/sh',
      `printf '%s|' "$@" >> "${argvLog}"`,
      `printf '\\n' >> "${argvLog}"`,
      'case "$2 $3" in',
      `  "POST /api/v1/features") printf '%s\\n' '{"feature_id":"feat-operate-1","result":"created"}' ;;`,
      `  *) printf '%s\\n' '{"note":"back\\\\slash \\"quoted\\""}' 'second line' ;;`,
      'esac',
      '',
    ].join('\n'),
    { mode: 0o755 },
  );
  const stub = driveStub(world.claudeStub, {
    PATH: '/usr/bin:/bin',
    AGENTICO_BIN: fakeBin,
    AGENTICO_RUNTIME_DIR: world.runtimeDir,
  });
  try {
    stub.write({ type: 'control_request', request_id: 'init', request: { subtype: 'initialize' } });
    const [, init] = await stub.next(2);
    expect(init).toMatchObject({ session_id: 'e2e-supervisor-session' });

    stub.write(userTurn(`make one ${supervisorOperateCreateMarker('Operate Lab')}`));
    const create = await stub.next(10);
    expect(create.map((line) => line['type'])).toEqual([
      'assistant',
      'user',
      'assistant',
      'user',
      'assistant',
      'user',
      'assistant',
      'user',
      'assistant',
      'result',
    ]);
    type Block = Record<string, unknown> & { input?: { command?: string } };
    const block = (index: number): Block =>
      (create[index] as { message: { content: Block[] } }).message.content[0]!;
    const commands = [0, 2, 4, 6].map(block);
    for (const tool of commands) expect(tool).toMatchObject({ type: 'tool_use', name: 'Bash' });
    expect(commands.map((tool) => tool.input?.command)).toEqual([
      `"$AGENTICO_BIN" api POST /api/v1/features '{"name":"Operate Lab"}'`,
      `"$AGENTICO_BIN" api POST /api/v1/features/feat-operate-1/actions/setup '{}'`,
      `"$AGENTICO_BIN" api POST /api/v1/features/feat-operate-1/config '${JSON.stringify(SUPERVISOR_E2E_OPERATE_CONFIG)}'`,
      `"$AGENTICO_BIN" api GET /api/v1/features/feat-operate-1`,
    ]);
    const results = [1, 3, 5, 7].map(block);
    expect(results[0]).toMatchObject({
      type: 'tool_result',
      tool_use_id: commands[0]!['id'],
      content: '{"feature_id":"feat-operate-1","result":"created"}',
      is_error: false,
    });
    // Quotes, backslashes and newlines in helper output survive as JSON.
    expect(results[3]).toMatchObject({
      content: '{"note":"back\\\\slash \\"quoted\\""}\nsecond line',
    });
    expect(JSON.stringify(create[8])).toContain(supervisorOperateCreatedReply('Operate Lab'));
    expect(create[9]).toMatchObject({ type: 'result', subtype: 'success' });

    stub.write(userTurn(`go ${SUPERVISOR_E2E_MARKERS.operateStart}`));
    const start = await stub.next(4);
    expect(start.map((line) => line['type'])).toEqual(['assistant', 'user', 'assistant', 'result']);
    expect(JSON.stringify(start[0])).toContain('/api/v1/features/feat-operate-1/actions/start');
    expect(JSON.stringify(start[2])).toContain(SUPERVISOR_E2E_OPERATE_STARTED_REPLY);

    expect(fs.readFileSync(argvLog, 'utf8').split('\n').filter(Boolean)).toEqual([
      'api|POST|/api/v1/features|{"name":"Operate Lab"}|',
      'api|POST|/api/v1/features/feat-operate-1/actions/setup|{}|',
      `api|POST|/api/v1/features/feat-operate-1/config|${JSON.stringify(SUPERVISOR_E2E_OPERATE_CONFIG)}|`,
      'api|GET|/api/v1/features/feat-operate-1|',
      'api|POST|/api/v1/features/feat-operate-1/actions/start|{}|',
    ]);
    const log = fs.readFileSync(world.providerInvocationLog, 'utf8');
    const helperLines = log
      .split('\n')
      .filter((line) => line.startsWith(SUPERVISOR_E2E_HELPER_LOG_PREFIX));
    expect(helperLines).toHaveLength(5);
    expect(log).toContain('operate-feature:feat-operate-1');
    expect(log).toMatch(/helper-exit:5:0/);

    stub.child.stdin.end();
    const [code] = await stub.closed;
    expect(code).toBe(0);
  } finally {
    stub.child.kill('SIGKILL');
    stub.lines.close();
    destroyWorld(world);
  }
});

test('a combined world serves workflow sessions to children without the runtime-dir variable', async () => {
  const world = createWorld('supervisor-workflow', {
    supervisorProvider: true,
    workflowProvider: true,
  });
  const stub = driveStub(world.claudeStub, { PATH: '/usr/bin:/bin' });
  try {
    stub.write({ type: 'control_request', request_id: 'init', request: { subtype: 'initialize' } });
    const [response] = await stub.next(1);
    expect(response).toMatchObject({ type: 'control_response' });
    stub.write(userTurn('Implementation Context'));
    const [init] = await stub.next(1);
    expect(init).toMatchObject({ type: 'system', session_id: 'e2e-workflow-session' });
  } finally {
    stub.child.kill('SIGKILL');
    await stub.closed;
    stub.lines.close();
    destroyWorld(world);
  }
});

test('supervisor stub commits partial text, then holds the turn without a result', async () => {
  const world = createWorld('supervisor-partial', { supervisorProvider: true });
  const stub = driveStub(world.claudeStub, { PATH: '/usr/bin:/bin', HOME: world.home });
  try {
    stub.write({ type: 'control_request', request_id: 'init', request: { subtype: 'initialize' } });
    await stub.next(2);
    stub.write(userTurn(`go ${SUPERVISOR_E2E_MARKERS.partialHold}`));
    const [partial] = await stub.next(1);
    expect(partial).toMatchObject({
      type: 'assistant',
      message: { content: [{ type: 'text', text: supervisorStubPartialReply(1) }] },
    });
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(stub.received).toHaveLength(0);
    expect(fs.readFileSync(world.providerInvocationLog, 'utf8')).toContain('partial-holding:1');
    stub.write({ type: 'control_request', request_id: 'stop', request: { subtype: 'interrupt' } });
    const [interrupted] = await stub.next(1);
    expect(interrupted).toMatchObject({ type: 'result', is_error: true });
  } finally {
    stub.child.kill('SIGKILL');
    stub.lines.close();
    destroyWorld(world);
  }
});

test('supervisor stub logs each attached path with its first line', async () => {
  const world = createWorld('supervisor-attachments', { supervisorProvider: true });
  const attachments = path.join(world.root, 'conversation', 'attachments');
  fs.mkdirSync(attachments, { recursive: true });
  const image = path.join(attachments, '0123456789abcdef0123456789abcdef.png');
  const notes = path.join(attachments, 'fedcba9876543210fedcba9876543210.txt');
  const missing = path.join(attachments, '00112233445566778899aabbccddeeff.md');
  fs.writeFileSync(image, 'PNG header line\n\u0000binary');
  fs.writeFileSync(notes, 'Notes first line\nsecond line\n');
  const stub = driveStub(world.claudeStub, { PATH: '/usr/bin:/bin', HOME: world.home });
  try {
    stub.write({ type: 'control_request', request_id: 'init', request: { subtype: 'initialize' } });
    await stub.next(2);
    stub.write({
      type: 'user',
      message: {
        role: 'user',
        content: [
          'Review these',
          '',
          'Attached Images:',
          `- [Image #1]: ${image}`,
          '',
          'Attached Files:',
          `- [notes.txt]: ${notes}`,
          `- [spec.md]: ${missing}`,
        ].join('\n'),
      },
    });
    const reply = await stub.next(5);
    expect(JSON.stringify(reply[3])).toContain(supervisorStubReply(1));
    // A blank-text message is only the block; block content arrays work too.
    stub.write(userTurn(`Attached Files:\n- [notes.txt]: ${notes}`));
    await stub.next(5);
    stub.write(userTurn('no attachments, only a mention of Attached Files: in prose'));
    await stub.next(5);
    const log = fs.readFileSync(world.providerInvocationLog, 'utf8').split('\n');
    const attachmentLines = log.filter((line) => line.startsWith('attachment'));
    expect(attachmentLines).toEqual([
      ...supervisorStubAttachmentLogLines(1, image, 'PNG header line'),
      ...supervisorStubAttachmentLogLines(1, notes, 'Notes first line'),
      `attachment:1:${missing}`,
      `attachment-unreadable:1:${missing}`,
      ...supervisorStubAttachmentLogLines(2, notes, 'Notes first line'),
    ]);
  } finally {
    stub.child.kill('SIGKILL');
    stub.lines.close();
    destroyWorld(world);
  }
});

test('supervisor stub resumes from the rebuilt session file under the world home', async () => {
  const world = createWorld('supervisor-resume', { supervisorProvider: true });
  const workdir = path.join(world.root, 'work.dir');
  fs.mkdirSync(workdir);
  const encoded = fs.realpathSync(workdir).replace(/[/.]/g, '-');
  const projects = path.join(world.home, '.claude', 'projects', encoded);
  fs.mkdirSync(projects, { recursive: true });
  const nativeId = '6f1c2d3e-0000-4000-8000-000000000001';
  const line = (record: unknown): string => JSON.stringify(record);
  fs.writeFileSync(
    path.join(projects, `${nativeId}.jsonl`),
    [
      line({ type: 'user', message: { role: 'user', content: 'first prompt' } }),
      line({ type: 'assistant', message: { role: 'assistant', content: [{ type: 'text' }] } }),
      line({ type: 'user', message: { role: 'user', content: [{ type: 'tool_result' }] } }),
      line({ type: 'user', message: { role: 'user', content: 'second prompt' } }),
      '',
    ].join('\n'),
  );
  const stub = driveStub(
    world.claudeStub,
    { PATH: '/usr/bin:/bin', HOME: world.home },
    { args: ['--resume', nativeId], cwd: workdir },
  );
  try {
    stub.write({ type: 'control_request', request_id: 'init', request: { subtype: 'initialize' } });
    const [, init] = await stub.next(2);
    expect(init).toMatchObject({ type: 'system', subtype: 'init', session_id: nativeId });
    stub.write(userTurn('continue'));
    const reply = await stub.next(5);
    expect(JSON.stringify(reply[3])).toContain(supervisorStubResumedReply(2));
    stub.write(userTurn('and again'));
    const next = await stub.next(5);
    expect(JSON.stringify(next[3])).toContain(supervisorStubReply(2));
    const log = fs.readFileSync(world.providerInvocationLog, 'utf8').split('\n');
    expect(log).toContain(`resume:${nativeId}`);
    expect(log).toContain('history:2');
  } finally {
    stub.child.kill('SIGKILL');
    stub.lines.close();
    destroyWorld(world);
  }
});

test('supervisor stub fails a supervisor launch before any output while the sentinel exists', async () => {
  const world = createWorld('supervisor-launch-failure', { supervisorProvider: true });
  fs.writeFileSync(world.supervisorLaunchFailurePath, '');
  const failing = driveStub(world.claudeStub, {
    PATH: '/usr/bin:/bin',
    HOME: world.home,
    AGENTICO_RUNTIME_DIR: world.runtimeDir,
  });
  try {
    const [code] = await failing.closed;
    expect(code).toBe(3);
    expect(failing.received).toHaveLength(0);
    expect(fs.readFileSync(world.providerInvocationLog, 'utf8')).toContain('launch-failed');
  } finally {
    failing.child.kill('SIGKILL');
    failing.lines.close();
  }
  // A catalog probe (no runtime-dir variable) still answers, and so does a
  // supervisor launch once the sentinel is gone.
  fs.rmSync(world.supervisorLaunchFailurePath);
  const healthy = driveStub(world.claudeStub, {
    PATH: '/usr/bin:/bin',
    HOME: world.home,
    AGENTICO_RUNTIME_DIR: world.runtimeDir,
  });
  try {
    healthy.write({
      type: 'control_request',
      request_id: 'init',
      request: { subtype: 'initialize' },
    });
    const [response] = await healthy.next(2);
    expect(response).toMatchObject({ type: 'control_response' });
  } finally {
    healthy.child.kill('SIGKILL');
    healthy.lines.close();
    destroyWorld(world);
  }
});
