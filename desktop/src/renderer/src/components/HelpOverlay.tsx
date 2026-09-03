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
import { COMMAND_CATALOGUE, displayAccelerator } from '../../../shared/commands';
import type { RoutedRequest } from '../../../shared/ipc';

export function HelpOverlay({ routeRequest }: { routeRequest: RoutedRequest | null }) {
  const [open, setOpen] = useState(false);
  const closeRef = useRef<HTMLButtonElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (routeRequest?.event.target !== 'help') return;
    returnFocus.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setOpen(true);
  }, [routeRequest]);
  useEffect(() => {
    if (!open) return;
    closeRef.current?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setOpen(false);
        requestAnimationFrame(() => returnFocus.current?.focus());
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [open]);
  if (!open) return null;
  const close = () => {
    setOpen(false);
    requestAnimationFrame(() => returnFocus.current?.focus());
  };
  return (
    <div className="help-overlay__backdrop" onMouseDown={close}>
      <section
        role="dialog"
        aria-modal="true"
        aria-label="Keyboard shortcuts"
        className="help-overlay"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header>
          <div>
            <p className="eyebrow-label">Command catalogue</p>
            <h2>Keyboard shortcuts</h2>
          </div>
          <button ref={closeRef} type="button" onClick={close}>
            Close
          </button>
        </header>
        <dl>
          {COMMAND_CATALOGUE.filter((command) => command.accelerator).map((command) => (
            <div key={command.id}>
              <dt>{command.label}</dt>
              <dd>
                <kbd>{displayAccelerator(command.accelerator ?? '')}</kbd>
              </dd>
            </div>
          ))}
        </dl>
      </section>
    </div>
  );
}
