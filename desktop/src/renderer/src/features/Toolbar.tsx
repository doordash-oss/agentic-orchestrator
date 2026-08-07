/**
 * The 52px translucent toolbar spanning the content side of the shell (never
 * the sidebar): a leading sidebar-collapse toggle, a center-leading title
 * block (feature name or "Overview", plus a `repo · branch` mono sub-line),
 * and a trailing group carrying the always-visible attention bell, the
 * transient update button, and — only while a feature is selected, per the
 * mock — the cockpit's ⋯ overflow-menu slot and an inspector-toggle slot. Both
 * slots are chrome-owned mount points the cockpit portals its own controls
 * into: at wide widths the cockpit mounts a toggle button here for its
 * trailing split-view pane, and mounts nothing at narrow widths, where the
 * cockpit's own in-content "Inspector" button opens its drawer instead — so
 * exactly one inspector control ever shows at a time.
 *
 * The bell is a permanent fixture on every selection (Overview, a feature, the
 * Settings view) so its popover always has an anchor and ⌘⇧A, the tray, and
 * attention deep links work from anywhere. The toolbar owns which of the two
 * popovers is open, which is what makes them mutually exclusive: opening one
 * closes the other, so at most one transient surface hangs off the toolbar.
 *
 * On macOS this bar is the traffic-light-clearing drag region, same pattern
 * as the header it replaces: the bar itself is draggable, every interactive
 * child opts out via `-webkit-app-region: no-drag` (app.css).
 */
import { useState, type Dispatch, type RefObject, type SetStateAction } from 'react';
import type { AttentionItem, UpdateState } from '../../../shared/ipc';
import { AttentionInbox, type AttentionDrafts } from './AttentionInbox';
import { UpdatePopover } from '../components/UpdatePopover';

export interface ToolbarAttentionProps {
  items: AttentionItem[];
  refresh(): Promise<AttentionItem[]>;
  featureLabel(featureId: string | undefined): string;
  drafts: AttentionDrafts;
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  onJump(featureId: string, attentionId?: string): void;
  openRequest?: { id: number; attentionId?: string } | null;
}

export interface ToolbarUpdateProps {
  update: UpdateState | null;
  dismissedVersion: string | null;
  scheduling: boolean;
  onDismiss(version: string): void;
  onOpenSettings(): void;
  onInstallWhenIdle(): Promise<void>;
}

export interface ToolbarProps {
  sidebarCollapsed: boolean;
  onToggleSidebar(): void;
  title: string;
  subline?: string;
  /** Gates the cockpit-owned slots, which only exist while a feature is open. */
  showTrailing: boolean;
  attention?: ToolbarAttentionProps;
  update?: ToolbarUpdateProps;
  /** The cockpit-owned status chip, primary verbs, and completion controls portal into this node once mounted. */
  actionsSlotRef?(node: HTMLDivElement | null): void;
  /** The cockpit-owned overflow menu portals into this node once mounted. */
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
  update,
  actionsSlotRef,
  overflowSlotRef,
  inspectorSlotRef,
  showNewFeature = false,
  onNewFeature,
  newFeatureButtonRef,
}: ToolbarProps) {
  const [openPopover, setOpenPopover] = useState<'attention' | 'update' | null>(null);

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
        {attention !== undefined ? (
          <AttentionInbox
            {...attention}
            open={openPopover === 'attention'}
            onOpenChange={(next) => setOpenPopover(next ? 'attention' : null)}
          />
        ) : null}
        {update !== undefined ? (
          <UpdatePopover
            {...update}
            open={openPopover === 'update'}
            onOpenChange={(next) => setOpenPopover(next ? 'update' : null)}
          />
        ) : null}
        {showTrailing ? (
          <>
            <div className="toolbar__actions-slot" ref={actionsSlotRef} />
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
