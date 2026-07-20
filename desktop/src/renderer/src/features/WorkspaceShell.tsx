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
import { dashboardState, displayStatusLabel, orderDashboardFeatures } from './featureView';
import { BulkPreviewPanel } from './BulkPreviewPanel';
import { RecoveryWorkspace } from './RecoveryWorkspace';

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
  attentionJump?: string | null;
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
  const handledAttentionJump = useRef<string | null>(null);
  const handledRouteRequest = useRef<number | null>(null);
  const listRequestRef = useRef(0);
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
        event.kind.startsWith('config')
      ) {
        loadList();
      }
    });
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
    if (tabs === null || handledAttentionJump.current === attentionJump) return;
    handledAttentionJump.current = attentionJump;
    if (attentionJump === '__recovery__') {
      persist({ ...tabs, activeFeatureId: null });
      setView('home');
    } else {
      openFeature(attentionJump, featureLabel(attentionJump));
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
  }, [persist, tabs]);

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
        <div id="panel-home" role="tabpanel" aria-labelledby="tab-home" className="tab-panel">
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
                  <p className="home-surface__eyebrow">Authoritative feature queue</p>
                  <h1>Home</h1>
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
          className="tab-panel"
        >
          <FeatureCockpit
            key={active}
            featureId={active}
            titleHint={tabs.open.find((tab) => tab.featureId === active)?.titleHint ?? active}
            onClose={() => closeFeature(active)}
            onLoadedName={(name) => renameTab(active, name)}
            attentionItems={attentionItems.filter(
              (item) => item.kind !== 'recovery' && item.featureId === active,
            )}
            refreshAttention={refreshAttention}
            attentionDrafts={activeAttentionDrafts}
            setAttentionDrafts={updateAttentionDrafts}
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
      <h2 className="setup-step__title">Features</h2>
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
        <ul className="feature-list__items">
          {state.features.map((feature) => {
            const rowState = dashboardState(feature);
            const attentionCount = attentionByFeature.get(feature.id) ?? 0;
            return (
              <li key={feature.id} className="feature-list__item" data-tone={rowState.tone}>
                <span className="feature-list__signal" aria-hidden="true" />
                <div className="feature-list__facts">
                  <div className="feature-list__heading">
                    <span className="feature-list__name">{feature.name}</span>
                    <span className="feature-list__state" data-tone={rowState.tone}>
                      <span aria-hidden="true">{rowState.bucket === 'active' ? '◉' : '◆'}</span>{' '}
                      {rowState.label}
                    </span>
                    <AttentionBadge
                      count={attentionCount}
                      label={`Blocking input for ${feature.name}`}
                    />
                  </div>
                  <dl className="feature-list__details">
                    <div>
                      <dt>Repository</dt>
                      <dd>{feature.repos.join(', ')}</dd>
                    </div>
                    <div>
                      <dt>Status</dt>
                      <dd>{displayStatusLabel(feature.status)}</dd>
                    </div>
                    <div>
                      <dt>Current phase</dt>
                      <dd>{feature.currentPhase || 'Not started'}</dd>
                    </div>
                    <div>
                      <dt>Priority</dt>
                      <dd>
                        {rowState.bucket === 'intervention' ? 'Intervention' : rowState.label}
                      </dd>
                    </div>
                  </dl>
                  {feature.failure?.message !== undefined ? (
                    <p className="feature-list__failure">
                      <span aria-hidden="true">!</span> {feature.failure.message}
                    </p>
                  ) : null}
                </div>
                <button
                  type="button"
                  className="setup-wizard__action"
                  onClick={() => onOpen(feature.id, feature.name)}
                >
                  {openTabIds.includes(feature.id) ? 'Show tab' : 'Open'}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </section>
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
