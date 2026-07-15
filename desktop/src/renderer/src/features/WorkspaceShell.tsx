/**
 * The readiness-gated main surface: a Home tab (feature creation + the
 * authoritative feature list) plus one persistent tab per open feature.
 * Local settings store ONLY tab identity and presentation (feature id,
 * title hint, order, active tab); every feature itself is always reloaded
 * from the server, so existing state survives app restarts without any
 * local domain cache.
 */
import { useCallback, useEffect, useRef, useState, type KeyboardEvent } from 'react';
import type { FeatureSummaryView, TabsPrefs } from '../../../shared/ipc';
import { defaultTabsPrefs } from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { CreateFeatureForm } from './CreateFeatureForm';
import { FeatureCockpit } from './FeatureCockpit';

type ListState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; features: FeatureSummaryView[] };

export function WorkspaceShell() {
  // null while the local tab prefs are being restored.
  const [tabs, setTabs] = useState<TabsPrefs | null>(null);
  const [list, setList] = useState<ListState>({ phase: 'loading' });
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

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
    window.agentico
      .listFeatures()
      .then((features) => setList({ phase: 'loaded', features }))
      .catch((err: unknown) => setList({ phase: 'error', error: parseIpcError(err) }));
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
  const activeIsOpen = activeId !== null && tabs.open.some((tab) => tab.featureId === activeId);
  const active = activeIsOpen ? activeId : null;
  const tabCount = tabs.open.length + 1;

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

  return (
    <section className="shell-card workspace" aria-label="Workspace">
      <header className="shell-card__identity">
        <h1 className="shell-card__title">Agentico</h1>
        <span className="shell-card__version">runtime ready</span>
      </header>

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
          onClick={() => persist({ ...tabs, activeFeatureId: null })}
          onKeyDown={(event) => onTabKeyDown(event, 0)}
        >
          Home
        </button>
        {tabs.open.map((tab, index) => (
          <span key={tab.featureId} className="tab-strip__entry">
            <button
              ref={(node) => {
                tabRefs.current[index + 1] = node;
              }}
              type="button"
              role="tab"
              id={`tab-${tab.featureId}`}
              aria-selected={active === tab.featureId}
              aria-controls={`panel-${tab.featureId}`}
              className="tab-strip__tab"
              tabIndex={active === tab.featureId ? 0 : -1}
              onClick={() => persist({ ...tabs, activeFeatureId: tab.featureId })}
              onKeyDown={(event) => onTabKeyDown(event, index + 1)}
            >
              {tab.titleHint === '' ? tab.featureId : tab.titleHint}
            </button>
            <button
              type="button"
              className="tab-strip__close"
              aria-label={`Close ${tab.titleHint === '' ? tab.featureId : tab.titleHint} tab`}
              onClick={() => closeFeature(tab.featureId)}
            >
              <span aria-hidden="true">✕</span>
            </button>
          </span>
        ))}
      </div>

      {active === null ? (
        <div id="panel-home" role="tabpanel" aria-labelledby="tab-home" className="tab-panel">
          <CreateFeatureForm
            onCreated={({ featureId, name }) => {
              openFeature(featureId, name);
              loadList();
            }}
          />
          <FeatureList
            state={list}
            openTabIds={tabs.open.map((tab) => tab.featureId)}
            onOpen={openFeature}
            onRetry={loadList}
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
          />
        </div>
      )}
    </section>
  );
}

interface FeatureListProps {
  state: ListState;
  openTabIds: readonly string[];
  onOpen(featureId: string, titleHint: string): void;
  onRetry(): void;
}

function FeatureList({ state, openTabIds, onOpen, onRetry }: FeatureListProps) {
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
        <p className="setup-step__empty">No features exist yet. Create the first one above.</p>
      ) : (
        <ul className="feature-list__items">
          {state.features.map((feature) => (
            <li key={feature.id} className="feature-list__item">
              <div className="feature-list__facts">
                <span className="feature-list__name">{feature.name}</span>
                <code className="feature-list__meta">
                  {feature.status} · {feature.currentPhase} · {feature.repos.join(', ')}
                </code>
              </div>
              <button
                type="button"
                className="setup-wizard__action"
                onClick={() => onOpen(feature.id, feature.name)}
              >
                {openTabIds.includes(feature.id) ? 'Show tab' : 'Open'}
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
