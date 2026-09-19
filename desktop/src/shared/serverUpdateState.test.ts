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
import type { ServerUpdateState } from './ipc';
import {
  serverUpdatePolicyLabel,
  serverUpdateStatusLabel,
  serverUpdateSummary,
  serverUpdateVerbs,
} from './serverUpdateState';

function state(overrides: Partial<ServerUpdateState> = {}): ServerUpdateState {
  return {
    status: 'available',
    policy: 'notify',
    currentVersion: '1.0.0',
    latestVersion: '2.0.0',
    installation: 'tarball',
    signature: 'unverified',
    ...overrides,
  };
}

describe('serverUpdateVerbs', () => {
  it('offers both install verbs for an available release under notify', () => {
    expect(serverUpdateVerbs(state())).toEqual({
      check: true,
      installWhenIdle: true,
      installNow: true,
      installNowStopsWork: false,
      cancel: false,
    });
  });

  it('hides "install when idle" under auto because the server already waits for idle', () => {
    const verbs = serverUpdateVerbs(state({ policy: 'auto' }));
    expect(verbs.installWhenIdle).toBe(false);
    expect(verbs.installNow).toBe(true);
  });

  it('requires consent to install now while work is active', () => {
    expect(
      serverUpdateVerbs(state({ activeWorkSummary: '1 feature active on the server.' })),
    ).toMatchObject({ installNow: true, installNowStopsWork: true });
  });

  it('offers cancel and install now, but not check, while an idle install waits', () => {
    expect(
      serverUpdateVerbs(state({ status: 'scheduled', method: 'idle', targetVersion: '2.0.0' })),
    ).toMatchObject({ check: false, installWhenIdle: false, installNow: true, cancel: true });
  });

  it('offers nothing once the server is draining, unsupported, or disabled', () => {
    for (const status of ['draining', 'restarting', 'unsupported', 'disabled'] as const) {
      const verbs = serverUpdateVerbs(state({ status }));
      expect(verbs.installNow).toBe(false);
      expect(verbs.cancel).toBe(false);
    }
  });

  it('offers only a check when the server is up to date', () => {
    expect(serverUpdateVerbs(state({ status: 'up_to_date', latestVersion: '1.0.0' }))).toEqual({
      check: true,
      installWhenIdle: false,
      installNow: false,
      installNowStopsWork: false,
      cancel: false,
    });
  });
});

describe('server update copy', () => {
  it('names the policy and the waiting state in plain words', () => {
    expect(serverUpdatePolicyLabel(state({ policy: 'auto' }))).toBe(
      'Installs automatically when idle',
    );
    expect(serverUpdateStatusLabel(state({ status: 'scheduled' }))).toBe('Waiting for idle');
    expect(
      serverUpdateStatusLabel(
        state({ status: 'scheduled', scheduledFor: '2026-09-17T01:00:00.000Z' }),
      ),
    ).toBe('Waiting for window');
    expect(serverUpdateSummary(state({ policy: 'auto' }))).toBe(
      'Version 2.0.0 will install automatically when the server is idle.',
    );
    expect(serverUpdateSummary(state({ status: 'unsupported', remediation: 'Use brew.' }))).toBe(
      'Use brew.',
    );
  });
});
