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
});
