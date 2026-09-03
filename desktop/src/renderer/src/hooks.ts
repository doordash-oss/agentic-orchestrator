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

import { useCallback, useEffect, useRef, useState, type DependencyList } from 'react';
import type { ConnectionState, ServerKind, ThemeInfo, ThemePreference } from '../../shared/ipc';
import type { CanonicalError } from '../../shared/ipc';
import type { ErrorSurfaceLocalAction } from './components/ErrorSurface';
import { parseIpcError } from './wizard/ipcError';

/** Shared asynchronous load phases; callers choose the successful payload shape. */
export type LoadState<T> = { phase: 'loading' } | { phase: 'error'; error: CanonicalError } | T;

/**
 * Loads IPC-backed data with stale-result protection and a stable reload
 * affordance. The dependency list deliberately defines the load identity;
 * the latest callback is read from a ref so inline closures are safe.
 */
export function useIpcLoad<T>(
  load: () => Promise<T>,
  dependencies: DependencyList,
): { state: LoadState<{ phase: 'loaded'; data: T }>; reload: () => void } {
  const loadRef = useRef(load);
  loadRef.current = load;
  const requestRef = useRef(0);
  const [reloadToken, setReloadToken] = useState(0);
  const [state, setState] = useState<LoadState<{ phase: 'loaded'; data: T }>>({
    phase: 'loading',
  });

  const reload = useCallback(() => setReloadToken((token) => token + 1), []);

  useEffect(() => {
    const request = ++requestRef.current;
    setState({ phase: 'loading' });
    void loadRef.current().then(
      (data) => {
        if (request === requestRef.current) setState({ phase: 'loaded', data });
      },
      (error: unknown) => {
        if (request === requestRef.current) {
          setState({ phase: 'error', error: parseIpcError(error) });
        }
      },
    );
    return () => {
      requestRef.current += 1;
    };
    // The caller owns the semantic load dependencies; reloadToken is the local retry edge.
  }, [...dependencies, reloadToken]);

  return { state, reload };
}

/** The standard local Retry adapter for compact IPC load failures. */
export function retryAction(reload: () => void): ErrorSurfaceLocalAction {
  return { label: 'Retry', onAction: reload };
}

/** Subscribes to a media query reactively. */
export function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const list = window.matchMedia(query);
    const onChange = (event: MediaQueryListEvent): void => {
      setMatches(event.matches);
    };
    list.addEventListener('change', onChange);
    return () => list.removeEventListener('change', onChange);
  }, [query]);
  return matches;
}

/** Tracks the user's reduced-motion preference reactively. */
export function usePrefersReducedMotion(): boolean {
  return useMediaQuery('(prefers-reduced-motion: reduce)');
}

/** True in narrow windows (≤ 480px) so dense layouts can stack. */
export function useNarrowViewport(): boolean {
  return useMediaQuery('(max-width: 480px)');
}

const INITIAL_CONNECTION: ConnectionState = {
  status: 'idle',
  stage: 'resolve-runtime',
  detail: 'Starting…',
  ownership: 'none',
};

/**
 * Follows the main process's connection state: initial fetch plus pushed
 * updates. Failures leave the last known state; the connection shell owns
 * its own richer error presentation.
 */
export function useConnectionState(): ConnectionState {
  const [state, setState] = useState<ConnectionState>(INITIAL_CONNECTION);
  useEffect(() => {
    let alive = true;
    let receivedLiveUpdate = false;
    const unsubscribe = window.agentico.onConnectionChanged((next) => {
      receivedLiveUpdate = true;
      setState(next);
    });
    void window.agentico
      .getConnectionStatus()
      .then((loaded) => {
        if (alive && !receivedLiveUpdate) setState(loaded);
      })
      .catch(() => {
        // The shell surfaces IPC failures; the gate simply stays non-ready.
      });
    return () => {
      alive = false;
      unsubscribe();
    };
  }, []);
  return state;
}

/**
 * The locality of the ready connection, or `null` while connecting or on a
 * terminal failure. `null` degrades to the local behavior: only an
 * authoritative `remote` gates local-only affordances. Reacts live to
 * server switches through the pushed connection updates.
 */
export function useConnectionKind(): ServerKind | null {
  const state = useConnectionState();
  return state.status === 'ready' ? state.kind : null;
}

export interface ThemeState {
  preference: ThemePreference;
  resolved: 'light' | 'dark';
  setPreference: (preference: ThemePreference) => void;
}

/**
 * Owns the light/dark/system theme: loads the persisted preference from main,
 * follows OS appearance while on `system`, and mirrors the resolved theme
 * onto <html data-theme> for the CSS custom properties. Instances in the same
 * document sync through a custom window event; instances in another window
 * follow the main process's `theme` app-event broadcast, since a
 * same-document event cannot cross a window boundary. Both paths carry the
 * same resolved ThemeInfo, so applying either is idempotent.
 */
const THEME_SYNC_EVENT = 'agentico-theme-sync';

export function useTheme(): ThemeState {
  const [info, setInfo] = useState<ThemeInfo>(() => ({
    preference: 'system',
    resolved: window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light',
  }));
  const infoRef = useRef(info);
  infoRef.current = info;

  useEffect(() => {
    let alive = true;
    void window.agentico
      .getThemePreference()
      .then((loaded) => {
        if (alive) setInfo(loaded);
      })
      .catch(() => {
        // Keep the OS-derived fallback; theming must never block the shell.
      });

    const query = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = (event: MediaQueryListEvent): void => {
      if (infoRef.current.preference === 'system') {
        const resolved: 'light' | 'dark' = event.matches ? 'dark' : 'light';
        const next = { ...infoRef.current, resolved };
        setInfo(next);
        window.dispatchEvent(new CustomEvent(THEME_SYNC_EVENT, { detail: next }));
      }
    };
    query.addEventListener('change', onChange);

    const onSync = (event: Event): void => {
      if (event instanceof CustomEvent && event.detail) {
        setInfo(event.detail as ThemeInfo);
      }
    };
    window.addEventListener(THEME_SYNC_EVENT, onSync);

    const unsubscribe = window.agentico.onAppEvent((event) => {
      if (event.type === 'theme') {
        setInfo({ preference: event.preference, resolved: event.resolved });
      }
    });

    return () => {
      alive = false;
      query.removeEventListener('change', onChange);
      window.removeEventListener(THEME_SYNC_EVENT, onSync);
      unsubscribe();
    };
  }, []);

  useEffect(() => {
    document.documentElement.dataset['theme'] = info.resolved;
  }, [info.resolved]);

  const setPreference = useCallback((preference: ThemePreference) => {
    void window.agentico
      .setThemePreference(preference)
      .then((loaded) => {
        setInfo(loaded);
        window.dispatchEvent(new CustomEvent(THEME_SYNC_EVENT, { detail: loaded }));
      })
      .catch(() => {
        // Persisting failed; leave the current theme untouched.
      });
  }, []);

  return { preference: info.preference, resolved: info.resolved, setPreference };
}

/**
 * Mirrors the dynamic macOS system accent onto the root `--accent` custom
 * property, overriding the Bench static fallback the moment a value
 * arrives. Off macOS, and on any main-process read failure, no `accent`
 * event is ever published, so the static per-appearance blue holds —
 * there is nothing to unset here.
 */
export function useSystemAccentMirror(): void {
  useEffect(
    () =>
      window.agentico.onAppEvent((event) => {
        if (event.type === 'accent') {
          document.documentElement.style.setProperty('--accent', event.color);
        }
      }),
    [],
  );
}
