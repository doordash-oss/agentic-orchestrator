import { useEffect, type RefObject } from 'react';

/**
 * Native `<details>` stays open on outside interaction; close it on an outside
 * pointer or Escape so a popup never lingers over drawers opened elsewhere.
 * `focusSelector` names the summary to return focus to after Escape.
 */
export function useDetailsDismiss(
  ref: RefObject<HTMLDetailsElement | null>,
  focusSelector?: string,
): void {
  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      const menu = ref.current;
      if (menu?.open === true && !menu.contains(event.target as Node)) menu.open = false;
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      const menu = ref.current;
      if (menu?.open !== true) return;
      menu.open = false;
      if (focusSelector !== undefined) menu.querySelector<HTMLElement>(focusSelector)?.focus();
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [focusSelector, ref]);
}
