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
 * The Settings window's root: a System Settings-style source list on the
 * sidebar material beside exactly one pane of content at a time.
 *
 * The container is all that is new here — every pane's controls, headings,
 * aria-labels, confirmation dialogs, data fetching, and event subscriptions
 * are the ones the settings surface already had. The list mirrors the main
 * window's sidebar: one single-select listbox with a roving tabindex where
 * arrow keys move focus and selection together.
 *
 * The selected pane is the window's title and is written back to settings on
 * every switch, so reopening the window — or relaunching the app — lands
 * where it was left. The first-ever open lands on Workspace roots.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import type { SettingsPaneId } from '../../shared/ipc';
import { defaultSettingsWindowPrefs } from '../../shared/ipc';
import { SettingsPanel } from './features/SettingsPanel';
import {
  SETTINGS_PANE_CATALOGUE,
  settingsPaneLabel,
  type PaneFocusIntent,
} from './features/settingsPanes';
import { useSystemAccentMirror, useTheme } from './hooks';

export default function SettingsWindow() {
  // Mirrors the resolved theme onto <html data-theme> in this window too, and
  // follows the main process's cross-window theme broadcast — the pane's own
  // Appearance radiogroup owns a second instance of the same hook.
  useTheme();
  useSystemAccentMirror();

  // null until the persisted pane is restored, so the window never paints one
  // pane and then jumps to another.
  const [pane, setPane] = useState<SettingsPaneId | null>(null);
  const [focusIntent, setFocusIntent] = useState<PaneFocusIntent | null>(null);
  const focusSeq = useRef(0);
  const persistence = useRef<Promise<void>>(Promise.resolve());

  useEffect(() => {
    let alive = true;
    window.agentico
      .getSettings()
      .then((settings) => {
        if (!alive) return;
        setPane(settings.settingsWindow.pane);
        // A cold-open focus intent rides the persisted prefs one way; the
        // window consumes it (fires it at the pane) and clears it so a later
        // plain open does not re-fire it.
        const intent = settings.settingsWindow.focus;
        if (intent !== undefined) {
          focusSeq.current += 1;
          setFocusIntent({ intent, seq: focusSeq.current });
          void window.agentico
            .updateSettings({
              settingsWindow: { ...settings.settingsWindow, focus: undefined },
            })
            .catch(() => undefined);
        }
      })
      .catch(() => {
        if (alive) setPane(defaultSettingsWindowPrefs().pane);
      });
    return () => {
      alive = false;
    };
  }, []);

  // A deep link arriving while the window is already open switches the pane
  // (and refreshes its focus intent); arriving with it closed lands there
  // because the main process wrote the pane before raising the window.
  useEffect(
    () =>
      window.agentico.onRouteRequest((event) => {
        if (event.target === 'settings' && event.settingsSection !== undefined) {
          setPane(event.settingsSection);
        }
        if (event.settingsFocus !== undefined) {
          focusSeq.current += 1;
          setFocusIntent({ intent: event.settingsFocus, seq: focusSeq.current });
        }
      }),
    [],
  );

  /** Persist failures never block the switch — the pane is presentation only. */
  const selectPane = useCallback((next: SettingsPaneId) => {
    setPane(next);
    const write = () =>
      window.agentico
        .getSettings()
        .then((settings) =>
          window.agentico.updateSettings({
            settingsWindow: { ...settings.settingsWindow, pane: next },
          }),
        )
        .then(() => undefined)
        .catch(() => undefined);
    persistence.current = persistence.current.then(write, write);
  }, []);

  useEffect(() => {
    if (pane !== null) {
      // The window title tracks the pane, so the Window menu and ⌘-Tab name
      // the pane too. The visible title is the pane's own heading, which sits
      // where System Settings puts it — the inset title bar shows no text.
      document.title = settingsPaneLabel(pane);
    }
  }, [pane]);

  /**
   * Roving tabindex with selection following focus, the same behaviour (and
   * the same key set) the main window's feature sidebar implements.
   */
  const onListKeyDown = (event: React.KeyboardEvent<HTMLDivElement>): void => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const rows = Array.from(event.currentTarget.querySelectorAll<HTMLElement>('[role="option"]'));
    if (rows.length === 0) return;
    const current = rows.findIndex((row) => row === document.activeElement);
    const next =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? rows.length - 1
          : event.key === 'ArrowDown'
            ? (current + 1) % rows.length
            : (Math.max(current, 0) - 1 + rows.length) % rows.length;
    const target = rows[next];
    if (target === undefined) return;
    event.preventDefault();
    target.focus();
    // The rows are rendered straight from the catalogue and no row is ever
    // hidden, so DOM position and catalogue position are the same index.
    const id = SETTINGS_PANE_CATALOGUE[next]?.id;
    if (id !== undefined) selectPane(id);
  };

  if (pane === null) {
    return (
      <div className="settings-window">
        <p role="status" aria-live="polite" className="settings-window__restoring">
          Restoring settings…
        </p>
      </div>
    );
  }

  return (
    <div className="settings-window">
      <nav className="settings-window__nav" aria-label="Settings panes">
        <div className="settings-window__nav-header" aria-hidden="true" />
        <div
          className="settings-window__nav-list"
          role="listbox"
          aria-label="Settings panes"
          onKeyDown={onListKeyDown}
        >
          {SETTINGS_PANE_CATALOGUE.map(({ id, label, Icon }) => {
            const selected = id === pane;
            return (
              <div
                key={id}
                id={`settings-pane-${id}`}
                role="option"
                aria-selected={selected}
                tabIndex={selected ? 0 : -1}
                className="settings-window__pane-row"
                data-selected={selected}
                onClick={() => selectPane(id)}
              >
                <Icon className="settings-window__pane-glyph" />
                <span className="settings-window__pane-label">{label}</span>
              </div>
            );
          })}
        </div>
      </nav>
      <div className="settings-window__pane">
        <SettingsPanel pane={pane} focusIntent={focusIntent} />
      </div>
    </div>
  );
}
