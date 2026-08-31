/**
 * The window-chrome control cluster modeled on Claude Desktop's macOS window:
 * the sidebar collapse/expand toggle plus a magnifier that opens the Command
 * Palette (the same surface ⌘K opens), anchored immediately to the right of
 * the traffic lights. The shell renders exactly one instance and moves it
 * between homes: inside `.sidebar__header` while the sidebar is visible, and
 * in the toolbar's leading zone once the sidebar collapses — so the buttons
 * never leave the top-left corner and are never rendered twice.
 *
 * Both homes are macOS window drag regions; the cluster opts itself out via
 * `.sidebar__chrome-controls { -webkit-app-region: no-drag }` (app.css) so
 * the buttons stay clickable wherever they land.
 */
export function SidebarChromeControls({
  sidebarCollapsed,
  onToggleSidebar,
  onOpenPalette,
}: {
  sidebarCollapsed: boolean;
  onToggleSidebar(): void;
  onOpenPalette(): void;
}) {
  return (
    <div className="sidebar__chrome-controls">
      <button
        type="button"
        className="toolbar__sidebar-toggle"
        aria-label={sidebarCollapsed ? 'Show sidebar' : 'Hide sidebar'}
        aria-pressed={sidebarCollapsed}
        onClick={onToggleSidebar}
      >
        <svg
          aria-hidden="true"
          width="18"
          height="16"
          viewBox="0 0 18 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
        >
          <rect x="1.75" y="2.75" width="14.5" height="10.5" rx="2.75" />
          <line x1="6.75" y1="3.25" x2="6.75" y2="12.75" />
        </svg>
      </button>
      <button
        type="button"
        className="toolbar__palette-search"
        aria-label="Search features"
        onClick={onOpenPalette}
      >
        <svg
          aria-hidden="true"
          width="18"
          height="16"
          viewBox="0 0 18 16"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
        >
          <circle cx="8.25" cy="7.25" r="4.5" />
          <line x1="11.75" y1="10.75" x2="15" y2="14" />
        </svg>
      </button>
    </div>
  );
}
