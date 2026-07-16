import { useCallback, useEffect, useRef, useState } from 'react';
import type { ConnectionState, ThemeInfo, ThemePreference } from '../../shared/ipc';

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
    void window.agentico
      .getConnectionStatus()
      .then((loaded) => {
        if (alive) setState(loaded);
      })
      .catch(() => {
        // The shell surfaces IPC failures; the gate simply stays non-ready.
      });
    const unsubscribe = window.agentico.onConnectionChanged((next) => {
      setState(next);
    });
    return () => {
      alive = false;
      unsubscribe();
    };
  }, []);
  return state;
}

export interface ThemeState {
  preference: ThemePreference;
  resolved: 'light' | 'dark';
  setPreference: (preference: ThemePreference) => void;
}

/**
 * Owns the light/dark/system theme: loads the persisted preference from main,
 * follows OS appearance while on `system`, and mirrors the resolved theme
 * onto <html data-theme> for the CSS custom properties.
 */
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
        setInfo((prev) => ({ ...prev, resolved: event.matches ? 'dark' : 'light' }));
      }
    };
    query.addEventListener('change', onChange);
    return () => {
      alive = false;
      query.removeEventListener('change', onChange);
    };
  }, []);

  useEffect(() => {
    document.documentElement.dataset['theme'] = info.resolved;
  }, [info.resolved]);

  const setPreference = useCallback((preference: ThemePreference) => {
    void window.agentico
      .setThemePreference(preference)
      .then(setInfo)
      .catch(() => {
        // Persisting failed; leave the current theme untouched.
      });
  }, []);

  return { preference: info.preference, resolved: info.resolved, setPreference };
}
