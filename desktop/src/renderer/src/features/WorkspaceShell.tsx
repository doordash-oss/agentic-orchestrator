/**
 * The readiness-gated main surface: a Home tab (authoritative feature list
 * plus one persistent tab per open feature. Feature creation is a focused,
 * secondary flow reached from Home.
 * Local settings store ONLY tab identity and presentation (feature id,
 * title hint, order, active tab); every feature itself is always reloaded
 * from the server, so existing state survives app restarts without any
 * local domain cache.
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type Dispatch,
  type KeyboardEvent,
  type RefObject,
  type SetStateAction,
} from 'react';
import type { AttentionItem, FeatureSnapshot, RoutedRequest, TabsPrefs } from '../../../shared/ipc';
import { defaultTabsPrefs } from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { CreateFeatureForm } from './CreateFeatureForm';
import { FeatureCockpit } from './FeatureCockpit';
import { SettingsPanel } from './SettingsPanel';
import { emptyAttentionDrafts, type AttentionDrafts } from './AttentionInbox';
import {
  dashboardGroupId,
  dashboardState,
  displayStatusLabel,
  formatElapsed,
  groupDashboardFeatures,
  isRunAtRest,
  orderDashboardFeatures,
  railStageLabel,
  spineActiveIndex,
  spineStages,
  type SpineStage,
} from './featureView';
import { BulkPreviewPanel } from './BulkPreviewPanel';
import { RecoveryWorkspace } from './RecoveryWorkspace';
import { usePrefersReducedMotion } from '../hooks';

const SETTINGS_TAB_ID = '__settings__';

type ListState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; features: FeatureSnapshot[] };

type CreationDestination =
  { kind: 'home' } | { kind: 'settings' } | { kind: 'feature'; featureId: string };

export function WorkspaceShell({
  attentionItems = [],
  refreshAttention = async () => [],
  attentionDrafts,
  setAttentionDrafts,
  attentionJump = null,
  onAttentionJumpHandled = () => {},
  routeRequest = null,
}: {
  attentionItems?: AttentionItem[];
  refreshAttention?: () => Promise<AttentionItem[]>;
  attentionDrafts?: AttentionDrafts;
  setAttentionDrafts?: Dispatch<SetStateAction<AttentionDrafts>>;
  attentionJump?: {
    requestId: number;
    featureId: string;
    attentionId?: string;
  } | null;
  onAttentionJumpHandled?: () => void;
  routeRequest?: RoutedRequest | null;
}) {
  // null while the local tab prefs are being restored.
  const [tabs, setTabs] = useState<TabsPrefs | null>(null);
  const [list, setList] = useState<ListState>({ phase: 'loading' });
  const [localAttentionDrafts, setLocalAttentionDrafts] = useState(emptyAttentionDrafts);
  const activeAttentionDrafts = attentionDrafts ?? localAttentionDrafts;
  const updateAttentionDrafts = setAttentionDrafts ?? setLocalAttentionDrafts;
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const newFeatureButtonRef = useRef<HTMLButtonElement | null>(null);
  const handledAttentionJump = useRef<number | null>(null);
  const [attentionPreviewRequest, setAttentionPreviewRequest] = useState<{
    requestId: number;
    featureId: string;
    attentionId?: string;
  } | null>(null);
  const handledRouteRequest = useRef<number | null>(null);
  const listRequestRef = useRef(0);
  const homeActiveRef = useRef(false);
  const [view, setView] = useState<'home' | 'create'>('home');
  const [bulkPreviewRequest, setBulkPreviewRequest] = useState<number | null>(null);

  // Restore ONLY identity/presentation state locally; corrupt or missing
  // settings fall back to an empty tab strip.
  useEffect(() => {
    let alive = true;
    window.agentico
      .getSettings()
      .then((settings) => {
        if (alive) {
          setTabs(settings.tabs);
        }
      })
      .catch(() => {
        if (alive) {
          setTabs(defaultTabsPrefs());
        }
      });
    return () => {
      alive = false;
    };
  }, []);

  const loadList = useCallback(() => {
    const request = ++listRequestRef.current;
    window.agentico
      .listFeatures()
      .then((features) =>
        Promise.all(features.map((feature) => window.agentico.getFeature(feature.id))),
      )
      .then((features) => {
        if (request === listRequestRef.current) {
          setList({ phase: 'loaded', features: orderDashboardFeatures(features) });
        }
      })
      .catch((err: unknown) => {
        if (request === listRequestRef.current) {
          setList({ phase: 'error', error: parseIpcError(err) });
        }
      });
  }, []);

  // The Home feature list follows the authoritative server state: fetch on
  // mount and refetch on any feature-scoped invalidation or full resync.
  useEffect(() => {
    loadList();
    return window.agentico.onAppEvent((event) => {
      if (event.type !== 'invalidated') {
        return;
      }
      if (
        event.kind === 'resync' ||
        event.kind.startsWith('feature') ||
        event.kind.startsWith('lifecycle') ||
        event.kind.startsWith('relationship') ||
        event.kind.startsWith('config')
      ) {
        loadList();
      }
    });
  }, [loadList]);

  // Out-of-band changes (a delete from another window, the CLI, or a missed
  // invalidation) can leave the queue stale. Refetch when the app regains focus
  // while Home is showing, so returning to the window always shows server truth.
  useEffect(() => {
    const refreshHome = () => {
      if (document.visibilityState === 'visible' && homeActiveRef.current) {
        loadList();
      }
    };
    window.addEventListener('focus', refreshHome);
    document.addEventListener('visibilitychange', refreshHome);
    return () => {
      window.removeEventListener('focus', refreshHome);
      document.removeEventListener('visibilitychange', refreshHome);
    };
  }, [loadList]);

  /** Persist failures never block the UI — tabs are presentation only. */
  const persist = useCallback((next: TabsPrefs) => {
    setTabs(next);
    window.agentico.updateSettings({ tabs: next }).catch(() => {
      // The server-side feature state is unaffected; ignore.
    });
  }, []);

  const openFeature = useCallback(
    (featureId: string, titleHint: string) => {
      const base = tabs ?? defaultTabsPrefs();
      const exists = base.open.some((tab) => tab.featureId === featureId);
      persist({
        open: exists ? base.open : [...base.open, { featureId, titleHint }],
        activeFeatureId: featureId,
      });
    },
    [persist, tabs],
  );

  const closeFeature = useCallback(
    (featureId: string) => {
      const base = tabs ?? defaultTabsPrefs();
      persist({
        open: base.open.filter((tab) => tab.featureId !== featureId),
        activeFeatureId: base.activeFeatureId === featureId ? null : base.activeFeatureId,
      });
    },
    [persist, tabs],
  );

  const handleFeatureDeleted = useCallback(
    (featureId: string) => {
      setList((current) =>
        current.phase === 'loaded'
          ? {
              ...current,
              features: current.features.filter((feature) => feature.id !== featureId),
            }
          : current,
      );
      closeFeature(featureId);
      loadList();
    },
    [closeFeature, loadList],
  );

  const renameTab = useCallback(
    (featureId: string, titleHint: string) => {
      const base = tabs ?? defaultTabsPrefs();
      const tab = base.open.find((entry) => entry.featureId === featureId);
      if (tab === undefined || tab.titleHint === titleHint) {
        return;
      }
      persist({
        open: base.open.map((entry) =>
          entry.featureId === featureId ? { ...entry, titleHint } : entry,
        ),
        activeFeatureId: base.activeFeatureId,
      });
    },
    [persist, tabs],
  );

  const attentionByFeature = useMemo(() => {
    const counts = new Map<string, number>();
    for (const item of attentionItems) {
      if (item.kind === 'recovery') continue;
      if (item.featureId === undefined) continue;
      counts.set(item.featureId, (counts.get(item.featureId) ?? 0) + 1);
    }
    return counts;
  }, [attentionItems]);
  const featureLabel = useCallback(
    (featureId: string | undefined): string => {
      if (featureId === undefined) return 'Runtime';
      const listed =
        list.phase === 'loaded'
          ? list.features.find((feature) => feature.id === featureId)?.name
          : undefined;
      if (listed !== undefined) return listed;
      const tab = tabs?.open.find((entry) => entry.featureId === featureId);
      if (tab !== undefined && tab.titleHint !== '') return tab.titleHint;
      return 'Untitled feature';
    },
    [list, tabs],
  );

  useEffect(() => {
    if (tabs === null) return;
    const index =
      tabs.activeFeatureId === null
        ? 0
        : tabs.activeFeatureId === SETTINGS_TAB_ID
          ? 1
          : tabs.open.findIndex((tab) => tab.featureId === tabs.activeFeatureId) + 2;
    const tab = tabRefs.current[index];
    if (tab !== null && tab !== undefined && typeof tab.scrollIntoView === 'function') {
      tab.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    }
  }, [tabs]);

  useEffect(() => {
    if (attentionJump === null) {
      handledAttentionJump.current = null;
      return;
    }
    if (tabs === null || handledAttentionJump.current === attentionJump.requestId) return;
    handledAttentionJump.current = attentionJump.requestId;
    if (attentionJump.featureId === '__recovery__') {
      persist({ ...tabs, activeFeatureId: null });
      setView('home');
    } else {
      openFeature(attentionJump.featureId, featureLabel(attentionJump.featureId));
      setAttentionPreviewRequest({
        requestId: attentionJump.requestId,
        featureId: attentionJump.featureId,
        ...(attentionJump.attentionId === undefined
          ? {}
          : { attentionId: attentionJump.attentionId }),
      });
    }
    onAttentionJumpHandled();
  }, [attentionJump, featureLabel, onAttentionJumpHandled, openFeature, persist, tabs]);

  const completeCreationExit = useCallback((destination: CreationDestination) => {
    setTabs((current) => {
      const base = current ?? defaultTabsPrefs();
      const activeFeatureId =
        destination.kind === 'home'
          ? null
          : destination.kind === 'settings'
            ? SETTINGS_TAB_ID
            : destination.featureId;
      const next = {
        ...base,
        activeFeatureId,
      };
      window.agentico.updateSettings({ tabs: next }).catch(() => {});
      return next;
    });
    setView('home');
    if (destination.kind === 'home') {
      requestAnimationFrame(() => newFeatureButtonRef.current?.focus());
    }
  }, []);
  const creationGuard = useCreationGuard(completeCreationExit);

  const showHome = useCallback(() => {
    if (tabs === null) return;
    persist({ ...tabs, activeFeatureId: null });
    setView('home');
    loadList();
  }, [loadList, persist, tabs]);
  const closeAttentionPreview = useCallback(() => setAttentionPreviewRequest(null), []);

  useEffect(() => {
    if (tabs === null || routeRequest === null) return;
    if (handledRouteRequest.current === routeRequest.id) return;
    handledRouteRequest.current = routeRequest.id;
    if (routeRequest.event.target === 'home') {
      if (view === 'create') {
        creationGuard.leave({ kind: 'home' });
      } else {
        showHome();
      }
    } else if (routeRequest.event.target === 'settings') {
      if (view === 'create') {
        creationGuard.leave({ kind: 'settings' });
      } else {
        persist({ ...tabs, activeFeatureId: SETTINGS_TAB_ID });
      }
    } else if (routeRequest.event.target === 'bulk') {
      setBulkPreviewRequest(routeRequest.id);
      if (view === 'create') {
        creationGuard.leave({ kind: 'home' });
      } else {
        showHome();
      }
    }
  }, [creationGuard, persist, routeRequest, showHome, tabs, view]);

  if (tabs === null) {
    return (
      <section className="shell-card workspace" aria-label="Workspace">
        <p role="status" aria-live="polite" className="cockpit__loading">
          Restoring workspace…
        </p>
      </section>
    );
  }

  const activeId = tabs.activeFeatureId;
  const isSettingsActive = activeId === SETTINGS_TAB_ID;
  const activeIsOpen = activeId !== null && tabs.open.some((tab) => tab.featureId === activeId);
  const active = isSettingsActive ? SETTINGS_TAB_ID : activeIsOpen ? activeId : null;
  // Read by the focus/visibility refresh so it only refetches when Home is shown.
  homeActiveRef.current = active === null && view !== 'create';
  const tabCount = tabs.open.length + 2;

  const focusTab = (index: number): void => {
    const clamped = (index + tabCount) % tabCount;
    tabRefs.current[clamped]?.focus();
  };

  const onTabKeyDown = (event: KeyboardEvent, index: number): void => {
    if (event.key === 'ArrowRight') {
      event.preventDefault();
      focusTab(index + 1);
    } else if (event.key === 'ArrowLeft') {
      event.preventDefault();
      focusTab(index - 1);
    }
  };

  const activateFeatureTab = (featureId: string) => {
    persist({ ...tabs, activeFeatureId: featureId });
    setView('home');
  };

  const navigateHome = () => {
    if (view === 'create') {
      creationGuard.leave({ kind: 'home' });
      return;
    }
    showHome();
  };

  const navigateSettings = () => {
    if (view === 'create') {
      creationGuard.leave({ kind: 'settings' });
      return;
    }
    persist({ ...tabs, activeFeatureId: SETTINGS_TAB_ID });
  };

  const navigateFeature = (featureId: string) => {
    if (view === 'create') {
      creationGuard.leave({ kind: 'feature', featureId });
      return;
    }
    activateFeatureTab(featureId);
  };

  return (
    <section className="workspace" aria-label="Workspace">
      <div className="tab-strip__rail">
        <div className="tab-strip" role="tablist" aria-label="Workspace tabs">
          <button
            ref={(node) => {
              tabRefs.current[0] = node;
            }}
            type="button"
            role="tab"
            id="tab-home"
            aria-selected={active === null}
            aria-controls="panel-home"
            className="tab-strip__tab"
            tabIndex={active === null ? 0 : -1}
            onClick={navigateHome}
            onKeyDown={(event) => onTabKeyDown(event, 0)}
          >
            Home
          </button>
          <button
            ref={(node) => {
              tabRefs.current[1] = node;
            }}
            type="button"
            role="tab"
            id="tab-settings"
            aria-selected={isSettingsActive}
            aria-controls="panel-settings"
            className="tab-strip__tab"
            tabIndex={isSettingsActive ? 0 : -1}
            onClick={navigateSettings}
            onKeyDown={(event) => onTabKeyDown(event, 1)}
          >
            Settings
          </button>
          {tabs.open.map((tab, index) => (
            <span key={tab.featureId} className="tab-strip__entry">
              <button
                ref={(node) => {
                  tabRefs.current[index + 2] = node;
                }}
                type="button"
                role="tab"
                id={`tab-${tab.featureId}`}
                aria-selected={active === tab.featureId}
                aria-controls={`panel-${tab.featureId}`}
                className="tab-strip__tab"
                tabIndex={active === tab.featureId ? 0 : -1}
                onClick={() => {
                  navigateFeature(tab.featureId);
                }}
                onKeyDown={(event) => onTabKeyDown(event, index + 2)}
              >
                <span>{tab.titleHint === '' ? featureLabel(tab.featureId) : tab.titleHint}</span>
                <AttentionBadge
                  count={attentionByFeature.get(tab.featureId) ?? 0}
                  label={`Blocking input for ${featureLabel(tab.featureId)}`}
                />
              </button>
              <button
                type="button"
                className="tab-strip__close"
                aria-label={`Close ${tab.titleHint === '' ? featureLabel(tab.featureId) : tab.titleHint} tab`}
                onClick={() => closeFeature(tab.featureId)}
              >
                <span aria-hidden="true">✕</span>
              </button>
            </span>
          ))}
        </div>
        <TabOverflowMenu
          tabs={tabs}
          attentionByFeature={attentionByFeature}
          featureLabel={featureLabel}
          onNavigateHome={navigateHome}
          onNavigateSettings={navigateSettings}
          onNavigateFeature={navigateFeature}
        />
      </div>

      {active === null ? (
        <div
          id="panel-home"
          role="tabpanel"
          aria-labelledby="tab-home"
          className={`tab-panel${view === 'create' ? ' tab-panel--create' : ''}`}
        >
          {view === 'create' ? (
            <CreationFlow
              guard={creationGuard}
              onCreated={({ featureId, name }) => {
                creationGuard.reset();
                openFeature(featureId, name);
                loadList();
                setView('home');
              }}
            />
          ) : (
            <>
              <header className="home-surface__header">
                <div>
                  <p className="home-surface__eyebrow">Agentico · Supervised runs</p>
                  <h1>Feature queue</h1>
                  <HomeReadout state={list} />
                </div>
                {list.phase !== 'loaded' || list.features.length > 0 ? (
                  <button
                    ref={newFeatureButtonRef}
                    type="button"
                    className="create-form__submit"
                    onClick={() => setView('create')}
                  >
                    New feature
                  </button>
                ) : null}
              </header>
              <FeatureList
                state={list}
                openTabIds={tabs.open.map((tab) => tab.featureId)}
                attentionByFeature={attentionByFeature}
                onOpen={openFeature}
                onRetry={loadList}
                onCreate={() => setView('create')}
                createButtonRef={newFeatureButtonRef}
              />
              <RecoveryWorkspace
                onNavigateToFeature={(featureId) => openFeature(featureId, featureLabel(featureId))}
              />
              <BulkPreviewPanel autoPreviewKey={bulkPreviewRequest} />
            </>
          )}
        </div>
      ) : active === SETTINGS_TAB_ID ? (
        <div
          id="panel-settings"
          role="tabpanel"
          aria-labelledby="tab-settings"
          className="tab-panel"
        >
          <SettingsPanel
            routeRequest={routeRequest?.event.target === 'settings' ? routeRequest : null}
          />
        </div>
      ) : (
        <div
          id={`panel-${active}`}
          role="tabpanel"
          aria-labelledby={`tab-${active}`}
          className="tab-panel tab-panel--cockpit"
        >
          <FeatureCockpit
            key={active}
            featureId={active}
            titleHint={tabs.open.find((tab) => tab.featureId === active)?.titleHint ?? active}
            onClose={() => closeFeature(active)}
            onDeleted={handleFeatureDeleted}
            onLoadedName={(name) => renameTab(active, name)}
            attentionItems={attentionItems.filter(
              (item) => item.kind !== 'recovery' && item.featureId === active,
            )}
            refreshAttention={refreshAttention}
            attentionDrafts={activeAttentionDrafts}
            setAttentionDrafts={updateAttentionDrafts}
            attentionPreviewRequest={
              attentionPreviewRequest?.featureId === active ? attentionPreviewRequest : null
            }
            onAttentionPreviewClose={closeAttentionPreview}
            selectedRunNumber={
              tabs.open.find((tab) => tab.featureId === active)?.selectedRunNumber ?? null
            }
            onSelectRun={(runNumber) => {
              const next = {
                ...tabs,
                open: tabs.open.map((tab) =>
                  tab.featureId === active ? { ...tab, selectedRunNumber: runNumber } : tab,
                ),
              };
              persist(next);
            }}
          />
        </div>
      )}
    </section>
  );
}

interface CreationGuard {
  leave(destination?: CreationDestination): void;
  reset(): void;
  onDirtyChange(dirty: boolean): void;
  discardOpen: boolean;
  keepEditing(): void;
  discard(): void;
}

function useCreationGuard(onExit: (destination: CreationDestination) => void): CreationGuard {
  const [dirty, setDirty] = useState(false);
  const [discardOpen, setDiscardOpen] = useState(false);
  const [pendingDestination, setPendingDestination] = useState<CreationDestination | null>(null);

  const reset = useCallback(() => {
    setDirty(false);
    setDiscardOpen(false);
    setPendingDestination(null);
  }, []);
  const leave = useCallback(
    (destination: CreationDestination = { kind: 'home' }) => {
      if (dirty) {
        setPendingDestination(destination);
        setDiscardOpen(true);
        return;
      }
      reset();
      onExit(destination);
    },
    [dirty, onExit, reset],
  );
  const discard = useCallback(() => {
    const destination = pendingDestination ?? { kind: 'home' };
    reset();
    onExit(destination);
  }, [onExit, pendingDestination, reset]);

  return {
    leave,
    reset,
    onDirtyChange: setDirty,
    discardOpen,
    keepEditing: () => setDiscardOpen(false),
    discard,
  };
}

function CreationFlow({
  guard,
  onCreated,
}: {
  guard: CreationGuard;
  onCreated(created: { featureId: string; name: string }): void;
}) {
  return (
    <section className="creation-flow" aria-label="New feature flow">
      <header className="creation-flow__header">
        <button type="button" className="setup-wizard__action" onClick={() => guard.leave()}>
          Back to Home
        </button>
        <p>Feature definition</p>
      </header>
      <CreateFeatureForm onDirtyChange={guard.onDirtyChange} onCreated={onCreated} />
      {guard.discardOpen ? (
        <div className="impact-dialog__backdrop">
          <div
            className="impact-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="Discard feature draft"
          >
            <h2>Discard this feature draft?</h2>
            <p>Your entered feature details have not been created.</p>
            <div className="impact-dialog__actions">
              <button type="button" onClick={guard.keepEditing}>
                Keep editing
              </button>
              <button type="button" className="cockpit__stop" onClick={guard.discard}>
                Discard draft
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </section>
  );
}

function TabOverflowMenu({
  tabs,
  attentionByFeature,
  featureLabel,
  onNavigateHome,
  onNavigateSettings,
  onNavigateFeature,
}: {
  tabs: TabsPrefs;
  attentionByFeature: ReadonlyMap<string, number>;
  featureLabel(featureId: string): string;
  onNavigateHome(): void;
  onNavigateSettings(): void;
  onNavigateFeature(featureId: string): void;
}) {
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const close = useCallback(() => {
    setOpen(false);
    requestAnimationFrame(() => buttonRef.current?.focus());
  }, []);

  useEffect(() => {
    if (!open) return;
    const dismiss = (event: MouseEvent) => {
      if (
        !menuRef.current?.contains(event.target as Node) &&
        !buttonRef.current?.contains(event.target as Node)
      ) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        close();
      }
    };
    window.addEventListener('mousedown', dismiss);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      window.removeEventListener('mousedown', dismiss);
      window.removeEventListener('keydown', onKeyDown);
    };
  }, [close, open]);

  const select = (navigate: () => void) => {
    navigate();
    close();
  };
  return (
    <div className="tab-strip__overflow-wrap">
      <button
        ref={buttonRef}
        type="button"
        className="tab-strip__overflow"
        aria-expanded={open}
        aria-haspopup="menu"
        aria-controls="workspace-tab-menu"
        onClick={() => setOpen((current) => !current)}
      >
        Tabs <span aria-hidden="true">⌄</span>
      </button>
      {open ? (
        <div
          ref={menuRef}
          id="workspace-tab-menu"
          className="tab-strip__menu"
          role="menu"
          aria-label="Open features"
        >
          <button type="button" role="menuitem" onClick={() => select(onNavigateHome)}>
            Home
          </button>
          <button type="button" role="menuitem" onClick={() => select(onNavigateSettings)}>
            Settings
          </button>
          {tabs.open.map((tab) => {
            const count = attentionByFeature.get(tab.featureId) ?? 0;
            return (
              <button
                key={tab.featureId}
                type="button"
                role="menuitem"
                onClick={() => select(() => onNavigateFeature(tab.featureId))}
              >
                {tab.titleHint === '' ? featureLabel(tab.featureId) : tab.titleHint}
                {count > 0 ? ` · ${count} pending` : ''}
              </button>
            );
          })}
        </div>
      ) : null}
    </div>
  );
}

interface FeatureListProps {
  state: ListState;
  openTabIds: readonly string[];
  attentionByFeature: ReadonlyMap<string, number>;
  onOpen(featureId: string, titleHint: string): void;
  onRetry(): void;
  onCreate(): void;
  createButtonRef: RefObject<HTMLButtonElement | null>;
}

/** The masthead run tally: active, waiting-on-you, and shipped counts. */
function HomeReadout({ state }: { state: ListState }) {
  if (state.phase !== 'loaded' || state.features.length === 0) return null;
  let running = 0;
  let waiting = 0;
  let shipped = 0;
  for (const feature of state.features) {
    const group = dashboardGroupId(feature);
    if (group === 'published' || group === 'done') {
      shipped += 1;
      continue;
    }
    const { bucket } = dashboardState(feature);
    if (bucket === 'active') running += 1;
    else if (bucket === 'intervention') waiting += 1;
  }
  return (
    <p className="home-surface__readout">
      <b>{running}</b> running<span aria-hidden="true"> · </span>
      <b>{waiting}</b> waiting on you<span aria-hidden="true"> · </span>
      <b>{shipped}</b> shipped
    </p>
  );
}

function FeatureList({
  state,
  openTabIds,
  attentionByFeature,
  onOpen,
  onRetry,
  onCreate,
  createButtonRef,
}: FeatureListProps) {
  return (
    <section className="feature-list" aria-label="Existing features">
      {state.phase === 'loading' ? (
        <p role="status" className="setup-step__empty">
          Loading features…
        </p>
      ) : state.phase === 'error' ? (
        <div className="feature-list__error">
          <p className="form-field__error">
            {state.error.code}: {state.error.message}
          </p>
          <button type="button" className="setup-wizard__action" onClick={onRetry}>
            Try again
          </button>
        </div>
      ) : state.features.length === 0 ? (
        <div className="feature-list__empty">
          <p className="home-surface__eyebrow">Runtime ready · workspace clear</p>
          <h3>Turn a goal into a supervised run.</h3>
          <p>
            Define the work, choose its repositories, set the pipeline, then review the exact run
            contract before anything is created.
          </p>
          <button
            ref={createButtonRef}
            type="button"
            className="setup-wizard__action"
            onClick={onCreate}
          >
            New feature
          </button>
        </div>
      ) : (
        <div className="feature-list__groups">
          {groupDashboardFeatures(state.features).map((group) => (
            <section
              key={group.id}
              className="feature-list__group"
              aria-labelledby={`feature-list-group-${group.id}`}
            >
              <div className="feature-list__group-head" data-kind={group.id}>
                <h3 id={`feature-list-group-${group.id}`} className="feature-list__group-title">
                  {group.label}
                </h3>
                <span className="feature-list__group-count" aria-hidden="true">
                  {group.id === 'in-progress'
                    ? `· ${group.features.length}`
                    : `× ${group.features.length}`}
                </span>
              </div>
              {group.id === 'in-progress' ? (
                <ul className="run-grid">
                  {group.features.map((feature) => (
                    <RunningCard
                      key={feature.id}
                      feature={feature}
                      isOpen={openTabIds.includes(feature.id)}
                      attentionCount={attentionByFeature.get(feature.id) ?? 0}
                      onOpen={onOpen}
                    />
                  ))}
                </ul>
              ) : (
                <ul className="queue-rows">
                  {group.features.map((feature) => (
                    <QueueRow
                      key={feature.id}
                      feature={feature}
                      isOpen={openTabIds.includes(feature.id)}
                      onOpen={onOpen}
                    />
                  ))}
                </ul>
              )}
            </section>
          ))}
        </div>
      )}
    </section>
  );
}

interface RunRowProps {
  feature: FeatureSnapshot;
  isOpen: boolean;
  onOpen(featureId: string, titleHint: string): void;
}

/** A feature still in flight: full phase rail, live/needs-you badge, one action. */
function RunningCard({
  feature,
  isOpen,
  attentionCount,
  onOpen,
}: RunRowProps & { attentionCount: number }) {
  const stages = spineStages(feature.pipeline);
  const activeIndex = spineActiveIndex(feature, stages);
  const { bucket, label } = dashboardState(feature);
  const needsYou = attentionCount > 0 || bucket === 'intervention';
  const railTone = needsYou ? 'attention' : 'progress';
  // Amber when a run wants you, blue pulse while it's genuinely working, quiet
  // otherwise — so a resting "Code ready" run never reads as live.
  const badgeState = needsYou ? 'attention' : bucket === 'active' ? 'live' : 'quiet';
  // Name the specific reason a parked run needs you (Interrupted / Failed /
  // Review needed / Input needed); a live run with a pending prompt just says
  // "Needs you"; otherwise Live or the resting state.
  const badge =
    bucket === 'intervention'
      ? label
      : needsYou
        ? 'Needs you'
        : bucket === 'active'
          ? label === 'Active'
            ? 'Live'
            : label
          : label;
  const elapsed = formatElapsed(feature);
  return (
    <li className="run-card" data-state={badgeState}>
      <div className="run-card__head">
        <h4 className="run-card__title">{feature.name}</h4>
        <span className="run-card__badges">
          <AttentionBadge count={attentionCount} label={`Blocking input for ${feature.name}`} />
          <span className="run-card__badge">
            <span className="run-card__dot" aria-hidden="true" />
            {badge}
          </span>
        </span>
      </div>
      <p className="run-card__meta">
        <span>
          repo <b>{feature.repos.join(', ')}</b>
        </span>
        <span>
          status <b>{displayStatusLabel(feature.status)}</b>
        </span>
        {elapsed !== null ? (
          <span>
            elapsed <b>{elapsed}</b>
          </span>
        ) : null}
      </p>
      <FlightRail
        stages={stages}
        activeIndex={activeIndex}
        atRest={isRunAtRest(feature.status)}
        tone={railTone}
        label={`Pipeline for ${feature.name}`}
      />
      {feature.failure?.message !== undefined ? (
        <p className="run-card__failure">
          <span aria-hidden="true">! </span>
          {feature.failure.message}
        </p>
      ) : null}
      <div className="run-card__actions">
        <button
          type="button"
          className="run-card__action"
          onClick={() => onOpen(feature.id, feature.name)}
        >
          {isOpen ? 'Show tab' : 'Open'}
        </button>
      </div>
    </li>
  );
}

/** A shipped feature: compact row with an all-done rail and one action. */
function QueueRow({ feature, isOpen, onOpen }: RunRowProps) {
  const stages = spineStages(feature.pipeline);
  const stateLabel = dashboardGroupId(feature) === 'done' ? 'Done' : 'Shipped';
  return (
    <li className="queue-row">
      <span className="queue-row__name">{feature.name}</span>
      <span className="queue-row__repo">{feature.repos.join(', ')}</span>
      <span className="queue-row__rail" aria-hidden="true">
        {stages.map((stage) => (
          <i key={stage.id} className="queue-row__pip" />
        ))}
      </span>
      <span className="queue-row__state">{stateLabel}</span>
      <button
        type="button"
        className="queue-row__action"
        onClick={() => onOpen(feature.id, feature.name)}
      >
        {isOpen ? 'Show tab' : 'Open'}
      </button>
    </li>
  );
}

/**
 * The signature: a horizontal pipeline rail with a filled track to the active
 * stop, a pulsing needle on the current stage, and condensed stage labels.
 */
function FlightRail({
  stages,
  activeIndex,
  atRest,
  tone,
  label,
}: {
  stages: readonly SpineStage[];
  activeIndex: number;
  atRest: boolean;
  tone: 'progress' | 'attention';
  label: string;
}) {
  const reducedMotion = usePrefersReducedMotion();
  const denom = Math.max(stages.length - 1, 1);
  const fillPct = (Math.min(activeIndex, denom) / denom) * 100;
  return (
    <div
      className="flight-rail"
      data-tone={tone}
      role="group"
      aria-label={label}
      style={{ '--stops': stages.length } as CSSProperties}
    >
      <div className="flight-rail__track">
        <span className="flight-rail__fill" style={{ width: `${fillPct}%` }} />
      </div>
      <div className="flight-rail__stops">
        {stages.map((stage, index) => {
          const state =
            index < activeIndex || (atRest && index === activeIndex)
              ? 'done'
              : index === activeIndex
                ? 'active'
                : 'upcoming';
          const isActive = state === 'active';
          return (
            <span
              key={stage.id}
              className="flight-rail__stop"
              data-state={state}
              data-tone={tone}
              {...(isActive ? { 'aria-current': 'step' as const } : {})}
            >
              <span
                className={
                  isActive && !reducedMotion
                    ? 'flight-rail__stop-dot flight-rail__stop-dot--pulse'
                    : 'flight-rail__stop-dot'
                }
                aria-hidden="true"
              />
              <span className="flight-rail__stop-label" title={stage.label}>
                {railStageLabel(stage.label, stages.length)}
              </span>
            </span>
          );
        })}
      </div>
    </div>
  );
}

function AttentionBadge({ count, label }: { count: number; label: string }) {
  if (count === 0) return null;
  return (
    <span className="attention-badge" role="status" aria-label={`${label}: ${count} pending`}>
      {count}
    </span>
  );
}
