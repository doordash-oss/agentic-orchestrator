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
 * The Settings window's pane catalogue: today's sections, in today's order,
 * each with the label the source list and the window title both show and
 * the glyph its row carries. Pane ids are the persisted values, so a label
 * can be reworded without invalidating a stored preference.
 */
import type { SVGProps } from 'react';
import { SETTINGS_PANES, type SettingsFocus, type SettingsPaneId } from '../../../shared/ipc';

/** A within-pane focus intent plus a sequence number, so a repeat route re-fires. */
export interface PaneFocusIntent {
  intent: SettingsFocus;
  seq: number;
}
import {
  AdvancedIcon,
  AppearanceIcon,
  BellIcon,
  DefaultsIcon,
  DiagnosticsIcon,
  FolderIcon,
  ProvidersIcon,
  ServersIcon,
  UpdateIcon,
} from '../components/icons';

export interface SettingsPaneDescriptor {
  id: SettingsPaneId;
  label: string;
  Icon(props: SVGProps<SVGSVGElement>): React.ReactElement;
}

export const SETTINGS_PANE_CATALOGUE: readonly SettingsPaneDescriptor[] = [
  { id: 'workspace-roots', label: 'Workspace roots', Icon: FolderIcon },
  { id: 'servers', label: 'Servers', Icon: ServersIcon },
  { id: 'providers', label: 'Providers', Icon: ProvidersIcon },
  { id: 'appearance', label: 'Appearance', Icon: AppearanceIcon },
  { id: 'updates', label: 'Updates', Icon: UpdateIcon },
  { id: 'notifications', label: 'Notifications', Icon: BellIcon },
  { id: 'diagnostics', label: 'Diagnostics', Icon: DiagnosticsIcon },
  { id: 'advanced', label: 'Advanced', Icon: AdvancedIcon },
  { id: 'workspace-defaults', label: 'Workspace defaults', Icon: DefaultsIcon },
];

// The catalogue is the pane order; keeping it in lockstep with the persisted
// id list means a pane can never be added to one and forgotten in the other.
if (SETTINGS_PANE_CATALOGUE.length !== SETTINGS_PANES.length) {
  throw new Error('The Settings pane catalogue and SETTINGS_PANES disagree.');
}

export function settingsPaneLabel(id: SettingsPaneId): string {
  return SETTINGS_PANE_CATALOGUE.find((pane) => pane.id === id)?.label ?? 'Settings';
}
