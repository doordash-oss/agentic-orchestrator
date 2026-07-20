import type { AppRouteEvent } from './ipc';

export type CommandGroup = 'window' | 'navigation' | 'attention' | 'assistant' | 'feature' | 'bulk';

export type CommandId =
  | 'global.palette'
  | 'global.help'
  | 'global.show'
  | 'global.home'
  | 'global.settings'
  | 'global.attention'
  | 'global.ama'
  | 'global.bulk'
  | 'global.quit'
  | 'feature.start'
  | 'feature.pause-stop'
  | 'feature.resume'
  | 'feature.retry'
  | 'feature.restart';

export interface CommandDescriptor {
  id: CommandId;
  label: string;
  group: CommandGroup;
  accelerator?: string;
  target?: AppRouteEvent['target'];
}

export const COMMAND_CATALOGUE: readonly CommandDescriptor[] = [
  {
    id: 'global.show',
    label: 'Show Agentico',
    group: 'window',
    accelerator: 'CommandOrControl+Shift+0',
  },
  {
    id: 'global.palette',
    label: 'Command Palette',
    group: 'navigation',
    accelerator: 'CommandOrControl+K',
    target: 'palette',
  },
  {
    id: 'global.help',
    label: 'Keyboard shortcuts',
    group: 'navigation',
    accelerator: 'CommandOrControl+/',
    target: 'help',
  },
  {
    id: 'global.home',
    label: 'Home',
    group: 'navigation',
    accelerator: 'CommandOrControl+1',
    target: 'home',
  },
  {
    id: 'global.settings',
    label: 'Settings',
    group: 'navigation',
    accelerator: 'CommandOrControl+,',
    target: 'settings',
  },
  {
    id: 'global.attention',
    label: 'Attention',
    group: 'attention',
    accelerator: 'CommandOrControl+Shift+A',
    target: 'attention',
  },
  {
    id: 'global.ama',
    label: 'AMA',
    group: 'assistant',
    accelerator: 'CommandOrControl+Shift+M',
    target: 'ama',
  },
  {
    id: 'global.bulk',
    label: 'Bulk Resume / Retry',
    group: 'bulk',
    accelerator: 'CommandOrControl+Shift+B',
    target: 'bulk',
  },
  {
    id: 'global.quit',
    label: 'Quit Agentico',
    group: 'window',
    accelerator: 'CommandOrControl+Q',
  },
  { id: 'feature.start', label: 'Start Feature', group: 'feature' },
  { id: 'feature.pause-stop', label: 'Pause / Stop Feature', group: 'feature' },
  { id: 'feature.resume', label: 'Resume Feature', group: 'feature' },
  { id: 'feature.retry', label: 'Retry Feature', group: 'feature' },
  { id: 'feature.restart', label: 'Restart Feature', group: 'feature' },
];

export function commandById(id: CommandId): CommandDescriptor {
  const command = COMMAND_CATALOGUE.find((entry) => entry.id === id);
  if (command === undefined) {
    throw new Error(`Unknown command: ${id}`);
  }
  return command;
}

export function displayAccelerator(accelerator: string, platform = navigator.platform): string {
  return accelerator
    .replace('CommandOrControl', platform.toLowerCase().includes('mac') ? '⌘' : 'Ctrl')
    .replaceAll('+', ' ');
}
