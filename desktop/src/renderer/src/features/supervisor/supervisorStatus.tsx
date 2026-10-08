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
 * The renderer-wide supervisor status: the latest supervisor read model per
 * server connection key, the main window's effective focus, and the
 * Supervisor row's unread flag. It lives in the app root beside the drafts
 * store, so the sidebar row, the update surfaces and the setup banner see
 * the supervisor from any page — the Supervisor page keeps its own,
 * transcript-bearing state.
 *
 * Unread: set when a turn ends (the lifecycle returns to `idle` with outcome
 * `completed`) unless the Supervisor page is selected and the window focused
 * at that moment; cleared the moment both hold. Markers, failures and
 * interruptions never set it. Per server key, in memory, lost on relaunch.
 */
import { createContext, useContext, useEffect, useSyncExternalStore, type ReactNode } from 'react';
import type {
  SupervisorLifecycle,
  SupervisorPendingRequest,
  SupervisorState,
} from '../../../../shared/ipc';

/** What the shell and other surfaces read about the current server's supervisor. */
export interface SupervisorStatus {
  /** The latest lifecycle; null until the server's state first loads. */
  lifecycle: SupervisorLifecycle | null;
  state: SupervisorState | null;
  unread: boolean;
  windowFocused: boolean;
}

interface SupervisorStatusEntry {
  state: SupervisorState | null;
  unread: boolean;
  /** Counts applied stream states, so a fetch that raced one is dropped. */
  streamTick: number;
}

const EMPTY_ENTRY: SupervisorStatusEntry = Object.freeze({
  state: null,
  unread: false,
  streamTick: 0,
});

/** Lifecycles inside a turn: leaving one for a completed `idle` ends the turn. */
function inTurn(lifecycle: SupervisorLifecycle): boolean {
  return (
    lifecycle === 'running' ||
    lifecycle === 'waiting_permission' ||
    lifecycle === 'waiting_question'
  );
}

function turnEnded(previous: SupervisorState | null, next: SupervisorState): boolean {
  return (
    previous !== null &&
    inTurn(previous.lifecycle) &&
    next.lifecycle === 'idle' &&
    next.lastTurnOutcome === 'completed'
  );
}

/**
 * In-memory supervisor status per server key. Writes notify subscribers
 * only when the current server's snapshot changed.
 */
export class SupervisorStatusStore {
  private readonly entries = new Map<string, SupervisorStatusEntry>();
  private readonly listeners = new Set<() => void>();
  private currentKey: string | null = null;
  private focused: boolean;
  private pageSelected = false;
  private snapshot: SupervisorStatus;

  constructor(options: { windowFocused?: boolean } = {}) {
    this.focused = options.windowFocused ?? true;
    this.snapshot = this.build();
  }

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  /** The current server's status; a stable object between changes. */
  getSnapshot = (): SupervisorStatus => this.snapshot;

  /** The connected server's key, or null while no server is ready. */
  get serverKey(): string | null {
    return this.currentKey;
  }

  /** Stream states applied for the server so far; see `applyFetchedState`. */
  streamTick(serverKey: string): number {
    return this.entry(serverKey).streamTick;
  }

  /** Points the store at the ready server (or none). */
  setServer(serverKey: string | null): void {
    if (this.currentKey === serverKey) return;
    this.currentKey = serverKey;
    this.reconcile();
  }

  setWindowFocused(focused: boolean): void {
    if (this.focused === focused) return;
    this.focused = focused;
    this.reconcile();
  }

  /** The shell reports whether the Supervisor page is the selected page. */
  setPageSelected(selected: boolean): void {
    if (this.pageSelected === selected) return;
    this.pageSelected = selected;
    this.reconcile();
  }

  /** A stream `state` event for the server. */
  applyStreamState(serverKey: string, state: SupervisorState): void {
    const entry = this.entry(serverKey);
    this.write(
      serverKey,
      this.withState({ ...entry, streamTick: entry.streamTick + 1 }, state, serverKey),
    );
  }

  /**
   * A fetched read model, started when the server's stream tick was `tick`.
   * A stream state that arrived while the fetch was in flight is newer, so
   * the fetched one is dropped then.
   */
  applyFetchedState(serverKey: string, state: SupervisorState, tick: number): void {
    const entry = this.entry(serverKey);
    if (entry.streamTick !== tick) return;
    this.write(serverKey, this.withState(entry, state, serverKey));
  }

  /** A stream `request` event: the request joins the pending list once. */
  addRequest(serverKey: string, generation: number, request: SupervisorPendingRequest): void {
    const entry = this.entry(serverKey);
    const state = entry.state;
    if (state === null || generation < state.generation) return;
    if (state.pendingRequests.some((item) => item.id === request.id)) return;
    this.write(serverKey, {
      ...entry,
      state: { ...state, pendingRequests: [...state.pendingRequests, request] },
    });
  }

  private entry(serverKey: string): SupervisorStatusEntry {
    return this.entries.get(serverKey) ?? EMPTY_ENTRY;
  }

  private seen(serverKey: string): boolean {
    return serverKey === this.currentKey && this.pageSelected && this.focused;
  }

  private withState(
    entry: SupervisorStatusEntry,
    state: SupervisorState,
    serverKey: string,
  ): SupervisorStatusEntry {
    const unread = turnEnded(entry.state, state) ? !this.seen(serverKey) : entry.unread;
    return { ...entry, state, unread };
  }

  private write(serverKey: string, entry: SupervisorStatusEntry): void {
    this.entries.set(serverKey, entry);
    this.reconcile();
  }

  /** Clears the current server's unread flag while it is seen, then republishes. */
  private reconcile(): void {
    const key = this.currentKey;
    if (key !== null && this.seen(key)) {
      const entry = this.entries.get(key);
      if (entry?.unread === true) this.entries.set(key, { ...entry, unread: false });
    }
    const next = this.build();
    const previous = this.snapshot;
    if (
      next.state === previous.state &&
      next.unread === previous.unread &&
      next.windowFocused === previous.windowFocused
    ) {
      return;
    }
    this.snapshot = next;
    for (const listener of this.listeners) listener();
  }

  private build(): SupervisorStatus {
    const entry = this.currentKey === null ? EMPTY_ENTRY : this.entry(this.currentKey);
    return {
      lifecycle: entry.state?.lifecycle ?? null,
      state: entry.state,
      unread: entry.unread,
      windowFocused: this.focused,
    };
  }
}

export const SupervisorStatusContext = createContext<SupervisorStatusStore | null>(null);

const FALLBACK_STATUS: SupervisorStatus = Object.freeze({
  lifecycle: null,
  state: null,
  unread: false,
  windowFocused: true,
});
const noSubscription = (): (() => void) => () => undefined;
const fallbackSnapshot = (): SupervisorStatus => FALLBACK_STATUS;

/**
 * The current server's supervisor status: `{ lifecycle, state, unread,
 * windowFocused }`. Without a provider (standalone renders) it reads as
 * not-yet-loaded.
 */
export function useSupervisorStatus(): SupervisorStatus {
  const store = useContext(SupervisorStatusContext);
  return useSyncExternalStore(
    store?.subscribe ?? noSubscription,
    store?.getSnapshot ?? fallbackSnapshot,
  );
}

/** The shell reports whether the Supervisor page is selected; cleared on unmount. */
export function useReportSupervisorPageSelected(selected: boolean): void {
  const store = useContext(SupervisorStatusContext);
  useEffect(() => {
    if (store === null) return;
    store.setPageSelected(selected);
  }, [selected, store]);
  useEffect(() => {
    if (store === null) return;
    return () => store.setPageSelected(false);
  }, [store]);
}

function initialWindowFocus(): boolean {
  return document.visibilityState !== 'hidden' && document.hasFocus();
}

/** Creates the app-session store, seeded with the document's current focus. */
export function createSupervisorStatusStore(): SupervisorStatusStore {
  return new SupervisorStatusStore({ windowFocused: initialWindowFocus() });
}

/**
 * Provides the store and feeds it: the ready server's key, the main
 * process's window-focus pushes, and the supervisor stream (`state`,
 * `request` and `reset`). A server's state is fetched each time it becomes
 * the ready server (the stream only carries changes from then on) and again
 * after a stream reset; unread survives, held per key.
 */
export function SupervisorStatusProvider({
  store,
  serverKey,
  children,
}: {
  store: SupervisorStatusStore;
  /** The ready server's connection key, or null while none is ready. */
  serverKey: string | null;
  children: ReactNode;
}) {
  useEffect(() => store.setServer(serverKey), [serverKey, store]);

  useEffect(
    () => window.agentico.onWindowFocusChanged((event) => store.setWindowFocused(event.focused)),
    [store],
  );

  useEffect(() => {
    if (serverKey === null) return;
    let alive = true;
    const load = (): void => {
      const tick = store.streamTick(serverKey);
      void window.agentico
        .getSupervisorState()
        .then((state) => {
          if (alive) store.applyFetchedState(serverKey, state, tick);
        })
        .catch(() => {
          // The stream keeps the status current; a missed load is not fatal.
        });
    };
    const unsubscribe = window.agentico.onSupervisorEvent((event) => {
      if (event.type === 'reset') {
        load();
      } else if (event.type === 'state') {
        store.applyStreamState(serverKey, event.state);
      } else if (event.type === 'request') {
        store.addRequest(serverKey, event.generation, event.request);
      }
    });
    load();
    return () => {
      alive = false;
      unsubscribe();
    };
  }, [serverKey, store]);

  return (
    <SupervisorStatusContext.Provider value={store}>{children}</SupervisorStatusContext.Provider>
  );
}
