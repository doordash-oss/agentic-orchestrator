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
import { AccentController, normalizeAccentColor, type AccentColorSource } from '../accent';

function makeSource(color: string): AccentColorSource & { notify: () => void } {
  const listeners = new Set<() => void>();
  return {
    getAccentColor: () => color,
    subscribeNotification: (_event, callback) => {
      listeners.add(callback);
      return listeners.size;
    },
    unsubscribeNotification: () => {},
    notify: () => {
      for (const listener of listeners) listener();
    },
  };
}

describe('normalizeAccentColor', () => {
  it('drops the alpha channel and lowercases RRGGBBAA', () => {
    expect(normalizeAccentColor('3D7DFFFF')).toBe('#3d7dff');
  });

  it('accepts a bare RRGGBB value', () => {
    expect(normalizeAccentColor('0a63ec')).toBe('#0a63ec');
  });

  it('rejects malformed input', () => {
    expect(normalizeAccentColor('not-a-color')).toBeNull();
  });
});

describe('AccentController', () => {
  it('publishes the current color on start, on macOS', () => {
    const source = makeSource('3D7DFFFF');
    const onChange = vi.fn();
    const controller = new AccentController('darwin', source, onChange);

    controller.start();

    expect(onChange).toHaveBeenCalledWith('#3d7dff');
    expect(controller.getCurrent()).toBe('#3d7dff');
  });

  it('re-publishes on the system change notification', () => {
    const source = makeSource('3D7DFFFF');
    const onChange = vi.fn();
    const controller = new AccentController('darwin', source, onChange);
    controller.start();

    source.getAccentColor = () => '0A63ECFF';
    source.notify();

    expect(onChange).toHaveBeenCalledTimes(2);
    expect(onChange).toHaveBeenLastCalledWith('#0a63ec');
    expect(controller.getCurrent()).toBe('#0a63ec');
  });

  it('never reads the platform surface off macOS, leaving current null', () => {
    const source = makeSource('3D7DFFFF');
    const getAccentColor = vi.spyOn(source, 'getAccentColor');
    const onChange = vi.fn();
    const controller = new AccentController('linux', source, onChange);

    controller.start();

    expect(getAccentColor).not.toHaveBeenCalled();
    expect(onChange).not.toHaveBeenCalled();
    expect(controller.getCurrent()).toBeNull();
  });

  it('leaves current null on a read failure and never publishes', () => {
    const source: AccentColorSource = {
      getAccentColor: () => {
        throw new Error('read failed');
      },
      subscribeNotification: () => 1,
      unsubscribeNotification: () => {},
    };
    const onChange = vi.fn();
    const controller = new AccentController('darwin', source, onChange);

    controller.start();

    expect(onChange).not.toHaveBeenCalled();
    expect(controller.getCurrent()).toBeNull();
  });

  it('unsubscribes on stop', () => {
    const source = makeSource('3D7DFFFF');
    const unsubscribeNotification = vi.spyOn(source, 'unsubscribeNotification');
    const controller = new AccentController('darwin', source, vi.fn());
    controller.start();

    controller.stop();

    expect(unsubscribeNotification).toHaveBeenCalledWith(1);
  });
});
