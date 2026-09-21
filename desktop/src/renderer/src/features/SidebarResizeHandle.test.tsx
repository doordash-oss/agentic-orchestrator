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

import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SidebarResizeHandle } from './SidebarResizeHandle';

// jsdom's window is 1024 wide, so the handle's maximum is 512.
const MAXIMUM = Math.min(520, Math.floor(window.innerWidth / 2));

function renderHandle(width = 260) {
  const onPreview = vi.fn();
  const onCommit = vi.fn();
  const view = render(
    <SidebarResizeHandle width={width} onPreview={onPreview} onCommit={onCommit} />,
  );
  const handle = screen.getByRole('separator', { name: 'Resize sidebar' });
  return { handle, onPreview, onCommit, unmount: view.unmount };
}

function pointer(type: string, init: { clientX: number; pointerId?: number }): PointerEvent {
  return new PointerEvent(type, { bubbles: true, pointerId: 1, button: 0, ...init });
}

afterEach(cleanup);

describe('SidebarResizeHandle drag', () => {
  it('previews from the window during a drag and commits on release, then stops listening', () => {
    const { handle, onPreview, onCommit } = renderHandle();

    fireEvent(handle, pointer('pointerdown', { clientX: 256 }));
    expect(handle).toHaveAttribute('data-resizing', 'true');

    act(() => window.dispatchEvent(pointer('pointermove', { clientX: 306 })));
    act(() => window.dispatchEvent(pointer('pointermove', { clientX: 356 })));
    expect(onPreview.mock.calls).toEqual([[310], [360]]);

    act(() => window.dispatchEvent(pointer('pointerup', { clientX: 356 })));
    expect(onCommit).toHaveBeenCalledWith(360);
    expect(handle).toHaveAttribute('data-resizing', 'false');

    act(() => window.dispatchEvent(pointer('pointermove', { clientX: 400 })));
    expect(onPreview).toHaveBeenCalledTimes(2);
  });

  it('keeps the drag alive when the browser takes pointer capture away', () => {
    const { handle, onPreview, onCommit } = renderHandle();

    fireEvent(handle, pointer('pointerdown', { clientX: 256 }));
    // Chromium fires this when the window is deactivated mid-drag.
    fireEvent(handle, pointer('lostpointercapture', { clientX: 256 }));
    expect(handle).toHaveAttribute('data-resizing', 'true');
    expect(onPreview).not.toHaveBeenCalled();

    act(() => window.dispatchEvent(pointer('pointermove', { clientX: 356 })));
    act(() => window.dispatchEvent(pointer('pointerup', { clientX: 356 })));
    expect(onPreview).toHaveBeenCalledWith(360);
    expect(onCommit).toHaveBeenCalledWith(360);
  });

  it('commits the width the pointer had reached when the window blurs mid-drag', () => {
    const { handle, onCommit } = renderHandle();

    fireEvent(handle, pointer('pointerdown', { clientX: 256 }));
    act(() => window.dispatchEvent(pointer('pointermove', { clientX: 300 })));
    act(() => window.dispatchEvent(new Event('blur')));
    expect(onCommit).toHaveBeenCalledWith(304);
    expect(handle).toHaveAttribute('data-resizing', 'false');

    // The release that arrives once the window is active again is not a second commit.
    act(() => window.dispatchEvent(pointer('pointerup', { clientX: 300 })));
    expect(onCommit).toHaveBeenCalledTimes(1);
  });

  it('clamps the drag to the minimum and the window-derived maximum', () => {
    const { handle, onPreview, onCommit } = renderHandle();

    fireEvent(handle, pointer('pointerdown', { clientX: 256 }));
    act(() => window.dispatchEvent(pointer('pointermove', { clientX: 5000 })));
    act(() => window.dispatchEvent(pointer('pointermove', { clientX: -5000 })));
    expect(onPreview.mock.calls).toEqual([[MAXIMUM], [200]]);
    act(() => window.dispatchEvent(pointer('pointerup', { clientX: -5000 })));
    expect(onCommit).toHaveBeenCalledWith(200);
  });

  it('ignores events from another pointer and a second press during a drag', () => {
    const { handle, onPreview, onCommit } = renderHandle();

    fireEvent(handle, pointer('pointerdown', { clientX: 256 }));
    fireEvent(handle, pointer('pointerdown', { clientX: 900, pointerId: 7 }));
    act(() => window.dispatchEvent(pointer('pointermove', { clientX: 900, pointerId: 7 })));
    act(() => window.dispatchEvent(pointer('pointerup', { clientX: 900, pointerId: 7 })));
    expect(onPreview).not.toHaveBeenCalled();
    expect(onCommit).not.toHaveBeenCalled();

    act(() => window.dispatchEvent(pointer('pointerup', { clientX: 256 })));
    expect(onCommit).toHaveBeenCalledWith(260);
  });

  it('detaches its window listeners when unmounted mid-drag', () => {
    const { handle, onPreview, onCommit, unmount } = renderHandle();

    fireEvent(handle, pointer('pointerdown', { clientX: 256 }));
    unmount();
    act(() => window.dispatchEvent(pointer('pointermove', { clientX: 356 })));
    act(() => window.dispatchEvent(pointer('pointerup', { clientX: 356 })));
    expect(onPreview).not.toHaveBeenCalled();
    expect(onCommit).not.toHaveBeenCalled();
  });
});

describe('SidebarResizeHandle keyboard and reset', () => {
  it('steps, jumps to the bounds, and resets on double click', () => {
    const { handle, onCommit } = renderHandle();

    fireEvent.keyDown(handle, { key: 'ArrowRight' });
    fireEvent.keyDown(handle, { key: 'ArrowLeft' });
    fireEvent.keyDown(handle, { key: 'Home' });
    fireEvent.keyDown(handle, { key: 'End' });
    fireEvent.doubleClick(handle);
    expect(onCommit.mock.calls).toEqual([[270], [250], [200], [MAXIMUM], [260]]);
  });
});
