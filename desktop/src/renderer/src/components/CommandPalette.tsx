import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import {
  COMMAND_CATALOGUE,
  FEATURE_COMMANDS,
  NO_ACTIVE_FEATURE_REASON,
  commandById,
  displayAccelerator,
  featureCommandState,
  type CommandDescriptor,
  type CommandGroup,
  type CommandId,
  type FeatureActionLike,
  type FeatureCommandId,
} from '../../../shared/commands';
import { activeFeatureCommandTarget, runFeatureCommand } from '../features/featureCommands';
import { displayPhaseLabel, displayStatusLabel } from '../features/featureView';
import { DEFAULT_RUNTIME_ID } from '../../../shared/ipc';
import type { AppRouteEvent, FeatureSummaryView, RoutedRequest } from '../../../shared/ipc';
import { useConnectionState } from '../hooks';

/**
 * The palette lists two kinds of row: the command catalogue, grouped by its own
 * groups, and the live feature list under a group of its own — so ⌘K is one
 * search box for "what can I do" and "where do I go".
 */
type PaletteGroup = CommandGroup | 'feature-nav';

interface PaletteEntry {
  id: CommandId | `feature-nav.${string}`;
  label: string;
  group: PaletteGroup;
  /** Secondary matchable text: a feature's status and phase. */
  detail?: string;
  accelerator?: string;
  disabled?: boolean;
  disabledReason?: string;
  run(): Promise<void> | void;
}

const GROUP_LABELS: Record<PaletteGroup, string> = {
  'feature-nav': 'Features',
  window: 'Window',
  file: 'File',
  navigation: 'Navigation',
  view: 'View',
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
  const connection = useConnectionState();
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState(false);
  const [activeFeatureId, setActiveFeatureId] = useState<string | null>(null);
  // The selected feature's live action catalogue, or null while it is unknown.
  const [activeActions, setActiveActions] = useState<readonly FeatureActionLike[] | null>(null);
  const [features, setFeatures] = useState<readonly FeatureSummaryView[]>([]);
  const [activeIndex, setActiveIndex] = useState(0);
  const dialogRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const selectedRef = useRef<HTMLButtonElement | null>(null);
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
    // The mounted cockpit already holds the authoritative catalogue for what is
    // selected, and holds it synchronously — so the Feature group is correct on
    // the very frame the palette opens instead of after a settings-plus-snapshot
    // round trip. The async read below still runs, both to resolve a selection
    // the DOM cannot see and to cover a selection with no cockpit mounted.
    const mounted = activeFeatureCommandTarget();
    const seededId = selectedFeatureId ?? mounted?.featureId ?? null;
    setActiveFeatureId(seededId);
    setActiveActions(mounted !== null && mounted.featureId === seededId ? mounted.actions : null);
    window.agentico
      .getSettings()
      .then((settings) => {
        // The selection is scoped to the connected server's identity key.
        const scoped = settings.shell.featureByServer[connection.serverKey ?? DEFAULT_RUNTIME_ID];
        const activeId = selectedFeatureId ?? scoped ?? null;
        if (alive) setActiveFeatureId(activeId);
        if (activeId === null) return null;
        return window.agentico.getFeature(activeId);
      })
      .then((feature) => {
        if (alive) setActiveActions(feature === null ? null : feature.actions);
      })
      .catch(() => {
        if (alive) setActiveActions(null);
      });
    // Navigating to a feature by name needs the whole list, not just the
    // selection; the runtime owns it, so it is fetched per open.
    window.agentico
      .listFeatures()
      .then((rows) => {
        if (alive) setFeatures(rows);
      })
      .catch(() => {
        if (alive) setFeatures([]);
      });
    return () => {
      alive = false;
    };
  }, [open, connection.serverKey]);

  const entries = useMemo(
    () => buildEntries({ ready, activeFeatureId, activeActions, onRoute, close }),
    [activeFeatureId, activeActions, close, onRoute, ready],
  );

  const featureEntries = useMemo(
    () => features.map((feature) => featureNavEntry(feature, onRoute, close)),
    [close, features, onRoute],
  );

  const filtered = useMemo(() => {
    const needle = query.trim().toLocaleLowerCase();
    // Feature rows are a search result, not a browsable list: with no query the
    // palette stays the command catalogue it has always been, and the sidebar
    // remains the place to browse features.
    if (needle === '') return entries;
    return [
      ...rankedFeatureMatches(featureEntries, needle),
      ...entries.filter((entry) => matches(entry, needle)),
    ];
  }, [entries, featureEntries, query]);

  useEffect(() => {
    setActiveIndex(0);
  }, [query, open]);

  const selectable = useMemo(() => filtered.filter((entry) => entry.disabled !== true), [filtered]);
  const activeEntry = selectable[Math.min(activeIndex, Math.max(selectable.length - 1, 0))];

  // The highlighted row is the only keyboard-focus signifier the palette has —
  // focus itself stays in the search input — so it has to follow the arrow keys
  // into view. The list overflows well before the catalogue ends.
  useEffect(() => {
    selectedRef.current?.scrollIntoView?.({ block: 'nearest' });
  }, [activeEntry?.id]);

  if (!open) return null;

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
          aria-label="Search features and commands"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={onInputKeyDown}
          placeholder="Search features and commands"
        />
        <div className="command-palette__list" role="listbox" aria-label="Commands">
          {filtered.length === 0 ? (
            <p className="command-palette__empty">No features or commands match.</p>
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
                      ref={selected ? selectedRef : undefined}
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
                      {entry.detail !== undefined ? (
                        <span className="command-palette__item-reason">{entry.detail}</span>
                      ) : null}
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

function matches(entry: PaletteEntry, needle: string): boolean {
  return `${entry.label} ${GROUP_LABELS[entry.group]} ${entry.detail ?? ''} ${entry.disabledReason ?? ''}`
    .toLocaleLowerCase()
    .includes(needle);
}

/**
 * Feature matches on name, with the rows whose name starts with the needle
 * ahead of the rest — typing the first letters of a feature puts it under the
 * cursor, so Enter opens it without an arrow key.
 */
function rankedFeatureMatches(entries: PaletteEntry[], needle: string): PaletteEntry[] {
  const scored = entries.flatMap((entry) => {
    const name = entry.label.toLocaleLowerCase();
    if (name.startsWith(needle)) return [{ entry, rank: 0 }];
    if (name.includes(needle)) return [{ entry, rank: 1 }];
    return matches(entry, needle) ? [{ entry, rank: 2 }] : [];
  });
  return scored.sort((a, b) => a.rank - b.rank).map((match) => match.entry);
}

/** A row that selects the feature in the sidebar, the same as clicking it. */
function featureNavEntry(
  feature: FeatureSummaryView,
  onRoute: (event: AppRouteEvent) => void,
  close: () => void,
): PaletteEntry {
  const phase = displayPhaseLabel(feature.currentPhase);
  return {
    id: `feature-nav.${feature.id}`,
    label: feature.name,
    group: 'feature-nav',
    detail:
      phase === ''
        ? displayStatusLabel(feature.status)
        : `${displayStatusLabel(feature.status)} · ${phase}`,
    run: () => {
      onRoute({ target: 'select-feature', featureId: feature.id });
      close();
    },
  };
}

function buildEntries({
  ready,
  activeActions,
  activeFeatureId,
  onRoute,
  close,
}: {
  ready: boolean;
  activeActions: readonly FeatureActionLike[] | null;
  activeFeatureId: string | null;
  onRoute(event: AppRouteEvent): void;
  close(): void;
}): PaletteEntry[] {
  const globalEntries = COMMAND_CATALOGUE.filter(
    (command) => command.group !== 'feature' && command.paletteVisible === true,
  ).map((command) => globalEntry(command, { ready, activeFeatureId }, onRoute, close));

  const featureEntries = FEATURE_COMMANDS.map((command) =>
    featureEntry(command, activeFeatureId, activeActions, close),
  );

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
  context: { ready: boolean; activeFeatureId: string | null },
  onRoute: (event: AppRouteEvent) => void,
  close: () => void,
): PaletteEntry {
  const target = command.target;
  if (target === undefined) {
    throw new Error(`Global command ${command.id} has no route target.`);
  }
  // Settings is its own window and stays reachable while the runtime is down;
  // the inspector additionally needs a feature to inspect.
  const needsRuntime = target !== 'settings';
  const needsSelection = command.id === 'global.toggle-inspector';
  const disabled =
    (needsRuntime && !context.ready) || (needsSelection && context.activeFeatureId === null);
  return {
    ...command,
    disabled,
    disabledReason: !disabled
      ? undefined
      : needsRuntime && !context.ready
        ? 'Runtime is not ready.'
        : NO_ACTIVE_FEATURE_REASON,
    run: () => {
      onRoute({ target });
      close();
    },
  };
}

/**
 * A feature entry: enablement straight from the shared catalogue rule (so the
 * palette and the Feature menu can never disagree), dispatch straight through
 * the shared funnel (so Stop, Restart, and Delete get the cockpit's
 * confirmations rather than the raw action they used to fire).
 */
function featureEntry(
  command: CommandDescriptor,
  activeFeatureId: string | null,
  activeActions: readonly FeatureActionLike[] | null,
  close: () => void,
): PaletteEntry {
  const id = command.id as FeatureCommandId;
  const state = featureCommandState(id, activeActions, {
    hasSelection: activeFeatureId !== null,
  });
  return {
    ...command,
    disabled: !state.enabled,
    ...(state.reason === undefined ? {} : { disabledReason: state.reason }),
    run: () => {
      if (activeFeatureId === null) return;
      runFeatureCommand(id, { featureId: activeFeatureId });
      close();
    },
  };
}

function grouped(entries: PaletteEntry[]): Array<[PaletteGroup, PaletteEntry[]]> {
  const groups: Array<[PaletteGroup, PaletteEntry[]]> = [];
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
