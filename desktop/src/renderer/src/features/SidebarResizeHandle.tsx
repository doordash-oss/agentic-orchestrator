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

/** Pointer capture keeps resizing active outside the narrow divider. */
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
  const gesture = useRef<{ pointerId: number; x: number; width: number; next: number } | null>(
    null,
  );
  useEffect(() => {
    const resize = () => setMaximum(Math.min(520, Math.floor(window.innerWidth / 2)));
    window.addEventListener('resize', resize);
    return () => window.removeEventListener('resize', resize);
  }, []);
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
        if (event.button !== 0) return;
        event.preventDefault();
        event.currentTarget.focus();
        event.currentTarget.setPointerCapture(event.pointerId);
        gesture.current = {
          pointerId: event.pointerId,
          x: event.clientX,
          width: clamp(width),
          next: clamp(width),
        };
        setResizing(true);
      }}
      onPointerMove={(event) => {
        const active = gesture.current;
        if (!active || active.pointerId !== event.pointerId) return;
        active.next = clamp(active.width + event.clientX - active.x);
        onPreview(active.next);
      }}
      onPointerUp={(event) => {
        const active = gesture.current;
        if (!active || active.pointerId !== event.pointerId) return;
        gesture.current = null;
        setResizing(false);
        onCommit(active.next);
        event.currentTarget.releasePointerCapture(event.pointerId);
      }}
      onLostPointerCapture={() => {
        gesture.current = null;
        setResizing(false);
        onPreview(null);
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
