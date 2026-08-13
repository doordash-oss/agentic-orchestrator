import { useEffect, useRef, type ReactNode } from 'react';

/**
 * Narrow presentation of the review-feedback scope panel: a modal drawer that
 * owns dismissal (close control, Escape, backdrop) and focus (moves in on
 * open, cycles while open, and the caller restores focus to the opener). The
 * panel content is identical to the wide rail; only the container changes.
 */
const FOCUSABLE = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';

export function ScopeDrawer({
  onClose,
  title,
  children,
}: {
  onClose(): void;
  title: string;
  children: ReactNode;
}) {
  const drawerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const drawer = drawerRef.current;
    // Focus starts on the close action so the first Tab lands inside the panel.
    drawer?.querySelector<HTMLElement>('.review-feedback-drawer__close')?.focus();
    const handleKeyDown = (event: globalThis.KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab' || drawer === null || drawer === undefined) return;
      const items = Array.from(drawer.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
        (item) => !item.hasAttribute('disabled'),
      );
      const first = items[0];
      const last = items[items.length - 1];
      if (first === undefined || last === undefined) return;
      // Contain focus: wrapping beats escaping into the feed behind the modal.
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    <div className="review-feedback-drawer__backdrop" onMouseDown={onClose}>
      <div
        ref={drawerRef}
        id="review-feedback-scope-drawer"
        className="review-feedback-drawer"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="review-feedback-drawer__header">
          <h3 className="review-feedback-drawer__title">{title}</h3>
          <button type="button" className="review-feedback-drawer__close" onClick={onClose}>
            Close filters
          </button>
        </header>
        {children}
      </div>
    </div>
  );
}
