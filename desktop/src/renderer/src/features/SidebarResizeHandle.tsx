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

import { useEffect, useRef, useState } from 'react';

interface Gesture {
  pointerId: number;
  x: number;
  width: number;
  next: number;
  /** Detaches the window listeners and clears the gesture; never commits. */
  release(): void;
}

/**
 * A drag lives on the window, not on pointer capture. Capture is still
 * requested so the browser keeps hit-testing the divider, but the browser
 * can take it away mid-drag — Chromium releases it whenever the window is
 * deactivated — and a drag that then discarded its progress would leave the
 * sidebar exactly where it started. Moves and the release are read from the
 * window for as long as the gesture is active, so the drag survives losing
 * capture, and a window blur commits the width the pointer had reached.
 */
export function SidebarResizeHandle({
  width,
  onPreview,
  onCommit,
}: {
  width: number;
  onPreview(width: number | null): void;
  onCommit(width: number): void;
}) {
  const [maximum, setMaximum] = useState(() => Math.min(520, Math.floor(window.innerWidth / 2)));
  const [resizing, setResizing] = useState(false);
  const gesture = useRef<Gesture | null>(null);
  useEffect(() => {
    const resize = () => setMaximum(Math.min(520, Math.floor(window.innerWidth / 2)));
    window.addEventListener('resize', resize);
    return () => window.removeEventListener('resize', resize);
  }, []);
  // An unmount mid-drag (the sidebar collapsing, a shell remount) must not
  // leave window listeners driving a component that is gone.
  useEffect(() => () => gesture.current?.release(), []);
  const clamp = (value: number) => Math.max(200, Math.min(maximum, Math.round(value)));
  return (
    <div
      className="sidebar__resize-handle"
      role="separator"
      aria-label="Resize sidebar"
      aria-controls="feature-sidebar"
      aria-orientation="vertical"
      aria-valuemin={200}
      aria-valuemax={maximum}
      aria-valuenow={clamp(gesture.current?.next ?? width)}
      tabIndex={0}
      data-resizing={resizing}
      onPointerDown={(event) => {
        if (event.button !== 0 || gesture.current !== null) return;
        event.preventDefault();
        event.currentTarget.focus();
        try {
          event.currentTarget.setPointerCapture(event.pointerId);
        } catch {
          // Capture is a nicety here; the window listeners below carry the drag.
        }
        const start = clamp(width);
        const active: Gesture = {
          pointerId: event.pointerId,
          x: event.clientX,
          width: start,
          next: start,
          release: () => {
            window.removeEventListener('pointermove', move);
            window.removeEventListener('pointerup', finish);
            window.removeEventListener('pointercancel', finish);
            window.removeEventListener('blur', commit);
            gesture.current = null;
            setResizing(false);
          },
        };
        const move = (moveEvent: PointerEvent) => {
          if (moveEvent.pointerId !== active.pointerId) return;
          active.next = clamp(active.width + moveEvent.clientX - active.x);
          onPreview(active.next);
        };
        const commit = () => {
          active.release();
          onCommit(active.next);
        };
        const finish = (endEvent: PointerEvent) => {
          if (endEvent.pointerId !== active.pointerId) return;
          commit();
        };
        window.addEventListener('pointermove', move);
        window.addEventListener('pointerup', finish);
        window.addEventListener('pointercancel', finish);
        window.addEventListener('blur', commit);
        gesture.current = active;
        setResizing(true);
      }}
      onDoubleClick={() => onCommit(260)}
      onKeyDown={(event) => {
        const current = clamp(width);
        const next =
          event.key === 'ArrowLeft'
            ? current - 10
            : event.key === 'ArrowRight'
              ? current + 10
              : event.key === 'Home'
                ? 200
                : event.key === 'End'
                  ? maximum
                  : null;
        if (next === null) return;
        event.preventDefault();
        onCommit(clamp(next));
      }}
    />
  );
}
