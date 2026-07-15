import { useCallback, useEffect, useRef, useState } from 'react';
import type { ThemeInfo, ThemePreference } from '../../shared/ipc';

/** Tracks the user's reduced-motion preference reactively. */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(
    () => window.matchMedia('(prefers-reduced-motion: reduce)').matches,
  );
  useEffect(() => {
    const query = window.matchMedia('(prefers-reduced-motion: reduce)');
    const onChange = (event: MediaQueryListEvent): void => {
      setReduced(event.matches);
    };
    query.addEventListener('change', onChange);
    return () => query.removeEventListener('change', onChange);
  }, []);
  return reduced;
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
