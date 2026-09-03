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
import type { AttentionSnapshot } from '../../shared/ipc';
import { AttentionNotificationCoordinator, type NotificationHandle } from '../notifications';

describe('AttentionNotificationCoordinator', () => {
  it('reveals the application without routing into the attention drawer', () => {
    let activateNotification: (() => void) | undefined;
    const notification: NotificationHandle = {
      show: vi.fn(),
      on: vi.fn((_event, listener) => {
        activateNotification = listener;
      }),
    };
    const show = vi.fn();
    const route = vi.fn();
    const deps = {
      sink: {
        isSupported: () => true,
        create: vi.fn(() => notification),
      },
      shouldNotify: () => true,
      show,
      route,
    };
    const coordinator = new AttentionNotificationCoordinator(deps);
    const snapshot: AttentionSnapshot = {
      items: [
        {
          kind: 'permission',
          id: 'permission-1',
          featureId: 'feature-1',
          toolName: 'Bash',
          waitingSince: '2026-07-22T12:00:00.000Z',
        },
      ],
    };

    coordinator.update(snapshot, {
      previewEnabled: false,
      featureLabel: () => 'Search revamp',
    });
    activateNotification?.();

    expect(show).toHaveBeenCalledOnce();
    expect(route).not.toHaveBeenCalled();
  });

  it('never notifies for synthetic help items but still notifies real help requests', () => {
    const notification: NotificationHandle = { show: vi.fn(), on: vi.fn() };
    const create = vi.fn((_options: { title: string; body: string }) => notification);
    const coordinator = new AttentionNotificationCoordinator({
      sink: { isSupported: () => true, create },
      shouldNotify: () => true,
      show: vi.fn(),
    });
    const snapshot: AttentionSnapshot = {
      items: [
        {
          kind: 'help',
          id: 'feature-1:sess-1',
          featureId: 'feature-1',
          waitingSince: '2026-07-22T12:00:00.000Z',
          prompt: 'Agent has a question',
          waitingKind: 'coordinating',
        },
        {
          kind: 'help',
          id: '__chat__:',
          sessionId: '__chat__',
          waitingSince: '2026-07-22T12:00:30.000Z',
          prompt: 'Agent has a question',
          waitingKind: 'input',
        },
        {
          kind: 'help',
          id: 'feature-2:sess-2',
          featureId: 'feature-2',
          waitingSince: '2026-07-22T12:01:00.000Z',
          prompt: 'Which deploy target?',
          waitingKind: 'question',
        },
      ],
    };

    coordinator.update(snapshot, { previewEnabled: true, featureLabel: () => 'Search revamp' });

    expect(create).toHaveBeenCalledOnce();
    expect(create.mock.calls[0]?.[0].body).toContain('Help request');
  });
});

describe('AttentionNotificationCoordinator error items', () => {
  function errorItem() {
    return {
      kind: 'error' as const,
      id: 'error:feature-1:run::iteration_budget_exhausted',
      featureId: 'feature-1',
      waitingSince: '2026-08-05T12:00:00.000Z',
      ref: { scope: 'run' as const, code: 'iteration_budget_exhausted', featureId: 'feature-1' },
      class: 'blocking' as const,
      code: 'iteration_budget_exhausted',
      title: 'Iteration budget exhausted',
    };
  }

  it('notifies once while pending and again after the item clears and returns', () => {
    const notification: NotificationHandle = { show: vi.fn(), on: vi.fn() };
    const create = vi.fn((_options: { title: string; body: string }) => notification);
    const coordinator = new AttentionNotificationCoordinator({
      sink: { isSupported: () => true, create },
      shouldNotify: () => true,
      show: vi.fn(),
    });
    const options = { previewEnabled: true, featureLabel: () => 'Search revamp' };

    coordinator.update({ items: [errorItem()] }, options);
    expect(create).toHaveBeenCalledOnce();
    // The same pending item never re-notifies.
    coordinator.update({ items: [errorItem()] }, options);
    expect(create).toHaveBeenCalledOnce();
    // Clearing the item makes it eligible again.
    coordinator.update({ items: [] }, options);
    coordinator.update({ items: [errorItem()] }, options);
    expect(create).toHaveBeenCalledTimes(2);
  });

  it('composes the preview body from the class label, feature label, and catalog title', () => {
    const notification: NotificationHandle = { show: vi.fn(), on: vi.fn() };
    const create = vi.fn((_options: { title: string; body: string }) => notification);
    const coordinator = new AttentionNotificationCoordinator({
      sink: { isSupported: () => true, create },
      shouldNotify: () => true,
      show: vi.fn(),
    });

    coordinator.update(
      { items: [errorItem()] },
      { previewEnabled: true, featureLabel: () => 'Search revamp' },
    );

    const body = create.mock.calls[0]?.[0]?.body ?? '';
    expect(body).toContain('Failed');
    expect(body).toContain('Search revamp');
    expect(body).toContain('Iteration budget exhausted');
    expect(body.length).toBeLessThanOrEqual(180);
  });

  it('keeps the no-preview body unchanged for error items', () => {
    const notification: NotificationHandle = { show: vi.fn(), on: vi.fn() };
    const create = vi.fn((_options: { title: string; body: string }) => notification);
    const coordinator = new AttentionNotificationCoordinator({
      sink: { isSupported: () => true, create },
      shouldNotify: () => true,
      show: vi.fn(),
    });

    coordinator.update(
      { items: [errorItem()] },
      { previewEnabled: false, featureLabel: () => 'Search revamp' },
    );

    expect(create.mock.calls[0]?.[0].body).toBe('Agentico needs attention.');
  });
});
