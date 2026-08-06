import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import {
  COMMAND_CATALOGUE,
  commandById,
  displayAccelerator,
  type CommandDescriptor,
  type CommandGroup,
  type CommandId,
} from '../../../shared/commands';
import type {
  AppRouteEvent,
  FeatureOperationalAction,
  FeatureSnapshot,
  RoutedRequest,
} from '../../../shared/ipc';

interface PaletteEntry {
  id: CommandId;
  label: string;
  group: CommandGroup;
  accelerator?: string;
  disabled?: boolean;
  disabledReason?: string;
  run(): Promise<void> | void;
}

type PaletteFeatureAction = Extract<
  FeatureOperationalAction,
  'start' | 'pause-stop' | 'resume' | 'retry' | 'restart'
>;
const FEATURE_ACTIONS = new Set<PaletteFeatureAction>([
  'start',
  'pause-stop',
  'resume',
  'retry',
  'restart',
]);

const GROUP_LABELS: Record<CommandGroup, string> = {
  window: 'Window',
  navigation: 'Navigation',
  attention: 'Attention',
  assistant: 'Assistant',
  feature: 'Feature',
  bulk: 'Bulk',
};

export function CommandPalette({
  ready,
  routeRequest,
  onRoute,
}: {
  ready: boolean;
  routeRequest: RoutedRequest | null;
  onRoute(event: AppRouteEvent): void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState(false);
  const [activeFeatureId, setActiveFeatureId] = useState<string | null>(null);
  const [activeFeature, setActiveFeature] = useState<FeatureSnapshot | null>(null);
  const [activeIndex, setActiveIndex] = useState(0);
  const dialogRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);

  const close = useCallback(() => {
    setOpen(false);
    setQuery('');
    requestAnimationFrame(() => returnFocus.current?.focus());
  }, []);

  const openPalette = useCallback(() => {
    returnFocus.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setOpen(true);
  }, []);

  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent): void => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        if (isEditingShortcutTarget(event.target)) {
          return;
        }
        event.preventDefault();
        openPalette();
      }
      if (event.key === 'Escape' && open) {
        event.preventDefault();
        close();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [close, open, openPalette]);

  useEffect(() => {
    if (routeRequest?.event.target === 'palette') {
      openPalette();
    }
  }, [openPalette, routeRequest]);

  useEffect(() => {
    if (!open) return;
    requestAnimationFrame(() => inputRef.current?.focus());
    let alive = true;
    const selectedFeatureId = selectedFeatureIdFromDom();
    setActiveFeatureId(selectedFeatureId);
    setActiveFeature(null);
    window.agentico
      .getSettings()
      .then((settings) => {
        const activeId = selectedFeatureId ?? settings.shell.activeFeatureId;
        if (alive) setActiveFeatureId(activeId);
        if (activeId === null) return null;
        return window.agentico.getFeature(activeId);
      })
      .then((feature) => {
        if (alive) setActiveFeature(feature);
      })
      .catch(() => {
        if (alive) setActiveFeature(null);
      });
    return () => {
      alive = false;
    };
  }, [open]);

  const entries = useMemo(
    () => buildEntries({ ready, activeFeatureId, activeFeature, onRoute, close }),
    [activeFeatureId, activeFeature, close, onRoute, ready],
  );

  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    if (needle === '') return entries;
    return entries.filter((entry) =>
      `${entry.label} ${GROUP_LABELS[entry.group]} ${entry.disabledReason ?? ''}`
        .toLocaleLowerCase()
        .includes(needle),
    );
  }, [entries, query]);

  useEffect(() => {
    setActiveIndex(0);
  }, [query, open]);

  if (!open) return null;

  const selectable = filtered.filter((entry) => entry.disabled !== true);
  const activeEntry = selectable[Math.min(activeIndex, Math.max(selectable.length - 1, 0))];

  const runEntry = async (entry: PaletteEntry): Promise<void> => {
    if (entry.disabled === true || busy) return;
    setBusy(true);
    try {
      await entry.run();
    } finally {
      setBusy(false);
    }
  };

  const onInputKeyDown = (event: KeyboardEvent<HTMLInputElement>): void => {
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setActiveIndex((current) =>
        selectable.length === 0 ? 0 : (current + 1) % selectable.length,
      );
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setActiveIndex((current) =>
        selectable.length === 0 ? 0 : (current - 1 + selectable.length) % selectable.length,
      );
    } else if (event.key === 'Enter' && activeEntry !== undefined) {
      event.preventDefault();
      void runEntry(activeEntry);
    }
  };

  return (
    <div className="command-palette__backdrop" role="presentation" onMouseDown={close}>
      <div
        ref={dialogRef}
        className="command-palette"
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="command-palette__input"
          aria-label="Search commands"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={onInputKeyDown}
          placeholder="Search commands"
        />
        <div className="command-palette__list" role="listbox" aria-label="Commands">
          {filtered.length === 0 ? (
            <p className="command-palette__empty">No commands match.</p>
          ) : (
            grouped(filtered).map(([group, groupEntries]) => (
              <section
                key={group}
                className="command-palette__group"
                aria-label={GROUP_LABELS[group]}
              >
                <h2>{GROUP_LABELS[group]}</h2>
                {groupEntries.map((entry) => {
                  const selected = activeEntry?.id === entry.id;
                  return (
                    <button
                      key={entry.id}
                      type="button"
                      role="option"
                      aria-selected={selected}
                      className="command-palette__item"
                      disabled={entry.disabled === true || busy}
                      data-selected={selected}
                      onMouseEnter={() => {
                        const index = selectable.findIndex(
                          (candidate) => candidate.id === entry.id,
                        );
                        if (index >= 0) setActiveIndex(index);
                      }}
                      onClick={() => void runEntry(entry)}
                    >
                      <span className="command-palette__item-label">{entry.label}</span>
                      {entry.disabledReason !== undefined ? (
                        <span className="command-palette__item-reason">{entry.disabledReason}</span>
                      ) : null}
                      {entry.accelerator !== undefined ? (
                        <kbd>{displayAccelerator(entry.accelerator)}</kbd>
                      ) : null}
                    </button>
                  );
                })}
              </section>
            ))
          )}
        </div>
      </div>
    </div>
  );
}

function buildEntries({
  ready,
  activeFeature,
  activeFeatureId,
  onRoute,
  close,
}: {
  ready: boolean;
  activeFeature: FeatureSnapshot | null;
  activeFeatureId: string | null;
  onRoute(event: AppRouteEvent): void;
  close(): void;
}): PaletteEntry[] {
  const globalEntries = COMMAND_CATALOGUE.filter((command) => !command.id.startsWith('feature.'))
    .filter((command) => command.id !== 'global.palette')
    .filter((command) => command.target !== undefined)
    .map((command) => globalEntry(command, ready, onRoute, close));

  const featureEntries = COMMAND_CATALOGUE.filter((command) =>
    command.id.startsWith('feature.'),
  ).map((command) => featureEntry(command, activeFeatureId, activeFeature, close));

  return [globalPaletteEntry(close), ...globalEntries, ...featureEntries];
}

function globalPaletteEntry(close: () => void): PaletteEntry {
  const command = commandById('global.palette');
  return {
    ...command,
    run: close,
  };
}

function globalEntry(
  command: CommandDescriptor,
  ready: boolean,
  onRoute: (event: AppRouteEvent) => void,
  close: () => void,
): PaletteEntry {
  const target = command.target;
  if (target === undefined) {
    throw new Error(`Global command ${command.id} has no route target.`);
  }
  const needsRuntime = target !== 'settings';
  const disabled = needsRuntime && !ready;
  return {
    ...command,
    disabled,
    disabledReason: disabled ? 'Runtime is not ready.' : undefined,
    run: () => {
      onRoute({ target });
      close();
    },
  };
}

function featureEntry(
  command: CommandDescriptor,
  activeFeatureId: string | null,
  activeFeature: FeatureSnapshot | null,
  close: () => void,
): PaletteEntry {
  const action = command.id.replace('feature.', '') as FeatureOperationalAction;
  const catalogueAction = activeFeature?.actions.find((entry) => entry.id === action);
  const disabledReason =
    activeFeature === null && activeFeatureId === null
      ? 'No active feature tab.'
      : (catalogueAction?.disabledReasons[0]?.message ??
        (catalogueAction?.enabled === true ? undefined : 'Action is not available.'));
  const disabled =
    activeFeature === null ? activeFeatureId === null : catalogueAction?.enabled !== true;
  return {
    ...command,
    disabled,
    disabledReason: activeFeature === null && activeFeatureId !== null ? undefined : disabledReason,
    run: async () => {
      const featureId = activeFeature?.id ?? activeFeatureId;
      if (featureId === null || !isPaletteFeatureAction(action)) return;
      await window.agentico.dispatchFeatureAction({ featureId, action });
      close();
    },
  };
}

function isPaletteFeatureAction(action: FeatureOperationalAction): action is PaletteFeatureAction {
  return FEATURE_ACTIONS.has(action as PaletteFeatureAction);
}

function grouped(entries: PaletteEntry[]): Array<[CommandGroup, PaletteEntry[]]> {
  const groups: Array<[CommandGroup, PaletteEntry[]]> = [];
  for (const entry of entries) {
    const existing = groups.find(([group]) => group === entry.group);
    if (existing === undefined) {
      groups.push([entry.group, [entry]]);
    } else {
      existing[1].push(entry);
    }
  }
  return groups;
}

/**
 * True when a global shortcut's keydown target is a place the user is
 * actively typing (a native text input, a contenteditable region, Monaco, or
 * any `role="textbox"`) — every renderer-level shortcut that isn't already
 * scoped to a specific focused control (⌘K here; ⌘2-9 and ⌘⌃S in
 * WorkspaceShell) checks this before acting, so typing a digit or letter
 * never gets hijacked mid-sentence.
 */
export function isEditingShortcutTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  if (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target.isContentEditable
  ) {
    return true;
  }
  return target.closest('.monaco-editor, [role="textbox"]') !== null;
}

/** Reads the currently selected sidebar row directly from its DOM markup —
 * the fastest read of "what's on screen right now", ahead of any settings
 * round trip. The Overview row is not a feature id and reports as none
 * selected. */
function selectedFeatureIdFromDom(): string | null {
  const selected = document.querySelector(
    '[role="option"][aria-selected="true"][id^="sidebar-row-"]',
  );
  if (!(selected instanceof HTMLElement)) {
    return null;
  }
  const id = selected.id.slice('sidebar-row-'.length);
  return id === '' ? null : id;
}
