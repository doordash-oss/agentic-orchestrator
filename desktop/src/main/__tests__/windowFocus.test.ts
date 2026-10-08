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

import { describe, expect, it, vi } from 'vitest';
import { WindowFocusSignal } from '../windowFocus';

function makeSignal(visible = true) {
  const state = { visible };
  const publish = vi.fn<(focused: boolean) => void>();
  const signal = new WindowFocusSignal({ isVisible: () => state.visible, publish });
  return { signal, publish, state };
}

describe('WindowFocusSignal', () => {
  it('publishes the effective value on every focus and blur', () => {
    const { signal, publish } = makeSignal();
    signal.setFocused(true);
    signal.setFocused(false);
    signal.setFocused(false);
    expect(publish.mock.calls).toEqual([[true], [false], [false]]);
  });

  it('is never focused while the window is hidden or missing', () => {
    const { signal, publish, state } = makeSignal();
    signal.setFocused(true);
    state.visible = false;
    signal.emit();
    expect(signal.effective()).toBe(false);
    expect(publish).toHaveBeenLastCalledWith(false);
    state.visible = true;
    signal.emit();
    expect(publish).toHaveBeenLastCalledWith(true);
  });

  it('applies the packaged-test override over the ambient flag and publishes its changes', () => {
    const { signal, publish } = makeSignal();
    signal.setFocused(true);
    signal.setOverride(false);
    expect(publish).toHaveBeenLastCalledWith(false);
    signal.setFocused(true);
    expect(signal.effective()).toBe(false);
    signal.setOverride(true);
    signal.setFocused(false);
    expect(signal.effective()).toBe(true);
    signal.setOverride(undefined);
    expect(publish).toHaveBeenLastCalledWith(false);
  });

  it('tells subscribers the effective value on every publish until they unsubscribe', () => {
    const { signal } = makeSignal();
    const listener = vi.fn<(focused: boolean) => void>();
    const unsubscribe = signal.subscribe(listener);
    signal.setFocused(true);
    signal.setOverride(false);
    unsubscribe();
    signal.setOverride(undefined);
    expect(listener.mock.calls).toEqual([[true], [false]]);
  });

  it('keeps the override from making a hidden window focused', () => {
    const { signal } = makeSignal(false);
    signal.setOverride(true);
    expect(signal.effective()).toBe(false);
  });
});
