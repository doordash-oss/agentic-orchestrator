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

import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { DiagnosticsService } from '../diagnostics';

let dir: string;

beforeEach(() => {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-diagnostics-'));
});

afterEach(() => {
  fs.rmSync(dir, { recursive: true, force: true });
});

describe('DiagnosticsService', () => {
  it('retains bounded redacted diagnostics without exposing the diagnostics root', () => {
    const service = new DiagnosticsService({
      userDataDir: dir,
      version: '0.1.0',
      now: () => new Date('2026-07-20T10:00:00.000Z'),
      readServerLines: () => ['Bearer tok-secret failed at /Users/alice/project'],
    });

    service.record(
      'electron',
      'warn',
      'Open failed with Bearer tok-secret',
      'path=/Users/alice/project',
    );
    const snapshot = service.snapshot();

    expect(snapshot.retention.maxAgeDays).toBe(7);
    expect(snapshot.retention.maxBytes).toBe(25 * 1024 * 1024);
    expect(snapshot.entries.length).toBe(2);
    expect(JSON.stringify(snapshot)).not.toContain('tok-secret');
    expect(JSON.stringify(snapshot)).not.toContain('/Users/alice');
    expect(JSON.stringify(snapshot)).not.toContain('diagnosticsRoot');
    expect(fs.statSync(path.join(dir, 'diagnostics')).mode & 0o777).toBe(0o700);
  });

  it('drops blank server log lines before applying the renderer schema', () => {
    const service = new DiagnosticsService({
      userDataDir: dir,
      version: '0.1.0',
      now: () => new Date('2026-07-20T10:00:00.000Z'),
      readServerLines: () => ['', '   ', 'server ready', '\t', 'retrying connection'],
    });

    const snapshot = service.snapshot();

    expect(snapshot.entries.map((entry) => entry.message)).toEqual([
      'server ready',
      'retrying connection',
    ]);
    expect(snapshot.entries.every((entry) => entry.message.length > 0)).toBe(true);
  });

  it('stores only metadata for crashes and clears only the diagnostics root', () => {
    const service = new DiagnosticsService({
      userDataDir: dir,
      version: '0.1.0',
      platform: 'darwin',
      arch: 'arm64',
      now: () => new Date('2026-07-20T10:00:00.000Z'),
    });
    fs.mkdirSync(path.join(dir, 'features'), { recursive: true });
    fs.writeFileSync(path.join(dir, 'features', 'state.txt'), 'server-owned');

    service.recordCrash({ processRole: 'renderer', category: 'crashed', context: 'exitCode=9' });
    expect(service.snapshot().crashes[0]).toMatchObject({
      version: '0.1.0',
      platform: 'darwin',
      architecture: 'arm64',
      processRole: 'renderer',
      category: 'crashed',
    });

    const cleared = service.clear();
    expect(cleared.entries).toEqual([]);
    expect(cleared.crashes).toEqual([]);
    expect(fs.readFileSync(path.join(dir, 'features', 'state.txt'), 'utf8')).toBe('server-owned');
  });

  it('prunes old entries and caps retained events and crash metadata on snapshot', () => {
    let now = new Date('2026-07-01T10:00:00.000Z');
    const service = new DiagnosticsService({
      userDataDir: dir,
      version: '0.1.0',
      now: () => now,
    });

    service.record('electron', 'info', 'old event');
    now = new Date('2026-07-20T10:00:00.000Z');
    for (let i = 0; i < 210; i += 1) {
      service.record('update', 'info', `new event ${i}`);
    }
    for (let i = 0; i < 12; i += 1) {
      service.recordCrash({ processRole: 'main', category: `crash ${i}` });
    }

    const snapshot = service.snapshot();
    expect(snapshot.entries).toHaveLength(200);
    expect(snapshot.entries.some((entry) => entry.message === 'old event')).toBe(false);
    expect(snapshot.crashes).toHaveLength(10);
    expect(snapshot.retention.crashCount).toBe(10);
    expect(fs.readFileSync(path.join(dir, 'diagnostics', 'events.jsonl'), 'utf8')).not.toContain(
      'old event',
    );
  });

  it('drops persisted records that exceed the renderer diagnostics contract', () => {
    const diagnosticsRoot = path.join(dir, 'diagnostics');
    fs.mkdirSync(diagnosticsRoot, { recursive: true });
    fs.writeFileSync(
      path.join(diagnosticsRoot, 'events.jsonl'),
      `${JSON.stringify({
        id: 'evt-valid',
        time: '2026-07-20T10:00:00.000Z',
        source: 'update',
        level: 'info',
        message: 'valid event',
      })}\n${JSON.stringify({
        id: 'evt-oversized',
        time: '2026-07-20T10:00:00.000Z',
        source: 'update',
        level: 'info',
        message: 'oversized event',
        detail: 'x'.repeat(1201),
      })}\n`,
    );
    fs.writeFileSync(
      path.join(diagnosticsRoot, 'crashes.json'),
      `${JSON.stringify([
        {
          id: 'crash-valid',
          time: '2026-07-20T10:00:00.000Z',
          version: '0.1.0',
          platform: 'darwin',
          architecture: 'arm64',
          processRole: 'main',
          category: 'valid crash',
        },
        {
          id: 'crash-oversized',
          time: '2026-07-20T10:00:00.000Z',
          version: '0.1.0',
          platform: 'darwin',
          architecture: 'arm64',
          processRole: 'main',
          category: 'x'.repeat(81),
        },
      ])}\n`,
    );

    const service = new DiagnosticsService({
      userDataDir: dir,
      version: '0.1.0',
      now: () => new Date('2026-07-20T10:00:00.000Z'),
    });
    const snapshot = service.snapshot();

    expect(snapshot.entries.map((entry) => entry.id)).toEqual(['evt-valid']);
    expect(snapshot.crashes.map((crash) => crash.id)).toEqual(['crash-valid']);
    expect(fs.readFileSync(path.join(diagnosticsRoot, 'events.jsonl'), 'utf8')).not.toContain(
      'evt-oversized',
    );
    expect(fs.readFileSync(path.join(diagnosticsRoot, 'crashes.json'), 'utf8')).not.toContain(
      'crash-oversized',
    );
  });
});
