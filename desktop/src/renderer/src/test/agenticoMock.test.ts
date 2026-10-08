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

import { describe, expect, it, vi } from 'vitest';
import { SupervisorEventSchema, SupervisorStateSchema } from '../../../shared/ipc';
import {
  installAgenticoMock,
  supervisorPendingPermission,
  supervisorPendingQuestion,
  supervisorRecord,
  supervisorState,
} from './agenticoMock';

describe('installAgenticoMock supervisor surface', () => {
  it('builds schema-valid root and sub-agent pending requests', () => {
    const child = { origin: 'child' as const, childSessionId: 'agent_sub_1' };
    const state = supervisorState({
      lifecycle: 'waiting_permission',
      pendingRequests: [
        supervisorPendingPermission(),
        supervisorPendingPermission({ id: 'perm-child', ...child }),
        supervisorPendingQuestion({ id: 'ask-child', ...child }),
      ],
    });
    expect(SupervisorStateSchema.safeParse(state).success).toBe(true);
    expect(state.pendingRequests.map((item) => item.origin)).toEqual(['root', 'child', 'child']);
  });

  it('defaults to a stopped, unconfigured supervisor with an empty transcript', async () => {
    const mock = installAgenticoMock();

    await expect(window.agentico.getSupervisorState()).resolves.toEqual(supervisorState());
    await expect(window.agentico.getSupervisorTranscript({})).resolves.toMatchObject({
      items: [],
      headSeq: 0,
      hasMoreBefore: false,
      hasMoreAfter: false,
    });
    expect(mock.supervisorState()).toMatchObject({
      lifecycle: 'stopped',
      settings: { harness: '', model: '', effort: '' },
      pendingRequests: [],
    });
  });

  it('applies settings, sends deterministically, and ends', async () => {
    const mock = installAgenticoMock();

    await window.agentico.updateSupervisorSettings({
      harness: 'claude',
      model: 'claude-opus',
      effort: '',
      requestId: 'req-1',
      expectedGeneration: 0,
    });
    const sent = await window.agentico.sendSupervisorMessage({ text: 'Hi' });
    expect(sent.launched).toBe(true);
    expect(sent.record).toMatchObject({ seq: 1, kind: 'user', messages: [{ text: 'Hi' }] });
    expect(mock.supervisorState()).toMatchObject({
      lifecycle: 'running',
      headSeq: 1,
      generation: 1,
      settings: { harness: 'claude', model: 'claude-opus', effort: '' },
    });
    await expect(window.agentico.endSupervisor()).resolves.toMatchObject({ result: 'ended' });
    await expect(window.agentico.endSupervisor()).resolves.toMatchObject({
      result: 'not_active',
    });
  });

  it('commits only the visible text of a referenced send and rejects a malformed reference', async () => {
    const mock = installAgenticoMock();

    const sent = await window.agentico.sendSupervisorMessage({
      text: 'Explain this',
      errorReference: { scope: 'run', code: 'run_failed', featureId: 'abcd1234' },
    });
    expect(sent.record.messages).toEqual([
      { index: 1, role: 'user', type: 'text', text: 'Explain this' },
    ]);
    await expect(
      window.agentico.sendSupervisorMessage({
        text: 'Explain this',
        errorReference: { scope: 'run', code: 'run_failed' },
      }),
    ).rejects.toThrow();
    expect(mock.supervisorState().headSeq).toBe(1);
  });

  it('pushes supervisor events through a registry with exact unsubscribe', () => {
    const mock = installAgenticoMock();
    const listener = vi.fn();
    const unsubscribe = window.agentico.onSupervisorEvent(listener);
    expect(mock.supervisorEventListenerCount()).toBe(1);

    const event = {
      type: 'record' as const,
      conversationId: 'supervisor-conversation-1',
      generation: 1,
      streamEpoch: 'supervisor-epoch-1',
      record: supervisorRecord({ seq: 2 }),
    };
    expect(SupervisorEventSchema.safeParse(event).success).toBe(true);
    mock.emitSupervisorEvent(event);
    mock.emitSupervisorEvent({ type: 'reset' });
    expect(listener.mock.calls).toEqual([[event], [{ type: 'reset' }]]);

    unsubscribe();
    expect(mock.supervisorEventListenerCount()).toBe(0);
  });
});
