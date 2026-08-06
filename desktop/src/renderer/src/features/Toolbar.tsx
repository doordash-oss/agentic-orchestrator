/**
 * The 52px translucent toolbar spanning the content side of the shell (never
 * the sidebar): a leading sidebar-collapse toggle, a center-leading title
 * block (feature name or "Overview", plus a `repo · branch` mono sub-line),
 * and — only while a feature is selected, per the mock — a trailing group
 * with the relocated attention bell, the cockpit's ⋯ overflow-menu slot, and
 * an inspector-toggle slot. Both slots are chrome-owned mount points the
 * cockpit portals its own controls into: at wide widths the cockpit mounts
 * a toggle button here for its trailing split-view pane, and mounts
 * nothing at narrow widths, where the cockpit's own in-content "Inspector"
 * button opens its drawer instead — so exactly one inspector control ever
 * shows at a time.
 *
 * On macOS this bar is the traffic-light-clearing drag region, same pattern
 * as the header it replaces: the bar itself is draggable, every interactive
 * child opts out via `-webkit-app-region: no-drag` (app.css).
 */
import type { Dispatch, RefObject, SetStateAction } from 'react';
import type { AttentionItem } from '../../../shared/ipc';
import { AttentionInbox, type AttentionDrafts } from './AttentionInbox';

export interface ToolbarAttentionProps {
  items: AttentionItem[];
  refresh(): Promise<AttentionItem[]>;
  featureLabel(featureId: string | undefined): string;
  drafts: AttentionDrafts;
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  onJump(featureId: string, attentionId?: string): void;
  openRequest?: { id: number; attentionId?: string } | null;
}

export interface ToolbarProps {
  sidebarCollapsed: boolean;
  onToggleSidebar(): void;
  title: string;
  subline?: string;
  /** Hidden entirely on Overview, per the mock. */
  showTrailing: boolean;
  attention?: ToolbarAttentionProps;
  /** The cockpit-owned ⋯ overflow menu portals into this node once mounted. */
  overflowSlotRef?(node: HTMLDivElement | null): void;
  /** The cockpit-owned inspector-toggle button portals into this node once mounted. */
  inspectorSlotRef?(node: HTMLDivElement | null): void;
  /** Overview's sole "New feature" entry point — shown only when Overview is selected. */
  showNewFeature?: boolean;
  onNewFeature?(): void;
  newFeatureButtonRef?: RefObject<HTMLButtonElement | null>;
}

export function Toolbar({
  sidebarCollapsed,
  onToggleSidebar,
  title,
  subline,
  showTrailing,
  attention,
  overflowSlotRef,
  inspectorSlotRef,
  showNewFeature = false,
  onNewFeature,
  newFeatureButtonRef,
}: ToolbarProps) {
  return (
    <header className="toolbar" aria-label="Workspace toolbar">
      <div className="toolbar__leading">
        <button
          type="button"
          className="toolbar__sidebar-toggle"
          aria-label={sidebarCollapsed ? 'Show sidebar' : 'Hide sidebar'}
          aria-pressed={sidebarCollapsed}
          onClick={onToggleSidebar}
        >
          <span aria-hidden="true">▥</span>
        </button>
      </div>
      <div className="toolbar__title">
        <p className="toolbar__title-name">{title}</p>
        {subline !== undefined ? <p className="toolbar__title-subline">{subline}</p> : null}
      </div>
      <div className="toolbar__trailing">
        {/* The bell stays mounted regardless of selection — hidden visually
         * on Overview, per the mock — so its drawer, ⌘⇧A binding, and
         * route-driven deep links stay live even before a feature is picked. */}
        {attention !== undefined ? (
          <AttentionInbox {...attention} hideTrigger={!showTrailing} />
        ) : null}
        {showTrailing ? (
          <>
            <div className="toolbar__overflow-slot" ref={overflowSlotRef} />
            <div className="toolbar__inspector-slot" ref={inspectorSlotRef} />
          </>
        ) : null}
        {showNewFeature ? (
          <button
            ref={newFeatureButtonRef}
            type="button"
            className="toolbar__new-feature"
            onClick={onNewFeature}
          >
            New feature
          </button>
        ) : null}
      </div>
    </header>
  );
}
