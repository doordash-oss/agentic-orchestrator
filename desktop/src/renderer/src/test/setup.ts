import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, vi } from 'vitest';

// Auto-cleanup does not register without vitest globals.
afterEach(() => {
  cleanup();
});

// jsdom has no matchMedia; components consult it for system theme and
// reduced-motion behavior. Individual tests override `matches` per query.
export interface MatchMediaState {
  darkScheme: boolean;
  reducedMotion: boolean;
  narrowCockpit: boolean;
}

export const matchMediaState: MatchMediaState = {
  darkScheme: true,
  reducedMotion: false,
  narrowCockpit: false,
};

type Listener = (event: { matches: boolean }) => void;
const listeners = new Map<string, Set<Listener>>();

export function dispatchMediaChange(query: string, matches: boolean): void {
  if (query.includes('prefers-color-scheme: dark')) {
    matchMediaState.darkScheme = matches;
  }
  if (query.includes('prefers-reduced-motion')) {
    matchMediaState.reducedMotion = matches;
  }
  if (query.includes('max-width: 900px')) {
    matchMediaState.narrowCockpit = matches;
  }
  for (const listener of listeners.get(query) ?? []) {
    listener({ matches });
  }
}

function matchesFor(query: string): boolean {
  if (query.includes('prefers-color-scheme: dark')) return matchMediaState.darkScheme;
  if (query.includes('prefers-reduced-motion')) return matchMediaState.reducedMotion;
  if (query.includes('max-width: 900px')) return matchMediaState.narrowCockpit;
  return false;
}

Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: vi.fn((query: string) => ({
    get matches() {
      return matchesFor(query);
    },
    media: query,
    addEventListener: (_type: string, listener: Listener) => {
      let set = listeners.get(query);
      if (set === undefined) {
        set = new Set();
        listeners.set(query, set);
      }
      set.add(listener);
    },
    removeEventListener: (_type: string, listener: Listener) => {
      listeners.get(query)?.delete(listener);
    },
    addListener: () => {},
    removeListener: () => {},
    onchange: null,
    dispatchEvent: () => false,
  })),
});
