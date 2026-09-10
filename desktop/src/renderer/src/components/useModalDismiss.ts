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

import { useEffect, useRef, type RefObject } from 'react';

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Escape-to-close, Tab focus trap, focus restoration, and body-scroll lock. */
export function useModalDismiss(
  ref: RefObject<HTMLElement | null>,
  onClose: () => void,
  active = true,
): void {
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!active) return;
    const node = ref.current;
    const previouslyFocused =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    const focusable = (): HTMLElement[] =>
      node === null ? [] : Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
    (focusable()[0] ?? node)?.focus();

    const onKey = (event: KeyboardEvent): void => {
      if (node?.querySelector('[role="dialog"][aria-modal="true"]') !== null) return;
      if (event.key === 'Escape') {
        event.preventDefault();
        onCloseRef.current();
        return;
      }
      if (event.key !== 'Tab' || node === null) return;
      const items = focusable();
      if (items.length === 0) {
        event.preventDefault();
        node.focus();
        return;
      }
      const first = items[0]!;
      const last = items[items.length - 1]!;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = previousOverflow;
      // Restore the opener only when nothing else has claimed focus. A flow that
      // closes the modal and focuses its own target in the same commit (adoption
      // focuses the adopted repository row) owns focus, and an unconditional
      // deferred restore would steal it back after that owner has settled.
      requestAnimationFrame(() => {
        const focused = document.activeElement;
        const abandoned =
          focused === null || focused === document.body || node?.contains(focused) === true;
        if (abandoned) previouslyFocused?.focus();
      });
    };
  }, [active, ref]);
}
