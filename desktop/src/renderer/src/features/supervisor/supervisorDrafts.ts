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

/**
 * Per-server Supervisor composer drafts, modelled on the creation-drafts
 * store. One entry per server connection key holds the conversation it
 * belongs to, the draft text, the hidden error reference attached by an
 * "Explain in chat", the attachment items (local paths and staged uploads)
 * and the local message queue. The page reads and writes the entry instead
 * of component state, so a draft survives opening a feature and coming
 * back, and switching servers and back. In memory only: a relaunch starts
 * fresh, and nothing is copied between servers.
 *
 * Carry-over: when the server's current conversation differs from the
 * entry's (a reset or a server-side change), the text, attachments and error
 * reference move to the new conversation and the queue is discarded.
 */
import { createContext, useContext, useRef, useSyncExternalStore } from 'react';
import type { ErrorReference } from '../../../../shared/ipc';
import type { ComposerUploadItem } from '../stagedItems';

/** The composer's attachment items: local paths and staged uploads. */
export interface SupervisorDraftItems {
  images: readonly string[];
  attachments: readonly string[];
  imageUploads: readonly ComposerUploadItem[];
  attachmentUploads: readonly ComposerUploadItem[];
}

/** One locally queued message; it keeps its own text and attachments until sent. */
export interface SupervisorQueuedMessage {
  id: string;
  text: string;
  items: SupervisorDraftItems;
  errorReference: ErrorReference | null;
  /** Set by "Send now": delivered at the next idle whatever the turn's outcome. */
  next?: boolean;
}

/** One server's composer draft. */
export interface SupervisorDraftEntry {
  /** The conversation the draft and queue belong to; null until a state loads. */
  conversationId: string | null;
  text: string;
  errorReference: ErrorReference | null;
  items: SupervisorDraftItems;
  queue: readonly SupervisorQueuedMessage[];
}

export const EMPTY_SUPERVISOR_DRAFT_ITEMS: SupervisorDraftItems = Object.freeze({
  images: Object.freeze([]) as readonly string[],
  attachments: Object.freeze([]) as readonly string[],
  imageUploads: Object.freeze([]) as readonly ComposerUploadItem[],
  attachmentUploads: Object.freeze([]) as readonly ComposerUploadItem[],
});

export const EMPTY_SUPERVISOR_DRAFT: SupervisorDraftEntry = Object.freeze({
  conversationId: null,
  text: '',
  errorReference: null,
  items: EMPTY_SUPERVISOR_DRAFT_ITEMS,
  queue: Object.freeze([]) as readonly SupervisorQueuedMessage[],
});

/** True when the items hold no image or file of either source. */
export function draftItemsEmpty(items: SupervisorDraftItems): boolean {
  return (
    items.images.length === 0 &&
    items.attachments.length === 0 &&
    items.imageUploads.length === 0 &&
    items.attachmentUploads.length === 0
  );
}

/** How many images and files the items hold, of either source. */
export function draftItemCount(items: SupervisorDraftItems): number {
  return (
    items.images.length +
    items.attachments.length +
    items.imageUploads.length +
    items.attachmentUploads.length
  );
}

/** The union of two item sets, keeping order and dropping repeated paths or chips. */
export function mergeDraftItems(
  current: SupervisorDraftItems,
  added: SupervisorDraftItems,
): SupervisorDraftItems {
  const paths = (a: readonly string[], b: readonly string[]): readonly string[] => [
    ...a,
    ...b.filter((path) => !a.includes(path)),
  ];
  const uploads = (
    a: readonly ComposerUploadItem[],
    b: readonly ComposerUploadItem[],
  ): readonly ComposerUploadItem[] => [
    ...a,
    ...b.filter((item) => !a.some((known) => known.id === item.id)),
  ];
  return {
    images: paths(current.images, added.images),
    attachments: paths(current.attachments, added.attachments),
    imageUploads: uploads(current.imageUploads, added.imageUploads),
    attachmentUploads: uploads(current.attachmentUploads, added.attachmentUploads),
  };
}

/** Sets an empty composer to the draft; otherwise appends it after a blank line. */
export function appendDraftText(current: string, draft: string): string {
  return current.trim() === '' ? draft : `${current.trimEnd()}\n\n${draft}`;
}

/**
 * In-memory, per-server Supervisor drafts. Every write goes through
 * `update`, which notifies subscribers only when the entry changed.
 */
export class SupervisorDraftsStore {
  private readonly entries = new Map<string, SupervisorDraftEntry>();
  private readonly listeners = new Set<() => void>();

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  private notify(): void {
    for (const listener of this.listeners) listener();
  }

  /** The server's entry, or the shared empty entry when none is held. */
  entry(serverKey: string): SupervisorDraftEntry {
    return this.entries.get(serverKey) ?? EMPTY_SUPERVISOR_DRAFT;
  }

  /** Replaces the server's entry with `fn(current)`; the generic write path. */
  update(serverKey: string, fn: (entry: SupervisorDraftEntry) => SupervisorDraftEntry): void {
    const current = this.entry(serverKey);
    const next = fn(current);
    if (next === current) return;
    this.entries.set(serverKey, next);
    this.notify();
  }

  setText(serverKey: string, text: string): void {
    this.update(serverKey, (entry) => (entry.text === text ? entry : { ...entry, text }));
  }

  setErrorReference(serverKey: string, errorReference: ErrorReference | null): void {
    this.update(serverKey, (entry) =>
      entry.errorReference === errorReference ? entry : { ...entry, errorReference },
    );
  }

  /** Applies a functional update to one item list, as the composer hands them out. */
  updateItems<K extends keyof SupervisorDraftItems>(
    serverKey: string,
    key: K,
    fn: (items: SupervisorDraftItems[K]) => SupervisorDraftItems[K],
  ): void {
    this.update(serverKey, (entry) => {
      const next = fn(entry.items[key]);
      return next === entry.items[key]
        ? entry
        : { ...entry, items: { ...entry.items, [key]: next } };
    });
  }

  /**
   * Drafts routed text into the composer (appended after a blank line when
   * it already holds text); a routed reference replaces the attached one.
   */
  appendDraft(serverKey: string, draft?: string, errorReference?: ErrorReference): void {
    this.update(serverKey, (entry) => ({
      ...entry,
      ...(draft === undefined ? {} : { text: appendDraftText(entry.text, draft) }),
      ...(errorReference === undefined ? {} : { errorReference }),
    }));
  }

  /** Empties the composer (text, reference and items); the queue stays. */
  clearComposer(serverKey: string): void {
    this.update(serverKey, (entry) =>
      entry.text === '' && entry.errorReference === null && draftItemsEmpty(entry.items)
        ? entry
        : { ...entry, text: '', errorReference: null, items: EMPTY_SUPERVISOR_DRAFT_ITEMS },
    );
  }

  /** Appends a message to the queue's tail. */
  enqueue(serverKey: string, message: SupervisorQueuedMessage): void {
    this.update(serverKey, (entry) => ({ ...entry, queue: [...entry.queue, message] }));
  }

  /** Puts a message back at the queue's head (a send the server refused as stale). */
  requeueFront(serverKey: string, message: SupervisorQueuedMessage): void {
    this.update(serverKey, (entry) => ({
      ...entry,
      queue: [message, ...entry.queue.filter((item) => item.id !== message.id)],
    }));
  }

  /** Drops one queued message; unknown ids change nothing. */
  removeQueued(serverKey: string, id: string): void {
    this.update(serverKey, (entry) =>
      entry.queue.some((item) => item.id === id)
        ? { ...entry, queue: entry.queue.filter((item) => item.id !== id) }
        : entry,
    );
  }

  /** Marks one queued message as next, clearing the mark on every other. */
  markNext(serverKey: string, id: string): void {
    this.update(serverKey, (entry) =>
      entry.queue.some((item) => item.id === id)
        ? {
            ...entry,
            queue: entry.queue.map((item) =>
              item.id === id
                ? { ...item, next: true }
                : item.next === true
                  ? { ...item, next: false }
                  : item,
            ),
          }
        : entry,
    );
  }

  /**
   * Moves a queued message back into the composer: its text appended after
   * a blank line, its attachments merged, its reference attached when the
   * composer has none.
   */
  editQueued(serverKey: string, id: string): void {
    this.update(serverKey, (entry) => {
      const item = entry.queue.find((candidate) => candidate.id === id);
      if (item === undefined) return entry;
      return {
        ...entry,
        text: item.text === '' ? entry.text : appendDraftText(entry.text, item.text),
        items: mergeDraftItems(entry.items, item.items),
        errorReference: entry.errorReference ?? item.errorReference,
        queue: entry.queue.filter((candidate) => candidate.id !== id),
      };
    });
  }

  /** Empties the queue. */
  clearQueue(serverKey: string): void {
    this.update(serverKey, (entry) =>
      entry.queue.length === 0 ? entry : { ...entry, queue: EMPTY_SUPERVISOR_DRAFT.queue },
    );
  }

  /**
   * Binds the entry to the server's current conversation. A different
   * conversation keeps the text, attachments and error reference and drops
   * the queue; the same one changes nothing.
   */
  adoptConversation(serverKey: string, conversationId: string): void {
    this.update(serverKey, (entry) =>
      entry.conversationId === conversationId
        ? entry
        : { ...entry, conversationId, queue: EMPTY_SUPERVISOR_DRAFT.queue },
    );
  }

  /** Forgets the server's entry entirely. */
  discard(serverKey: string): void {
    if (this.entries.delete(serverKey)) this.notify();
  }
}

export const SupervisorDraftsContext = createContext<SupervisorDraftsStore | null>(null);

/**
 * The app-session Supervisor drafts store. The provider (App) supplies the
 * store that outlives page mounts and server switches; without one, a
 * private per-mount store keeps standalone renders self-contained.
 */
export function useSupervisorDrafts(): SupervisorDraftsStore {
  const provided = useContext(SupervisorDraftsContext);
  const fallback = useRef<SupervisorDraftsStore | null>(null);
  if (provided !== null) return provided;
  if (fallback.current === null) fallback.current = new SupervisorDraftsStore();
  return fallback.current;
}

/** Reactively reads one server's draft entry. */
export function useSupervisorDraftEntry(
  store: SupervisorDraftsStore,
  serverKey: string,
): SupervisorDraftEntry {
  return useSyncExternalStore(
    store.subscribe,
    () => store.entry(serverKey),
    () => EMPTY_SUPERVISOR_DRAFT,
  );
}
