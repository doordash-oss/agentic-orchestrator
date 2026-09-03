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

/**
 * The sidebar footer's server control: the current server's name and status
 * dot acting as a button that opens a macOS-style popover of every known
 * server (the live registry scan union the persisted known-servers list,
 * health-probed while open). Choosing a healthy other server switches
 * immediately — no confirmation — and the workspace rides the connection
 * shell's ready→attaching→ready transition.
 *
 * The popover is the single switcher surface: the menu and palette command
 * only route here. A fixed "Add Server…" footer row deep-links to
 * Settings → Servers with the paste field focused (the only add
 * affordance).
 */
import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from 'react';
import type { ServerListRow } from '../../../shared/ipc';
import { ToolbarPopover, ToolbarPopoverAnchor } from './ToolbarPopover';

const HEALTH_LABEL: Record<ServerListRow['health'], string> = {
  healthy: 'Available',
  unreachable: 'Unreachable',
  probing: 'Checking…',
};

function statusLabel(row: ServerListRow): string {
  return row.current ? 'Connected' : HEALTH_LABEL[row.health];
}

export function ServerSwitcher({
  currentLabel,
  tone,
  enabled,
  openRequest = null,
  onRouteHandled,
}: {
  /** Footer text: the connected server's display name (or generic label). */
  currentLabel: string;
  /** The existing footer tone contract: ready | progress | error | ama. */
  tone: string;
  /** False unless the workspace is connected — the popover only opens ready. */
  enabled: boolean;
  /** Route-and-focus signal from the global "Switch Server…" command. */
  openRequest?: { id: number } | null;
  /**
   * Fires once an openRequest has been consumed, so the owner can clear the
   * signal: this same control is re-homed across a breakpoint, and a stale
   * id would otherwise reopen the popover on the remount.
   */
  onRouteHandled?(): void;
}) {
  const [open, setOpen] = useState(false);
  const [rows, setRows] = useState<readonly ServerListRow[] | null>(null);
  const [highlight, setHighlight] = useState(0);
  const anchorRef = useRef<HTMLButtonElement | null>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const handledRoute = useRef<number | null>(null);

  // The global command focuses and opens this control; a repeated route for
  // the same id is a no-op exactly like the shell's own routed requests.
  useEffect(() => {
    if (openRequest === null || !enabled) return;
    if (handledRoute.current === openRequest.id) return;
    handledRoute.current = openRequest.id;
    anchorRef.current?.focus();
    setOpen(true);
    onRouteHandled?.();
  }, [enabled, openRequest, onRouteHandled]);

  // All health probing is bounded by the popover's open lifetime: opening
  // kicks an immediate round, closing stops every poll.
  useEffect(() => {
    if (!open) return;
    let alive = true;
    void window.agentico
      .listServers()
      .then((snapshot) => {
        if (alive) setRows(snapshot.rows);
      })
      .catch(() => {});
    void window.agentico
      .probeServers({ open: true })
      .then((snapshot) => {
        if (alive) setRows(snapshot.rows);
      })
      .catch(() => {});
    const unsubscribe = window.agentico.onServersChanged((snapshot) => {
      setRows(snapshot.rows);
    });
    return () => {
      alive = false;
      unsubscribe();
      void window.agentico.probeServers({ open: false }).catch(() => {});
    };
  }, [open]);

  // Keep the highlight on a row that still exists as probe results stream in.
  useEffect(() => {
    if (rows !== null && rows.length > 0 && highlight >= rows.length) {
      setHighlight(rows.length - 1);
    }
  }, [highlight, rows]);

  // Follow keyboard navigation into focus; hover highlight never steals it.
  useEffect(() => {
    const list = listRef.current;
    if (list?.contains(document.activeElement)) {
      list.querySelectorAll('button')[highlight]?.focus();
    }
  }, [highlight]);

  const choose = useCallback((row: ServerListRow) => {
    if (row.current || row.health !== 'healthy') {
      return;
    }
    setOpen(false);
    anchorRef.current?.focus();
    void window.agentico.switchConnectionServer({ serverKey: row.serverKey }).catch(() => {
      // The connection shell owns the failure surface; the popover is closed
      // either way.
    });
  }, []);

  // The only way to teach the app a new server from the switcher: close and
  // deep-link to Settings → Servers with the paste field focused.
  const addServer = useCallback(() => {
    setOpen(false);
    anchorRef.current?.focus();
    void window.agentico
      .openSettingsWindow({ section: 'servers', focus: 'add-server' })
      .catch(() => {
        // Opening settings is best-effort; the popover is closed either way.
      });
  }, []);

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    const count = rows?.length ?? 0;
    if (count === 0) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setHighlight((index) => (index + 1) % count);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setHighlight((index) => (index + count - 1) % count);
    } else if (event.key === 'Home') {
      event.preventDefault();
      setHighlight(0);
    } else if (event.key === 'End') {
      event.preventDefault();
      setHighlight(count - 1);
    }
  };

  return (
    <ToolbarPopoverAnchor className="sidebar__server">
      <button
        ref={anchorRef}
        type="button"
        className="sidebar__server-control"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`${currentLabel} — switch server`}
        disabled={!enabled}
        onClick={() => setOpen((value) => !value)}
      >
        <span className="sidebar__server-dot" aria-hidden="true" data-tone={tone}>
          ●
        </span>
        <span className="sidebar__server-name">{currentLabel}</span>
      </button>
      <ToolbarPopover
        open={open}
        label="Switch server"
        className="server-switcher"
        anchorRef={anchorRef}
        onDismiss={() => setOpen(false)}
      >
        {rows === null ? (
          <p className="server-switcher__empty" role="status">
            Checking servers…
          </p>
        ) : rows.length === 0 ? (
          <p className="server-switcher__empty">No other servers found.</p>
        ) : (
          <div
            className="server-switcher__list"
            role="listbox"
            aria-label="Servers"
            onKeyDown={onKeyDown}
            ref={listRef}
          >
            {rows.map((row, index) => {
              const selected = index === highlight;
              const reachable = !row.current && row.health === 'healthy';
              const displayName = row.nickname ?? row.name ?? 'Unnamed server';
              // Remote rows have no local runtime dir to disambiguate by.
              const location = row.runtimeDir === undefined ? '' : ` at ${row.runtimeDir}`;
              return (
                <button
                  key={row.serverKey}
                  type="button"
                  role="option"
                  aria-selected={selected}
                  aria-disabled={!reachable}
                  tabIndex={selected ? 0 : -1}
                  aria-label={`${displayName}${location} — ${statusLabel(row)}`}
                  className="server-switcher__row"
                  data-selected={selected}
                  data-health={row.health}
                  data-current={row.current}
                  onMouseEnter={() => setHighlight(index)}
                  onFocus={() => setHighlight(index)}
                  onClick={() => choose(row)}
                >
                  <span className="server-switcher__check" aria-hidden="true">
                    {row.current ? '✓' : ''}
                  </span>
                  <span className="server-switcher__primary" aria-hidden="true">
                    {displayName}
                    {row.kind === 'remote' ? (
                      <span className="settings-panel__server-kind" data-kind="remote">
                        Remote
                      </span>
                    ) : null}
                  </span>
                  <span className="server-switcher__runtime" aria-hidden="true">
                    {row.runtimeDir ?? ''}
                  </span>
                  <span className="server-switcher__status" aria-hidden="true">
                    {statusLabel(row)}
                  </span>
                </button>
              );
            })}
          </div>
        )}
        <div className="server-switcher__footer">
          <button
            type="button"
            className="server-switcher__row server-switcher__add"
            onClick={addServer}
          >
            <span className="server-switcher__primary">Add Server…</span>
          </button>
        </div>
      </ToolbarPopover>
    </ToolbarPopoverAnchor>
  );
}
