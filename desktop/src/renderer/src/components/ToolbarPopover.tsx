/**
 * The Bench toolbar popover: a transient surface anchored under the toolbar
 * button that opened it, on the raised material with a hairline ring and the
 * panel shadow — the macOS popover presentation, not a web modal. It takes no
 * scrim, traps no focus, and carries no close button: an outside pointer,
 * Escape, or a second click on its own trigger dismisses it, exactly like the
 * cockpit's disclosure menus (`useDetailsDismiss`).
 *
 * Callers own the trigger and the open state, so the toolbar can enforce the
 * one-popover-at-a-time rule across the attention bell and the update button.
 */
import { useEffect, useRef, type ReactNode, type RefObject } from 'react';

export interface ToolbarPopoverProps {
  open: boolean;
  /** Accessible name for the surface, and the handle tests address it by. */
  label: string;
  id?: string;
  /** Extra class on the surface, for per-popover width and content rules. */
  className?: string;
  /**
   * `aside` renders a complementary landmark (the attention inbox, which stays
   * a browsable list); `section` renders a region (the update notice).
   */
  as?: 'aside' | 'section';
  /** The trigger: pointer events inside it are the trigger's own toggle, and
   * Escape returns focus here. */
  anchorRef: RefObject<HTMLButtonElement | null>;
  onDismiss(): void;
  children: ReactNode;
}

export function ToolbarPopover({
  open,
  label,
  id,
  className,
  as = 'section',
  anchorRef,
  onDismiss,
  children,
}: ToolbarPopoverProps) {
  const surface = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node | null;
      if (target === null) return;
      if (surface.current?.contains(target) === true) return;
      // The trigger toggles itself on click; dismissing here too would make
      // the click reopen what the pointerdown just closed.
      if (anchorRef.current?.contains(target) === true) return;
      onDismiss();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      onDismiss();
      anchorRef.current?.focus();
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [anchorRef, onDismiss, open]);

  if (!open) return null;
  const surfaceProps = {
    ref: surface,
    ...(id === undefined ? {} : { id }),
    className: className === undefined ? 'toolbar-popover' : `toolbar-popover ${className}`,
    'aria-label': label,
    tabIndex: -1,
  };
  return as === 'aside' ? (
    <aside {...surfaceProps}>{children}</aside>
  ) : (
    <section {...surfaceProps}>{children}</section>
  );
}

/** The `position: relative` hull a trigger and its popover share. */
export function ToolbarPopoverAnchor({
  className,
  children,
}: {
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={
        className === undefined ? 'toolbar-popover-anchor' : `toolbar-popover-anchor ${className}`
      }
    >
      {children}
    </div>
  );
}
