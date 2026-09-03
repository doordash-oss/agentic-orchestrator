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

import { useEffect, useRef, type ReactNode } from 'react';

/**
 * The narrow-width inspector presentation, shared by every surface that has an
 * inspector: the drawer owns dismissal and focus, the caller supplies whichever
 * facts its surface inspects.
 */
export function InspectorDrawer({
  onClose,
  title = 'Feature inspector',
  children,
}: {
  onClose(): void;
  /** Accessible name and heading; surfaces scope it (e.g. "Pass inspector"). */
  title?: string;
  children: ReactNode;
}) {
  const drawerRef = useRef<HTMLElement>(null);
  useEffect(() => {
    drawerRef.current
      ?.querySelector<HTMLElement>(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
      )
      ?.focus();
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    <div className="cockpit__drawer-backdrop" onMouseDown={onClose}>
      <aside
        ref={drawerRef}
        id="cockpit-inspector-drawer"
        className="cockpit__drawer"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header>
          <h3>{title}</h3>
          <button type="button" onClick={onClose}>
            Close inspector
          </button>
        </header>
        {children}
      </aside>
    </div>
  );
}
