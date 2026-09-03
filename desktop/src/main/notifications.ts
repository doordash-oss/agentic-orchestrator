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

import { Notification } from 'electron';
import { redactText } from '../shared/errors';
import type { AttentionItem, AttentionSnapshot } from '../shared/ipc';
import { attentionOwnerFeatureId, ERROR_CLASS_LABELS, isSyntheticHelpItem } from '../shared/ipc';

type ActionableAttentionItem = Exclude<AttentionItem, { kind: 'recovery' }>;

export interface NotificationHandle {
  show(): void;
  on(event: 'click', listener: () => void): void;
}

export interface NotificationSink {
  isSupported(): boolean;
  create(options: { title: string; body: string }): NotificationHandle;
}

export const electronNotificationSink: NotificationSink = {
  isSupported: () => Notification.isSupported(),
  create: (options) => new Notification(options),
};

export interface AttentionNotificationCoordinatorDeps {
  sink: NotificationSink;
  shouldNotify(): boolean;
  show(): void;
}

export interface AttentionNotificationOptions {
  previewEnabled: boolean;
  featureLabel(featureId: string): string;
}

/**
 * Snapshot-only native notification coordinator. It never consumes raw stream
 * records or prompt payloads: pending IDs come from AttentionSnapshot, and
 * clearing an item from the snapshot makes it eligible for a future notify.
 */
export class AttentionNotificationCoordinator {
  private readonly notified = new Set<string>();

  constructor(private readonly deps: AttentionNotificationCoordinatorDeps) {}

  update(snapshot: AttentionSnapshot, options: AttentionNotificationOptions): void {
    // Synthetic help items are inline status, not a request — never notify.
    const pending = snapshot.items.filter(
      (item): item is ActionableAttentionItem =>
        item.kind !== 'recovery' && !isSyntheticHelpItem(item),
    );
    const pendingIds = new Set(pending.map(notificationIdentity));
    for (const id of this.notified) {
      if (!pendingIds.has(id)) {
        this.notified.delete(id);
      }
    }

    if (!this.deps.shouldNotify() || !this.deps.sink.isSupported()) {
      return;
    }

    for (const item of pending) {
      const identity = notificationIdentity(item);
      if (this.notified.has(identity)) continue;
      this.notified.add(identity);
      const notification = this.deps.sink.create({
        title: 'Agentico',
        body: options.previewEnabled
          ? previewBody(item, options.featureLabel)
          : 'Agentico needs attention.',
      });
      notification.on('click', () => {
        this.deps.show();
      });
      notification.show();
    }
  }
}

function notificationIdentity(item: AttentionItem): string {
  return `${item.kind}:${item.id}`;
}

function previewBody(
  item: ActionableAttentionItem,
  featureLabel: (featureId: string) => string,
): string {
  const owner = attentionOwnerFeatureId(item);
  const location = owner === undefined ? 'Runtime' : featureLabel(owner);
  const summary = previewSummary(item);
  return redactText(`${attentionTypeLabel(item)} · ${location}${summary}`).slice(0, 180);
}

function previewSummary(item: ActionableAttentionItem): string {
  switch (item.kind) {
    case 'permission':
      return item.toolName === '' ? '' : ` · ${item.toolName}`;
    case 'questions':
      return item.questions[0]?.header === undefined ? '' : ` · ${item.questions[0].header}`;
    case 'gate':
      return item.summary === undefined ? '' : ` · ${item.summary}`;
    case 'help':
      // Synthetic ('input'/'coordinating') items are filtered before notifying.
      return ' · Help request';
    case 'review':
      return ` · ${item.reviewKind} review`;
    case 'error':
      return ` · ${item.title}`;
    default: {
      const exhaustive: never = item;
      return exhaustive;
    }
  }
}

function attentionTypeLabel(item: ActionableAttentionItem): string {
  switch (item.kind) {
    case 'permission':
      return 'Permission';
    case 'questions':
      return 'Questions';
    case 'gate':
      return 'Input gate';
    case 'help':
      return 'Help';
    case 'review':
      return 'Review';
    // The error item's type label is its class label, shared with every
    // other presence surface.
    case 'error':
      return ERROR_CLASS_LABELS[item.class];
    default: {
      const exhaustive: never = item;
      return exhaustive;
    }
  }
}
