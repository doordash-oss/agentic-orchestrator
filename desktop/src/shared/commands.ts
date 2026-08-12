import type { AppRouteEvent } from './ipc';

export type CommandGroup =
  'window' | 'file' | 'navigation' | 'view' | 'attention' | 'assistant' | 'feature' | 'bulk';

export type GlobalCommandId =
  | 'global.palette'
  | 'global.help'
  | 'global.show'
  | 'global.home'
  | 'global.settings'
  | 'global.attention'
  | 'global.ama'
  | 'global.bulk'
  | 'global.quit'
  | 'global.new-feature'
  | 'global.close-window'
  | 'global.toggle-sidebar'
  | 'global.toggle-inspector'
  | 'global.switch-server';

/**
 * The per-feature action catalogue, in the order the Feature menu and the
 * palette's Feature group list it. Every id but `feature.configuration` maps
 * onto the server action catalogue entry of the same name (drop the prefix);
 * Configuration is a local editor, so it is enabled whenever a feature is
 * selected.
 */
export type FeatureCommandId =
  | 'feature.start'
  | 'feature.pause-stop'
  | 'feature.resume'
  | 'feature.retry'
  | 'feature.restart'
  | 'feature.rewind'
  | 'feature.publish'
  | 'feature.merge'
  | 'feature.mark-done'
  | 'feature.cleanup'
  | 'feature.rebase'
  | 'feature.refactor'
  | 'feature.review-feedback'
  | 'feature.configuration'
  | 'feature.delete';

export type CommandId = GlobalCommandId | FeatureCommandId;

export interface CommandDescriptor {
  id: CommandId;
  label: string;
  group: CommandGroup;
  accelerator?: string;
  target?: AppRouteEvent['target'];
  /**
   * Whether the ⌘K palette lists this command. Feature commands are always
   * listed; global commands opt in, so the deliberate menu-only exclusions
   * (Show Agentico, Quit, Close Window) stay out of the palette.
   */
  paletteVisible?: boolean;
  /**
   * True when a renderer listener is also bound to this accelerator and the
   * command is a toggle, where firing twice would cancel itself out. The menu
   * item is built with `registerAccelerator: false`, which on Linux and
   * Windows displays the binding without registering it, leaving the renderer
   * as the single handler. On macOS the flag is ignored — and does not need to
   * apply, because a native menu key equivalent consumes the event, so the
   * renderer listener never sees it. Either way the toggle fires exactly once.
   */
  rendererAccelerator?: boolean;
}

export const COMMAND_CATALOGUE: readonly CommandDescriptor[] = [
  {
    id: 'global.show',
    label: 'Show Agentico',
    group: 'window',
    accelerator: 'CommandOrControl+Shift+0',
  },
  {
    id: 'global.new-feature',
    label: 'New Feature',
    group: 'file',
    accelerator: 'CommandOrControl+N',
    target: 'new-feature',
    paletteVisible: true,
  },
  {
    // The platform `close` role owns the behavior and the ⌘W binding; the
    // catalogue entry exists so the parity guard and the shortcut regression
    // can assert the item, and it is deliberately not a palette entry.
    id: 'global.close-window',
    label: 'Close Window',
    group: 'file',
    accelerator: 'CommandOrControl+W',
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
    paletteVisible: true,
  },
  {
    id: 'global.home',
    // Renamed from "Home" now that the Bench sidebar's pinned row (and every
    // other surface pointing at it) calls this destination Overview; the id
    // and route target are untouched so every existing dispatch path (menu,
    // palette, ⌘1) keeps working unchanged.
    label: 'Overview',
    group: 'navigation',
    accelerator: 'CommandOrControl+1',
    target: 'home',
    paletteVisible: true,
  },
  {
    id: 'global.settings',
    label: 'Settings',
    group: 'navigation',
    accelerator: 'CommandOrControl+,',
    target: 'settings',
    paletteVisible: true,
  },
  {
    // Pure route-and-focus: the footer switcher stays the single switcher
    // UI — the menu and palette never enumerate servers themselves.
    id: 'global.switch-server',
    label: 'Switch Server…',
    group: 'navigation',
    target: 'switch-server',
    paletteVisible: true,
  },
  {
    id: 'global.toggle-sidebar',
    label: 'Show/Hide Sidebar',
    group: 'view',
    accelerator: 'Command+Control+S',
    target: 'toggle-sidebar',
    paletteVisible: true,
    rendererAccelerator: true,
  },
  {
    id: 'global.toggle-inspector',
    label: 'Show/Hide Inspector',
    group: 'view',
    target: 'toggle-inspector',
    paletteVisible: true,
  },
  {
    id: 'global.attention',
    label: 'Attention',
    group: 'attention',
    accelerator: 'CommandOrControl+Shift+A',
    target: 'attention',
    paletteVisible: true,
  },
  {
    id: 'global.ama',
    label: 'AMA',
    group: 'assistant',
    accelerator: 'CommandOrControl+Shift+M',
    target: 'ama',
    paletteVisible: true,
  },
  {
    id: 'global.bulk',
    label: 'Bulk Resume / Retry',
    group: 'bulk',
    accelerator: 'CommandOrControl+Shift+B',
    target: 'bulk',
    paletteVisible: true,
  },
  {
    id: 'global.quit',
    label: 'Quit Agentico',
    group: 'window',
    accelerator: 'CommandOrControl+Q',
  },
  // The fifteen per-feature verbs, in Feature-menu order. Labels are the
  // canonical ones every surface uses: the menu, the palette, and the
  // cockpit's own controls all say the same word for the same action.
  { id: 'feature.start', label: 'Start', group: 'feature' },
  { id: 'feature.pause-stop', label: 'Stop', group: 'feature' },
  { id: 'feature.resume', label: 'Resume', group: 'feature' },
  { id: 'feature.retry', label: 'Retry', group: 'feature' },
  { id: 'feature.restart', label: 'Restart', group: 'feature' },
  { id: 'feature.rewind', label: 'Rewind', group: 'feature' },
  { id: 'feature.publish', label: 'Publish', group: 'feature' },
  { id: 'feature.merge', label: 'Merge', group: 'feature' },
  { id: 'feature.mark-done', label: 'Mark done', group: 'feature' },
  { id: 'feature.cleanup', label: 'Clean up', group: 'feature' },
  { id: 'feature.rebase', label: 'Rebase', group: 'feature' },
  { id: 'feature.refactor', label: 'Refactor', group: 'feature' },
  { id: 'feature.review-feedback', label: 'Address review feedback', group: 'feature' },
  { id: 'feature.configuration', label: 'Configuration', group: 'feature' },
  { id: 'feature.delete', label: 'Delete', group: 'feature' },
];

/** Every feature command, in Feature-menu order. */
export const FEATURE_COMMANDS: readonly CommandDescriptor[] = COMMAND_CATALOGUE.filter(
  (command) => command.group === 'feature',
);

/** The Feature-menu / palette order as plain ids. */
export const FEATURE_COMMAND_IDS: readonly FeatureCommandId[] = FEATURE_COMMANDS.map(
  (command) => command.id as FeatureCommandId,
);

export function isFeatureCommandId(id: string): id is FeatureCommandId {
  return FEATURE_COMMAND_IDS.includes(id as FeatureCommandId);
}

export function commandById(id: CommandId): CommandDescriptor {
  const command = COMMAND_CATALOGUE.find((entry) => entry.id === id);
  if (command === undefined) {
    throw new Error(`Unknown command: ${id}`);
  }
  return command;
}

/**
 * The server action-catalogue id a feature command dispatches against, or
 * null for Configuration, which is a local editor with no server action.
 */
export function featureActionId(id: FeatureCommandId): string | null {
  return id === 'feature.configuration' ? null : id.slice('feature.'.length);
}

/** The action-catalogue shape both the menu summary and the palette read. */
export interface FeatureActionLike {
  id: string;
  enabled: boolean;
  disabledReasons: ReadonlyArray<{ code: string; message: string }>;
}

export interface FeatureCommandState {
  enabled: boolean;
  /** Shown beside a disabled palette entry; absent while a snapshot loads. */
  reason?: string;
}

export const NO_ACTIVE_FEATURE_REASON = 'No feature is selected.';
export const UNAVAILABLE_ACTION_REASON = 'Action is not available.';

/**
 * The single enablement rule every surface reads, so the ⌘K palette and the
 * Feature menu can never disagree about a verb: no selection disables the
 * whole group with the no-active-feature reason, a selection whose snapshot
 * has not arrived yet disables without a reason (there is nothing true to
 * say yet), and otherwise the server's action catalogue decides and carries
 * its own first disabled reason.
 */
export function featureCommandState(
  id: FeatureCommandId,
  actions: readonly FeatureActionLike[] | null,
  options: { hasSelection: boolean },
): FeatureCommandState {
  if (!options.hasSelection) {
    return { enabled: false, reason: NO_ACTIVE_FEATURE_REASON };
  }
  if (actions === null) {
    return { enabled: false };
  }
  const actionId = featureActionId(id);
  if (actionId === null) {
    return { enabled: true };
  }
  const entry = actions.find((action) => action.id === actionId);
  if (entry?.enabled === true) {
    return { enabled: true };
  }
  return {
    enabled: false,
    reason: entry?.disabledReasons[0]?.message ?? UNAVAILABLE_ACTION_REASON,
  };
}

/** The compact enabled map the renderer pushes to the native menu. */
export function featureCommandEnablement(
  actions: readonly FeatureActionLike[] | null,
  options: { hasSelection: boolean },
): Record<FeatureCommandId, boolean> {
  const entries = FEATURE_COMMAND_IDS.map((id) => [
    id,
    featureCommandState(id, actions, options).enabled,
  ]);
  return Object.fromEntries(entries) as Record<FeatureCommandId, boolean>;
}

const MAC_MODIFIER_SYMBOLS: ReadonlyArray<[string, string]> = [
  ['CommandOrControl', '⌘'],
  ['CmdOrCtrl', '⌘'],
  ['Command', '⌘'],
  ['Control', '⌃'],
  ['Shift', '⇧'],
  ['Alt', '⌥'],
];

/**
 * macOS renders modifiers in a fixed order regardless of how the accelerator
 * was written — ⌃⌥⇧⌘ — so ⌘⌃S displays as "⌃ ⌘ S", the way the native menu and
 * System Settings show it. Anything not in this list is the key itself and
 * sorts last.
 */
const MAC_MODIFIER_ORDER = ['⌃', '⌥', '⇧', '⌘'];

export function displayAccelerator(accelerator: string, platform = navigator.platform): string {
  const isMac = platform.toLowerCase().includes('mac');
  if (!isMac) {
    return accelerator.replace('CommandOrControl', 'Ctrl').replaceAll('+', ' ');
  }
  const rank = (token: string): number => {
    const index = MAC_MODIFIER_ORDER.indexOf(token);
    return index === -1 ? MAC_MODIFIER_ORDER.length : index;
  };
  return accelerator
    .split('+')
    .map((token) => MAC_MODIFIER_SYMBOLS.find(([name]) => name === token)?.[1] ?? token)
    .sort((left, right) => rank(left) - rank(right))
    .join(' ');
}
