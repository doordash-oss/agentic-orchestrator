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
