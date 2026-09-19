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

import { describe, expect, it } from 'vitest';
import { CanonicalErrorException } from '../../shared/errors';
import type { ServerTransport } from '../serverClient';
import { ServerUpdateService, describeActiveWork, projectServerUpdate } from '../serverUpdates';

const IDLE_SUMMARY = {
  feature_count: 0,
  chat_active: false,
  clone_count: 0,
  upload_count: 0,
  origin_check_count: 0,
  pending_admissions: 0,
  parked_count: 0,
  detection_failed: false,
};

function snapshot(overrides: Record<string, unknown> = {}) {
  return {
    api_version: 'v1',
    update: {
      status: 'available',
      policy: 'auto',
      channel: 'stable',
      strategy: 'idle',
      current_version: '1.0.0',
      latest_version: '2.0.0',
      installation: 'tarball',
      signature: 'unverified',
      check_interval_seconds: 21600,
      scheduled_for: null,
      active_work_summary: IDLE_SUMMARY,
      ...overrides,
    },
  };
}

function transport(
  answer: (path: string, init: unknown) => { status: number; body: unknown },
): ServerTransport & { calls: Array<{ path: string; init: unknown }> } {
  const calls: Array<{ path: string; init: unknown }> = [];
  return {
    calls,
    apiRequest: (path, init) => {
      calls.push({ path, init });
      return Promise.resolve(answer(path, init));
    },
  };
}

describe('projectServerUpdate', () => {
  it('maps the wire snapshot onto the renderer-safe state and drops nulls', () => {
    const state = projectServerUpdate(
      snapshot({
        status: 'scheduled',
        method: 'idle',
        stop_active_work: false,
        target_version: '2.0.0',
        scheduled_for: '2026-09-17T01:00:00Z',
        active_work_summary: { ...IDLE_SUMMARY, feature_count: 2, chat_active: true },
      }),
    );
    expect(state).toMatchObject({
      status: 'scheduled',
      policy: 'auto',
      currentVersion: '1.0.0',
      latestVersion: '2.0.0',
      targetVersion: '2.0.0',
      method: 'idle',
      stopActiveWork: false,
      scheduledFor: '2026-09-17T01:00:00Z',
      activeWorkSummary: '2 features and chat active on the server.',
    });
    expect(projectServerUpdate(snapshot())).not.toHaveProperty('scheduledFor');
  });

  it('keeps a canonical server error and ignores anything else', () => {
    const error = {
      code: 'update_install_failed',
      class: 'warning',
      title: 'Install failed',
      summary: 'Signature verification failed for 2.0.0.',
    };
    expect(projectServerUpdate(snapshot({ status: 'failed', error })).error).toMatchObject({
      code: 'update_install_failed',
    });
    expect(projectServerUpdate(snapshot({ error: 'garbage' }))).not.toHaveProperty('error');
  });
});

describe('describeActiveWork', () => {
  it('summarizes live work as one sentence and stays silent when idle', () => {
    expect(describeActiveWork(IDLE_SUMMARY)).toBeUndefined();
    expect(describeActiveWork({ ...IDLE_SUMMARY, feature_count: 1 })).toBe(
      '1 feature active on the server.',
    );
    expect(describeActiveWork({ ...IDLE_SUMMARY, clone_count: 1, upload_count: 2 })).toBe(
      '3 repository operations active on the server.',
    );
    expect(describeActiveWork({ ...IDLE_SUMMARY, detection_failed: true })).toMatch(/detection/);
  });
});

describe('ServerUpdateService', () => {
  it('drives the four endpoints with the trusted mutation bodies', async () => {
    const client = transport(() => ({ status: 200, body: snapshot() }));
    const service = new ServerUpdateService(client);
    await service.get();
    await service.check();
    await service.install({ when: 'idle' });
    await service.install({ when: 'now', stopActiveWork: true });
    await service.cancel();
    expect(client.calls).toEqual([
      { path: '/api/v1/update', init: undefined },
      { path: '/api/v1/update/check', init: { method: 'POST', body: {} } },
      {
        path: '/api/v1/update/install',
        init: { method: 'POST', body: { consent: true, when: 'idle' } },
      },
      {
        path: '/api/v1/update/install',
        init: { method: 'POST', body: { consent: true, when: 'now', stop_active_work: true } },
      },
      { path: '/api/v1/update/install', init: { method: 'DELETE', body: {} } },
    ]);
  });

  it('cancels a scheduled idle wait before promoting it to install now', async () => {
    const client = transport((_, init) => ({
      status: 200,
      body:
        init === undefined
          ? snapshot({ status: 'scheduled', method: 'idle', target_version: '2.0.0' })
          : snapshot(),
    }));
    const service = new ServerUpdateService(client);
    await service.get();
    await service.install({ when: 'now' });
    expect(client.calls.slice(1).map((call) => (call.init as { method: string }).method)).toEqual([
      'DELETE',
      'POST',
    ]);
  });

  it('reads a server without the endpoints as unsupported and exposes the switcher badge', async () => {
    const older = new ServerUpdateService(transport(() => ({ status: 404, body: undefined })));
    await expect(older.get()).resolves.toMatchObject({ status: 'unsupported' });
    expect(older.switcherBadge()).toBeUndefined();

    const current = new ServerUpdateService(transport(() => ({ status: 200, body: snapshot() })));
    expect(current.switcherBadge()).toBeUndefined();
    await current.get();
    expect(current.switcherBadge()).toEqual({ available: true, latest: '2.0.0' });
  });

  it('maps a refused mutation onto the canonical error', async () => {
    const service = new ServerUpdateService(
      transport(() => ({
        status: 409,
        body: {
          api_version: 'v1',
          error: {
            code: 'update_blocked_active_work',
            class: 'warning',
            title: 'Work is active',
            summary: 'Two features are still running.',
          },
        },
      })),
    );
    await expect(service.install({ when: 'now' })).rejects.toBeInstanceOf(CanonicalErrorException);
  });
});
