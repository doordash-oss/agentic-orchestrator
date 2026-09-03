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
  narrowShell: boolean;
}

export const matchMediaState: MatchMediaState = {
  darkScheme: true,
  reducedMotion: false,
  narrowCockpit: false,
  narrowShell: false,
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
  if (query.includes('max-width: 700px')) {
    matchMediaState.narrowShell = matches;
  }
  for (const listener of listeners.get(query) ?? []) {
    listener({ matches });
  }
}

function matchesFor(query: string): boolean {
  if (query.includes('prefers-color-scheme: dark')) return matchMediaState.darkScheme;
  if (query.includes('prefers-reduced-motion')) return matchMediaState.reducedMotion;
  if (query.includes('max-width: 900px')) return matchMediaState.narrowCockpit;
  if (query.includes('max-width: 700px')) return matchMediaState.narrowShell;
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
