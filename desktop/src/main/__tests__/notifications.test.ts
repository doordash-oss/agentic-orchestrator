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
