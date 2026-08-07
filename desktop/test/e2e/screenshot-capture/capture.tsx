import React from 'react';
import { createRoot } from 'react-dom/client';

import '@fontsource/barlow-condensed/500.css';
import '@fontsource/barlow-condensed/600.css';
import '@fontsource/atkinson-hyperlegible/400.css';
import '@fontsource/atkinson-hyperlegible/700.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';

import '../../../src/renderer/src/styles/tokens.css';
import '../../../src/renderer/src/styles/app.css';

import {
  installMockApi,
  CYCLES_FEATURE_SNAPSHOT,
  FEATURE_QUESTION_ITEM,
  FEATURE_QUESTION_BENCH_ITEM,
} from './mock-api';
import { ArchiveMode } from '../../../src/renderer/src/features/ArchiveMode';
import { RewindJourney } from '../../../src/renderer/src/features/RewindJourney';
import { RepositoryInstrument } from '../../../src/renderer/src/features/RepositoryInstrument';
import { CurrentRunInspection } from '../../../src/renderer/src/features/CurrentRunInspection';
import { RefactorLauncher } from '../../../src/renderer/src/features/refactor/RefactorLauncher';
import { BulkPreviewPanel } from '../../../src/renderer/src/features/BulkPreviewPanel';
import { RecoveryWorkspace } from '../../../src/renderer/src/features/RecoveryWorkspace';
import { ChangesSurface } from '../../../src/renderer/src/features/completion/ChangesSurface';
import { PublishModalBody } from '../../../src/renderer/src/features/completion/PublishModal';
import { CleanupConfirm } from '../../../src/renderer/src/features/completion/CleanupConfirm';
import { useCompletionPreflight } from '../../../src/renderer/src/features/completion/useCompletionPreflight';
import type { CompletionAction } from '../../../src/renderer/src/features/completion/completionShared';
import { SettingsPanel } from '../../../src/renderer/src/features/SettingsPanel';
import { WorkspaceShell } from '../../../src/renderer/src/features/WorkspaceShell';
import { FeatureCockpit } from '../../../src/renderer/src/features/FeatureCockpit';
import { ConnectionShell } from '../../../src/renderer/src/components/ConnectionShell';
import { SetupWizard } from '../../../src/renderer/src/components/wizard/SetupWizard';
import type { ReadinessSnapshot } from '../../../src/shared/ipc';
import { AmaPanel } from '../../../src/renderer/src/components/AmaPanel';
import { CommandPalette } from '../../../src/renderer/src/components/CommandPalette';
import { MonacoBuffer } from '../../../src/renderer/src/components/monaco';
import {
  AttentionInbox,
  emptyAttentionDrafts,
} from '../../../src/renderer/src/features/AttentionInbox';
import { spineTone } from '../../../src/renderer/src/features/featureView';
import { classifyHold, railSegments, railTrio } from '../../../src/renderer/src/features/phaseRail';
import { PhaseRail, PhaseRailTrack } from '../../../src/renderer/src/features/PhaseRailRow';
import type {
  AgenticoApi,
  AppRouteEvent,
  AttentionItem,
  FeatureActionRequest,
} from '../../../src/shared/ipc';

function getScene(): string {
  const params = new URLSearchParams(window.location.search);
  return params.get('scene') ?? 'archive';
}

/**
 * Minimal Monaco mount for the first-monaco-lazy-load performance workload:
 * the editor chunk loads only after the click, so the measurement isolates the
 * dynamic import plus first editor attach.
 */
function MonacoLazyLoadScene() {
  const [open, setOpen] = React.useState(false);
  return (
    <div style={{ height: '100vh', display: 'flex', flexDirection: 'column', padding: 16 }}>
      <style>{'.perf-monaco__editor { flex: 1; min-height: 400px; }'}</style>
      <h1>Monaco lazy load</h1>
      <button type="button" onClick={() => setOpen(true)}>
        Open editor
      </button>
      {open ? (
        <MonacoBuffer
          defaultValue={'# Phase plan\n\n- step one\n- step two\n'}
          language="markdown"
          theme="dark"
          ariaLabel="Performance editor"
          className="perf-monaco__editor"
          exposeForE2E
          onChange={() => undefined}
        />
      ) : null}
    </div>
  );
}

function ArchiveScene({ scene }: { scene: string }) {
  const [selectedRun, setSelectedRun] = React.useState(7);
  const badge = scene === 'pinned' ? ('changed' as const) : null;

  return (
    <div
      className="workspace-shell__content"
      style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}
    >
      <div
        className="cockpit"
        aria-label="Feature History and Rewind"
        style={{ flex: 1, overflow: 'auto' }}
      >
        <ArchiveMode
          featureId="abcd1234ef567890"
          selectedRunNumber={selectedRun}
          pipeline="large"
          currentRunBadges={{ changed: badge === 'changed', attention: false }}
          onReturnToCurrent={() => setSelectedRun(0)}
        />
      </div>
    </div>
  );
}

function RewindScene() {
  return (
    <div style={{ position: 'fixed', inset: 0, background: 'var(--bg-elevation-1, #1a1a1a)' }}>
      <RewindJourney
        featureId="abcd1234ef567890"
        featureName="History and Rewind"
        validPhaseOptions={['inquire', 'research', 'design', 'plan', 'implement']}
        currentRoadmapPhase={2}
        totalRoadmapPhases={4}
        onClose={() => {}}
        onRewindComplete={() => {}}
      />
    </div>
  );
}

/** The connection shell mounted the way it appears before the workspace
 * exists: a centered card over the app background, no workspace chrome. */
function ConnectionShellScene() {
  return (
    <div
      style={{
        position: 'fixed',
        inset: 0,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: 'var(--bg-elevation-1, #1a1a1a)',
      }}
    >
      <ConnectionShell />
    </div>
  );
}

/** Providers satisfied, models still outstanding: the wizard's step track
 * reads completed (Providers) / current (Models) / upcoming (Ready). */
const SETUP_WIZARD_MODELS_STEP: ReadinessSnapshot = {
  ready: false,
  providers: [{ name: 'claude', installed: true, version: '2.1.0', ready: true }],
  models: {
    available: false,
    issue: { code: 'models_unavailable', message: 'No models are configured yet.' },
  },
  configuration: { valid: true },
  workspaceRoots: [{ path: '/work/space', valid: true }],
  repositories: [{ name: 'repo-a', path: '/work/space/repo-a', valid: true }],
  issues: [{ code: 'models_unavailable', message: 'No models are configured yet.' }],
};

/** The setup wizard mounted the way it appears before the workspace exists:
 * a centered card over the app background, no workspace chrome. */
function SetupWizardScene() {
  return (
    <div
      style={{
        position: 'fixed',
        inset: 0,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        background: 'var(--bg-elevation-1, #1a1a1a)',
      }}
    >
      <SetupWizard snapshot={SETUP_WIZARD_MODELS_STEP} onSnapshot={() => {}} />
    </div>
  );
}

function RepoInstrumentScene() {
  const snapshot = CYCLES_FEATURE_SNAPSHOT;
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        padding: 'var(--space-4)',
      }}
    >
      <div className="cockpit" aria-label="Feature cockpit" style={{ flex: 1, overflow: 'auto' }}>
        <PhaseRailTrack
          segments={railSegments(snapshot, null)}
          tone={spineTone(snapshot)}
          label="Feature pipeline"
        />
        <div className="toolbar__cockpit-actions" role="group" aria-label="Feature actions">
          <button type="button" className="cockpit__stop">
            Stop
          </button>
          <button type="button" className="cockpit__restart">
            Restart
          </button>
          <button type="button" className="cockpit__rewind-button">
            ↺ Rewind
          </button>
          <button type="button" className="cockpit__cycles-button">
            ⟳ Cycles
          </button>
          <button type="button" className="cockpit__inspector-toggle" aria-expanded={true}>
            Inspector
          </button>
          <p className="cockpit__phase-status" aria-label="Current feature status">
            <code>{snapshot.status}</code>
          </p>
        </div>
        <div className="cockpit__content">
          <main className="cockpit__canvas">
            <h2 className="cockpit__title">{snapshot.name}</h2>
            <dl className="cockpit__facts">
              <div className="cockpit__fact">
                <dt>Status</dt>
                <dd>
                  <code data-status={snapshot.status} />
                </dd>
              </div>
              <div className="cockpit__fact">
                <dt>Repositories</dt>
                <dd>
                  <code>{snapshot.repos.join(', ')}</code>
                </dd>
              </div>
            </dl>
          </main>
          <aside className="cockpit__inspector" aria-label="Feature inspector">
            <header className="cockpit__header">
              <div className="cockpit__identity">
                <h2 className="cockpit__title">{snapshot.name}</h2>
              </div>
            </header>
            {snapshot.repoStatus !== undefined && (
              <RepositoryInstrument
                repos={snapshot.repoStatus}
                onOpenPullRequest={() => undefined}
              />
            )}
          </aside>
        </div>
      </div>
    </div>
  );
}

type RunGaugeMode =
  'active' | 'reviewing' | 'verifying' | 'rest' | 'final-review' | 'held-question' | 'paused';

function RunGaugeScene({
  mode = 'active',
  filesView = false,
}: {
  mode?: RunGaugeMode;
  /** Renders the promoted top-level Files segment instead of the conversation column. */
  filesView?: boolean;
}) {
  const atRest = mode === 'rest';
  const finalReview = mode === 'final-review';
  const reviewing = mode === 'reviewing';
  const verifying = mode === 'verifying';
  const heldQuestion = mode === 'held-question';
  const paused = mode === 'paused';
  // Paused (NeedUserInput) reads like the at-rest/final-review renders for
  // the iteration/phaseStatus fields below: the run isn't mid-iteration
  // work right now, it's parked waiting on a person.
  const restLike = atRest || finalReview || paused;
  const currentPhase = atRest || finalReview ? 'Final Review' : 'Implement';
  const status = atRest
    ? 'CodeReady'
    : finalReview
      ? 'FinalReviewing'
      : paused
        ? 'NeedUserInput'
        : 'Implementing';
  const railSnapshot = {
    ...CYCLES_FEATURE_SNAPSHOT,
    currentPhase,
    status,
    currentRoadmapPhase: atRest ? 12 : 2,
    totalRoadmapPhases: atRest ? 12 : finalReview ? 2 : 5,
    ...(restLike
      ? {}
      : {
          currentIteration: 3,
          phaseStatus: reviewing ? 'reviewing' : verifying ? 'verifying' : 'implementing',
        }),
    reviewGate: {
      reviewingGate: reviewing,
      reviewFixing: false,
      validatingPlan: false,
      validatorStatuses: (reviewing
        ? {
            Craft: 'APPROVED',
            'Functionality/Evidence': 'running',
            Cleanliness: 'APPROVED',
            Design: 'running',
          }
        : {}) as Record<string, string>,
    },
  };
  // Held-question: the run is still executing (an active status) with an
  // open question — classifyHold reads this as `waiting`. Paused: the
  // status itself (NeedUserInput) is the hold — classifyHold reads this as
  // `paused` regardless of the open item's kind, but a waitingSince is
  // still needed for the trio to show a duration rather than an empty value.
  const openAttentionItems: AttentionItem[] = heldQuestion
    ? [
        {
          ...FEATURE_QUESTION_ITEM,
          id: 'rail-held-question',
          waitingSince: new Date(Date.now() - 12 * 60_000).toISOString(),
        },
      ]
    : paused
      ? [
          {
            ...FEATURE_QUESTION_ITEM,
            id: 'rail-paused-need-input',
            waitingSince: new Date(Date.now() - 47 * 60_000).toISOString(),
          },
        ]
      : [];
  const railHold = classifyHold(railSnapshot.status, openAttentionItems);
  return (
    <div className="app-frame">
      <div className="workspace">
        <div className="sidebar" aria-hidden="true" />
        <div className="content-column">
          <header className="toolbar" aria-hidden="true" />
          <div className="cockpit" aria-label="Feature cockpit">
            <div className="toolbar__cockpit-actions" role="group" aria-label="Feature actions">
              <p
                className="cockpit__phase-status"
                role="status"
                aria-label="Current feature status"
              >
                <code data-status="Implementing">Implementing</code>
              </p>
              <span style={{ flex: 1 }} />
              <button type="button" className="cockpit__stop">
                Stop
              </button>
              <details className="cockpit__overflow">
                <summary className="cockpit__overflow-summary" aria-label="More actions">
                  <span aria-hidden="true">⋯</span>
                </summary>
              </details>
            </div>
            <PhaseRail
              segments={railSegments(railSnapshot, railHold)}
              trio={railTrio({
                totalSeconds: atRest ? 0 : 8880,
                totalUsd: atRest ? 0 : 12.4,
                contextPercentage: atRest ? undefined : 42,
                hold: railHold,
              })}
              hold={railHold}
            />
            <div className="cockpit__content">
              <main className="cockpit__stage">
                <div className="cockpit__surface cockpit__surface--live">
                  <CurrentRunInspection
                    featureId="abcd1234ef567890"
                    runNumber={8}
                    currentPhase={currentPhase}
                    currentRoadmapPhase={atRest ? 12 : 2}
                    {...(restLike
                      ? {}
                      : {
                          currentIteration: 3,
                          phaseStatus: reviewing
                            ? 'reviewing'
                            : verifying
                              ? 'verifying'
                              : 'implementing',
                        })}
                    reviewGate={railSnapshot.reviewGate}
                    verificationItems={
                      verifying
                        ? [
                            { name: 'npm run typecheck', state: 'passed' },
                            { name: 'npm run build', state: 'running' },
                            { name: 'make test-fast', state: 'pending' },
                          ]
                        : undefined
                    }
                    shouldStream={false}
                    mode={filesView ? 'files' : 'live'}
                  />
                </div>
                <div className="cockpit__stage-status" />
              </main>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

type CockpitRedesignVariant = 'live' | 'review12' | 'popup' | 'sealed' | 'verification';

const REVIEW12_AXES: { name: string; status: 'running' | 'completed' | 'failed' }[] = [
  { name: 'Architecture', status: 'completed' },
  { name: 'Structural', status: 'completed' },
  { name: 'Grounding', status: 'completed' },
  { name: 'Security', status: 'failed' },
  { name: 'Performance', status: 'running' },
  { name: 'Testing', status: 'completed' },
  { name: 'Scope', status: 'running' },
  { name: 'Craft', status: 'completed' },
  { name: 'Evidence', status: 'running' },
  { name: 'Coverage', status: 'running' },
  { name: 'Docs', status: 'completed' },
  { name: 'Perf budget', status: 'completed' },
];

/**
 * The redesigned cockpit's stage bar, cohort strip, and reading column:
 * hand-assembled with the real classes from app.css (the same pattern
 * RunGaugeScene/ArchiveScene use), since a full
 * IPC-mocked FeatureCockpit mount can't easily reach a 12-axis cohort or an
 * open run-switcher menu deterministically. Every class below is the actual
 * class the redesigned components render.
 */
function CockpitRedesignScene({ variant }: { variant: CockpitRedesignVariant }) {
  const railSnapshot = {
    ...CYCLES_FEATURE_SNAPSHOT,
    currentPhase: variant === 'review12' ? 'Review' : 'Implement',
    status: 'Implementing',
    currentRoadmapPhase: variant === 'review12' ? 6 : 5,
    currentIteration: 3,
    phaseStatus: variant === 'review12' ? 'reviewing' : 'implementing',
    reviewGate: {
      reviewingGate: false,
      reviewFixing: false,
      validatingPlan: false,
      validatorStatuses: {},
    },
  };
  const railHold = classifyHold(railSnapshot.status, []);
  const members =
    variant === 'review12'
      ? REVIEW12_AXES
      : [
          { name: 'agentic-orchestrator', status: 'running' as const },
          { name: 'taulu', status: 'running' as const },
          { name: 'dev-console', status: 'completed' as const },
          { name: 'Plan', status: 'completed' as const },
          { name: 'skills-marketplace', status: 'running' as const },
          { name: 'devcontainer-images', status: 'completed' as const },
          { name: 'agent-gateway', status: 'completed' as const },
        ];
  const running = members.filter((m) => m.status === 'running').length;
  const done = members.filter((m) => m.status === 'completed').length;
  const failed = members.filter((m) => m.status === 'failed').length;
  const selectedName = variant === 'review12' ? 'Security' : 'agentic-orchestrator';

  const stageBar = (
    <div className="cockpit__stage-bar">
      <div className="cockpit__segmented" role="tablist" aria-label="Stage view">
        <button
          type="button"
          role="tab"
          aria-selected="true"
          className="cockpit__segment"
          data-active="true"
          disabled={variant === 'sealed'}
        >
          Live
        </button>
        <button
          type="button"
          role="tab"
          aria-selected="false"
          className="cockpit__segment"
          disabled={variant === 'sealed'}
        >
          Changes
        </button>
        <button
          type="button"
          role="tab"
          aria-selected="false"
          className="cockpit__segment"
          disabled
        >
          Review doc
        </button>
        <button
          type="button"
          role="tab"
          aria-selected="false"
          className="cockpit__segment"
          disabled={variant === 'sealed'}
        >
          Files
        </button>
      </div>
      <div className="cockpit__stage-bar-trailing">
        <details className="cockpit__run-switcher" open={variant === 'popup'}>
          <summary className="cockpit__run-switcher-summary">
            {variant === 'sealed' ? 'Run 6 · sealed' : 'Implement #3'}{' '}
            <span aria-hidden="true">▾</span>
          </summary>
          {variant === 'popup' ? (
            <div className="cockpit__run-switcher-menu" role="menu">
              <button
                type="button"
                role="menuitem"
                className="cockpit__run-switcher-item"
                aria-current="true"
              >
                Implement #3 · current
              </button>
              <button type="button" role="menuitem" className="cockpit__run-switcher-item">
                Run 6 · sealed
              </button>
              <button type="button" role="menuitem" className="cockpit__run-switcher-item">
                Run 5 · sealed
              </button>
              <button type="button" role="menuitem" className="cockpit__run-switcher-item">
                Run 4 · sealed
              </button>
              <button type="button" role="menuitem" className="cockpit__run-switcher-item">
                Run 3 · sealed
              </button>
              <button type="button" role="menuitem" className="cockpit__run-switcher-item">
                Run 2 · sealed
              </button>
              <button type="button" role="menuitem" className="cockpit__run-switcher-item">
                Run 1 · sealed
              </button>
              <button type="button" className="cockpit__run-switcher-more">
                Load older
              </button>
            </div>
          ) : null}
        </details>
      </div>
    </div>
  );

  const cohortStrip = (
    <div className="live-preview__strip-row">
      <div
        className="live-preview__strip"
        role="tablist"
        aria-label="Live agents"
        aria-orientation="horizontal"
      >
        <div className="live-preview__strip-group" role="presentation">
          {members.map((m) => (
            <button
              key={m.name}
              type="button"
              role="tab"
              aria-selected={m.name === selectedName}
              className="live-preview__agent"
              data-status={m.status}
              title={`${m.name} — ${m.status}`}
            >
              <span className="live-preview__agent-state" aria-hidden="true">
                {m.status === 'running' ? (
                  <span className="live-preview__agent-pip" />
                ) : m.status === 'completed' ? (
                  '✓'
                ) : (
                  '▲'
                )}
              </span>
              <span className="live-preview__agent-name">{m.name}</span>
            </button>
          ))}
        </div>
      </div>
      <p className="live-preview__strip-tally">
        {running} running · {done} done
        {failed > 0 ? (
          <span className="live-preview__strip-tally-issues"> · {failed} found issues</span>
        ) : null}
      </p>
    </div>
  );

  const readingColumn = (
    <section
      className="conversation__scroll live-preview__transcript"
      aria-label="Live agent transcript"
    >
      <article className="conversation__message" data-role="assistant">
        <span className="conversation__message-role">agentic-orchestrator</span>
        <p>
          Wiring the streamed preview into the cockpit so file changes surface as diffs instead of
          arriving on a poll.
        </p>
      </article>
      <article className="conversation__file-change" aria-label="Updated src/renderer/app.tsx">
        <header className="conversation__file-change-header">
          <span className="conversation__file-change-path">src/renderer/app.tsx</span>
          <span className="conversation__file-change-status">updated</span>
          <span
            className="conversation__file-change-stats"
            aria-label="2 lines added, 1 line removed"
          >
            <span data-kind="added">+2</span>
            <span data-kind="removed">−1</span>
          </span>
        </header>
        <div
          className="conversation__diff"
          role="region"
          aria-label="Diff for src/renderer/app.tsx"
        >
          <div className="conversation__diff-line" data-kind="removed">
            <span className="conversation__diff-marker" aria-hidden="true">
              −
            </span>
            <code>const preview = usePolledPreview(featureId);</code>
          </div>
          <div className="conversation__diff-line" data-kind="added">
            <span className="conversation__diff-marker" aria-hidden="true">
              +
            </span>
            <code>const preview = useStreamedPreview(featureId);</code>
          </div>
          <div className="conversation__diff-line" data-kind="added">
            <span className="conversation__diff-marker" aria-hidden="true">
              +
            </span>
            <code>const transcript = preview?.transcript ?? [];</code>
          </div>
        </div>
      </article>
      <article className="conversation__message" data-role="assistant">
        <span className="conversation__message-role">agentic-orchestrator</span>
        <p>Created the stream hook so the preview subscribes once and stops polling.</p>
      </article>
      {variant === 'verification' ? (
        <>
          <p className="conversation__verification-tick" data-tone="passed">
            <span aria-hidden="true">✓</span> npm run typecheck
          </p>
          <p className="conversation__verification-tick" data-tone="running">
            <span aria-hidden="true">⟳</span> npm run build
          </p>
          <p className="conversation__verification-tick" data-tone="running">
            <span aria-hidden="true">⟳</span> Verification: 1 of 3 checks passing
          </p>
        </>
      ) : (
        <p className="conversation__verification-tick" data-tone="running">
          <span aria-hidden="true">⟳</span> Verification: 6 of 8 checks passing
        </p>
      )}
      {variant === 'review12' ? (
        <article className="conversation__message" data-role="assistant">
          <span className="conversation__message-role">Security</span>
          <p>
            Reviewed the streamed preview subscription and the two call sites it replaces. One issue
            worth blocking on.
          </p>
        </article>
      ) : null}
    </section>
  );

  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <div className="workspace">
        <div className="sidebar" aria-hidden="true" />
        <div className="content-column">
          <header className="toolbar">
            <div className="toolbar__leading">
              <button type="button" className="toolbar__sidebar-toggle" aria-label="Hide sidebar">
                <span aria-hidden="true">▥</span>
              </button>
            </div>
            <div className="toolbar__title">
              <p className="toolbar__title-name">translate README to Italian</p>
              <p className="toolbar__title-subline">
                agentic-orchestrator · feature/translate-readme-it
              </p>
            </div>
            <div className="toolbar__trailing">
              <div className="toolbar__cockpit-actions" role="group" aria-label="Feature actions">
                <p
                  className="cockpit__phase-status"
                  role="status"
                  aria-label="Current feature status"
                >
                  <code data-status="Implementing">Implementing</code>
                </p>
                {variant !== 'sealed' ? (
                  <button type="button" className="cockpit__stop">
                    Stop
                  </button>
                ) : null}
              </div>
              <div className="toolbar__overflow-slot">
                <details className="cockpit__overflow">
                  <summary className="cockpit__overflow-summary" aria-label="More actions">
                    <span aria-hidden="true">⋯</span>
                  </summary>
                  <div className="cockpit__overflow-menu" role="menu">
                    <div className="cockpit__overflow-item" data-variant="default">
                      <button type="button" role="menuitem">
                        Edit configuration…
                      </button>
                    </div>
                    <div className="cockpit__overflow-item" data-variant="danger">
                      <button type="button" role="menuitem">
                        Delete
                      </button>
                    </div>
                  </div>
                </details>
              </div>
              <div className="toolbar__inspector-slot">
                <button
                  type="button"
                  className="toolbar__inspector-toggle"
                  aria-label="Toggle inspector"
                  aria-pressed="false"
                >
                  <span aria-hidden="true">▤</span>
                </button>
              </div>
            </div>
          </header>
          <div className="cockpit" aria-label="Feature cockpit">
            <PhaseRail
              segments={railSegments(railSnapshot, railHold)}
              trio={railTrio({
                totalSeconds: 10500,
                totalUsd: 3.08,
                contextPercentage: 42,
                hold: railHold,
              })}
              hold={railHold}
              tone="progress"
            />
            <div className="cockpit__content">
              <main className="cockpit__stage">
                {stageBar}
                {variant === 'sealed' ? (
                  <div className="cockpit__archive">
                    <div className="archive-mode__band" role="status">
                      <span className="archive-mode__band-label">Sealed run · Read only</span>
                      <span className="archive-mode__run-meta">Run 6 · sealed 2026-08-04</span>
                    </div>
                    <div className="cockpit__content archive-mode__content">
                      <main className="archive-mode__main">
                        <dl className="archive-mode__facts">
                          <div className="archive-mode__fact">
                            <dt>Phase</dt>
                            <dd>Implement</dd>
                          </div>
                          <div className="archive-mode__fact">
                            <dt>Iteration</dt>
                            <dd>2</dd>
                          </div>
                          <div className="archive-mode__fact">
                            <dt>Duration</dt>
                            <dd>1h 24m</dd>
                          </div>
                          <div className="archive-mode__fact">
                            <dt>Cost</dt>
                            <dd>$2.86</dd>
                          </div>
                        </dl>
                        <div className="archive-mode__artifacts">
                          <h3 className="archive-mode__section-title">Artifacts</h3>
                          <ul className="archive-mode__artifact-list">
                            <li className="archive-mode__artifact-item">
                              <button type="button" className="archive-mode__artifact-button">
                                phase-plan.md · Implement · 4.1 KB
                              </button>
                            </li>
                            <li className="archive-mode__artifact-item">
                              <button type="button" className="archive-mode__artifact-button">
                                review-notes.md · Implement · 1.8 KB
                              </button>
                            </li>
                          </ul>
                        </div>
                        <div className="archive-mode__logs">
                          <h3 className="archive-mode__section-title">Bounded logs</h3>
                          <ul
                            className="archive-mode__artifact-list"
                            aria-label="Available historical logs"
                          >
                            <li className="archive-mode__artifact-item">
                              <button type="button" className="archive-mode__artifact-button">
                                phase.log · 12.3 KB
                              </button>
                            </li>
                          </ul>
                        </div>
                        <div className="archive-mode__sessions">
                          <h3 className="archive-mode__section-title">Historical timeline</h3>
                          <ul className="archive-mode__session-list">
                            <li className="archive-mode__session-item">
                              <button type="button" className="archive-mode__session-button">
                                <span className="archive-mode__session-id">
                                  agentic-orchestrator
                                </span>
                              </button>
                            </li>
                          </ul>
                        </div>
                      </main>
                      <aside
                        className="cockpit__inspector archive-mode__inspector"
                        aria-label="Feature inspector"
                      >
                        <h3 className="archive-mode__section-title">Historical context</h3>
                        <dl className="archive-mode__inspector-facts">
                          <div className="archive-mode__fact">
                            <dt>Repository</dt>
                            <dd>agentic-orchestrator</dd>
                          </div>
                        </dl>
                      </aside>
                    </div>
                  </div>
                ) : (
                  <>
                    {cohortStrip}
                    <div className="cockpit__surface cockpit__surface--live">
                      <div className="live-preview__frame">
                        <div className="live-preview__bar">
                          <p className="cockpit__caption">Live activity</p>
                          <div className="live-preview__bar-controls">
                            <div
                              className="live-preview__views"
                              role="group"
                              aria-label="Preview view"
                            >
                              <button
                                type="button"
                                className="live-preview__view"
                                aria-pressed="true"
                              >
                                Conversation
                              </button>
                              <button
                                type="button"
                                className="live-preview__view"
                                aria-pressed="false"
                              >
                                Signal trace
                              </button>
                            </div>
                          </div>
                        </div>
                        {readingColumn}
                      </div>
                    </div>
                  </>
                )}
              </main>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

/**
 * Aftercare renders under a real toolbar: the trailing zone owns the state
 * chip, the wrap-up verbs, the ⋯ menu, and the inspector toggle, so the scene
 * mounts the same three portal hosts the shell provides.
 */
function AftercareScene() {
  const [drafts, setDrafts] = React.useState(emptyAttentionDrafts);
  const [actionsSlot, setActionsSlot] = React.useState<HTMLDivElement | null>(null);
  const [overflowSlot, setOverflowSlot] = React.useState<HTMLDivElement | null>(null);
  const [inspectorSlot, setInspectorSlot] = React.useState<HTMLDivElement | null>(null);
  const [title, setTitle] = React.useState('Configure per-phase effort level');
  return (
    <div className="app-frame">
      <div className="workspace">
        <div className="sidebar" aria-hidden="true" />
        <div className="content-column">
          <header className="toolbar" aria-label="Workspace toolbar">
            <div className="toolbar__leading">
              <button type="button" className="toolbar__sidebar-toggle" aria-label="Hide sidebar">
                <span aria-hidden="true">▥</span>
              </button>
            </div>
            <div className="toolbar__title">
              <p className="toolbar__title-name">{title}</p>
              <p className="toolbar__title-subline">
                agentic-orchestrator · feature/configure-per-phase-effort-level
              </p>
            </div>
            <div className="toolbar__trailing">
              <div className="toolbar__actions-slot" ref={setActionsSlot} />
              <div className="toolbar__overflow-slot" ref={setOverflowSlot} />
              <div className="toolbar__inspector-slot" ref={setInspectorSlot} />
            </div>
          </header>
          <FeatureCockpit
            featureId="abcd1234ef567890"
            titleHint="Configure per-phase effort level"
            onClose={() => undefined}
            onLoadedName={setTitle}
            attentionItems={[]}
            refreshAttention={() => Promise.resolve([])}
            attentionDrafts={drafts}
            setAttentionDrafts={setDrafts}
            actionsHost={actionsSlot}
            overflowMenuHost={overflowSlot}
            inspectorToggleHost={inspectorSlot}
          />
        </div>
      </div>
    </div>
  );
}

function FeatureQuestionScene({ scene }: { scene: string }): React.ReactElement {
  const [drafts, setDrafts] = React.useState(emptyAttentionDrafts);
  const attentionItems: AttentionItem[] = [
    scene === 'feature-question-bench' ? FEATURE_QUESTION_BENCH_ITEM : FEATURE_QUESTION_ITEM,
  ];
  return (
    <div className="app-frame">
      <div className="workspace">
        <div className="sidebar" aria-hidden="true" />
        <div className="content-column">
          <header className="toolbar" aria-hidden="true" />
          <FeatureCockpit
            featureId="abcd1234ef567890"
            titleHint="History and Rewind"
            onClose={() => undefined}
            onLoadedName={() => undefined}
            attentionItems={attentionItems}
            refreshAttention={() => Promise.resolve(attentionItems)}
            attentionDrafts={drafts}
            setAttentionDrafts={setDrafts}
          />
        </div>
      </div>
    </div>
  );
}

/**
 * The gate sheet's two branches. The plain branch carries two questions, a
 * known phase/iteration/stop time (so the dynamic lede renders in full), and
 * one drafted answer; the verification branch carries real blockers so the
 * structured decision renders.
 */
function gateSceneItems(scene: string): AttentionItem[] {
  if (scene === 'post-cycle-gate') {
    return [
      {
        kind: 'gate',
        id: 'post-cycle-gate',
        featureId: 'abcd1234ef567890',
        waitingSince: '2026-07-25T10:00:00.000Z',
        repoName: 'agentic-orchestrator',
        summary: 'The agent needs one decision before it can finish the rebase.',
        questions: [
          {
            index: 1,
            prompt: 'Which compatibility behavior should remain explicit?',
            answer: '',
          },
        ],
      },
    ];
  }
  if (scene === 'gate-sheet-plain') {
    return [
      {
        kind: 'gate',
        id: 'gate-sheet-plain',
        featureId: 'abcd1234ef567890',
        waitingSince: new Date(Date.now() - 11 * 60_000).toISOString(),
        repoName: 'agentic-orchestrator',
        iteration: 3,
        questions: [
          {
            index: 1,
            prompt:
              'The style guide requires the formal register (Lei), but three code comments quote informal user copy verbatim. Translate those quotes, or leave them as written?',
            answer: 'Leave the quoted copy in English and add a translator note above each one.',
          },
          {
            index: 2,
            prompt:
              'There is no established Italian term for "slop removal pass". What wording should be used consistently?',
            answer: '',
          },
        ],
      },
    ];
  }
  if (scene === 'gate-sheet-verification') {
    return [
      {
        kind: 'gate',
        id: 'gate-sheet-verification',
        featureId: 'abcd1234ef567890',
        waitingSince: new Date(Date.now() - 24 * 60_000).toISOString(),
        repoName: 'agentic-orchestrator',
        iteration: 3,
        questions: [{ index: 1, prompt: 'How should Agentico continue?', answer: '' }],
        verification: {
          blockers: [
            {
              itemId: 'deploy-smoke',
              name: 'Deployment smoke test',
              repoName: 'agentic-orchestrator',
              command: 'make deploy-smoke',
              reason: 'missing declared capability "Okta session"',
              capabilities: ['Okta session'],
              remediation: 'Make an Okta session available, then retry verification.',
            },
          ],
          allowedActions: ['RETRY_AFTER_AUTH', 'WAIVE'],
        },
      },
    ];
  }
  return [];
}

function PostImplementationScene({ scene }: { scene: string }) {
  const [drafts, setDrafts] = React.useState(emptyAttentionDrafts);
  const gateItems = React.useMemo(() => gateSceneItems(scene), [scene]);
  return (
    <div className="app-frame">
      <div className="workspace">
        <div className="sidebar" aria-hidden="true" />
        <div className="content-column">
          <header className="toolbar" aria-hidden="true" />
          <FeatureCockpit
            featureId="abcd1234ef567890"
            titleHint="Configure per-phase effort level"
            onClose={() => undefined}
            onLoadedName={() => undefined}
            attentionItems={gateItems}
            refreshAttention={() => Promise.resolve(gateItems)}
            attentionDrafts={drafts}
            setAttentionDrafts={setDrafts}
          />
        </div>
      </div>
    </div>
  );
}

function CycleModalScene() {
  const snapshot = CYCLES_FEATURE_SNAPSHOT;
  const title = 'Start refactor';
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
        padding: 'var(--space-4)',
      }}
    >
      <div
        className="cockpit__modal"
        style={{
          padding: '0',
          minHeight: 0,
          overflow: 'auto',
        }}
      >
        <header className="cockpit__modal-header">
          <h3>{title}</h3>
          <button type="button" className="cockpit__modal-close">
            Close
          </button>
        </header>
        <div className="cockpit__modal-body">
          <RefactorLauncher
            featureId="abcd1234ef567890"
            snapshot={snapshot}
            onCancel={() => {}}
            onDispatched={() => {}}
          />
        </div>
      </div>
    </div>
  );
}

function BulkPreviewScene() {
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'auto',
        padding: '1rem',
      }}
    >
      <BulkPreviewPanel />
    </div>
  );
}

function RecoveryScene({ scene }: { scene: string }) {
  const [attentionItems] = React.useState<
    { kind: 'recovery'; id: string; waitingSince: string; liveCount: number; deadCount: number }[]
  >(
    scene === 'recovery' || scene === 'recovery-constrained'
      ? [
          {
            kind: 'recovery' as const,
            id: 'recovery-scan',
            waitingSince: new Date(Date.now() - 120_000).toISOString(),
            liveCount: 1,
            deadCount: 1,
          },
        ]
      : [],
  );
  const [drafts] = React.useState(emptyAttentionDrafts());
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'auto',
        padding: 'var(--space-4)',
      }}
    >
      <div className="toolbar__trailing" style={{ marginBottom: 'var(--space-3)' }}>
        <AttentionInbox
          items={attentionItems}
          refresh={async () => attentionItems}
          featureLabel={() => 'Recovery workspace'}
          drafts={drafts}
          setDrafts={() => {}}
          onJump={() => {}}
        />
      </div>
      <RecoveryWorkspace onNavigateToFeature={() => {}} />
    </div>
  );
}

function CompletionScene({ scene }: { scene: string }): React.ReactElement {
  const api = (window as unknown as { agentico: AgenticoApi }).agentico;
  const completion = useCompletionPreflight('feat-electron-app', true, (id) =>
    api.preflightCompletion({ featureId: id }),
  );
  const getRepositoryDiff = (id: string, repo: string, filePath?: string) =>
    api.getRepositoryDiff({ featureId: id, repo, ...(filePath ? { filePath } : {}) });
  const dispatchAction = (id: string, action: CompletionAction, body?: Record<string, unknown>) =>
    api.dispatchFeatureAction({
      featureId: id,
      action,
      ...(body ? { body } : {}),
    } as FeatureActionRequest);
  const openExternal = (url: string) => api.openExternal({ url });
  const revealPath = (id: string, repo: string) => api.revealPath({ featureId: id, repo });

  if (scene === 'completion-publish') {
    return (
      <div style={{ height: '100vh', display: 'flex', flexDirection: 'column', padding: '24px' }}>
        <div className="cockpit__modal" style={{ maxWidth: '720px' }}>
          {completion.preflight !== null ? (
            <PublishModalBody
              featureId="feat-electron-app"
              preflight={completion.preflight}
              dispatchAction={dispatchAction}
              generatePublishDescription={(id, repos) =>
                api.generatePublishDescription({ featureId: id, repos })
              }
              openExternal={openExternal}
              onDispatched={() => {}}
            />
          ) : null}
        </div>
      </div>
    );
  }

  if (scene === 'completion-delete') {
    return (
      <div style={{ height: '100vh', position: 'relative' }}>
        {completion.preflight !== null ? (
          <CleanupConfirm
            featureId="feat-electron-app"
            preflight={completion.preflight}
            dispatchAction={dispatchAction}
            onClose={() => {}}
            onDispatched={() => {}}
          />
        ) : null}
      </div>
    );
  }

  const isConstrained = scene === 'completion-constrained';
  return (
    <div
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        padding: isConstrained ? '0' : '24px',
        maxWidth: isConstrained ? '760px' : '100%',
        margin: isConstrained ? '0 auto' : undefined,
      }}
    >
      <ChangesSurface
        featureId="feat-electron-app"
        preflight={completion.preflight}
        loading={completion.loading}
        error={completion.error}
        onRetry={() => void completion.refresh()}
        getRepositoryDiff={getRepositoryDiff}
        openExternal={openExternal}
        revealPath={revealPath}
      />
    </div>
  );
}

function BackgroundScene({ scene }: { scene: string }): React.ReactElement {
  const [drafts, setDrafts] = React.useState(emptyAttentionDrafts());
  const [attentionItems, setAttentionItems] = React.useState<AttentionItem[]>([]);
  const paletteRoute =
    scene === 'background-command-palette'
      ? { id: 2, event: { target: 'palette' as const } }
      : null;

  React.useEffect(() => {
    void window.agentico.getAttention().then((snapshot) => setAttentionItems(snapshot.items));
  }, []);

  const refreshAttention = React.useCallback(async () => {
    const snapshot = await window.agentico.getAttention();
    setAttentionItems(snapshot.items);
    return snapshot.items;
  }, []);

  const route = React.useCallback((_event: AppRouteEvent) => {}, []);

  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <header className="toolbar">
        <div className="toolbar__title">
          <p className="toolbar__title-name">History and Rewind</p>
        </div>
        <div className="toolbar__trailing">
          <AttentionInbox
            items={attentionItems}
            refresh={refreshAttention}
            featureLabel={() => 'History and Rewind'}
            drafts={drafts}
            setDrafts={setDrafts}
            onJump={() => {}}
            openRequest={null}
          />
        </div>
      </header>
      {
        <div className="tab-panel" style={{ flex: 1, minHeight: 0 }}>
          <header className="overview-surface__header">
            <div>
              <p className="eyebrow-label">Background supervision</p>
              <h1>History and Rewind</h1>
            </div>
            <button type="button" className="toolbar__new-feature">
              New feature
            </button>
          </header>
          <section className="toolbar__cockpit-actions" role="group" aria-label="Feature actions">
            <button type="button" className="cockpit__stop">
              Stop
            </button>
            <button type="button" className="cockpit__restart">
              Restart
            </button>
            <button type="button" className="cockpit__rewind-button">
              Rewind
            </button>
            <p className="cockpit__phase-status" aria-label="Current feature status">
              <code>implementing</code>
            </p>
          </section>
          {scene === 'background-close-dialog' ? <CloseDialogScene /> : null}
        </div>
      }
      <CommandPalette ready={true} routeRequest={paletteRoute} onRoute={route} />
    </div>
  );
}

function CloseDialogScene(): React.ReactElement {
  return (
    <div className="impact-dialog__backdrop" style={{ position: 'absolute' }}>
      <div
        className="impact-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Work is still running"
      >
        <h2>Work is still running</h2>
        <p>Agentico has background work that may continue without the window.</p>
        <p>
          1 feature run is stoppable. The AMA session is active. Keep Running hides the window and
          leaves work attached.
        </p>
        <div className="impact-dialog__actions">
          <button type="button">Keep Running</button>
          <button type="button" className="cockpit__stop">
            Stop Work and Quit
          </button>
          <button type="button">Cancel</button>
        </div>
      </div>
    </div>
  );
}

/**
 * The update notice inside the real shell: nothing here is hand-built. The same
 * WorkspaceShell the app mounts renders the toolbar, the always-visible
 * attention bell, the transient update trigger, the sidebar footer dot, and the
 * real UpdatePopover — all of it driven by the mock API's update state, so the
 * scene proves the notice occupies no flow space. The popover never opens on
 * its own; the specs click the trigger, exactly as a user does.
 *
 * The scene data (Overview selection, the active-work summary that unlocks
 * Install When Idle) lives in mock-api, keyed by scene id.
 */
function UpdateAppScene(): React.ReactElement {
  const [update, setUpdate] = React.useState<Awaited<ReturnType<AgenticoApi['getUpdates']>> | null>(
    null,
  );
  const [dismissedVersion, setDismissedVersion] = React.useState<string | null>(null);
  const [showSettings, setShowSettings] = React.useState(false);
  // This scene photographs the bell beside the update trigger, so it has to feed
  // the shell the same attention snapshot the app would — otherwise the badge
  // reads zero next to a populated "Waiting on you" lane.
  const [attentionItems, setAttentionItems] = React.useState<AttentionItem[]>([]);
  React.useEffect(() => {
    void window.agentico.getUpdates().then(setUpdate);
    void window.agentico.getAttention().then((snapshot) => setAttentionItems(snapshot.items));
  }, []);
  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <WorkspaceShell
        attentionItems={attentionItems}
        updateState={update}
        updateDismissedVersion={dismissedVersion}
        schedulingUpdate={false}
        onDismissUpdate={setDismissedVersion}
        onOpenUpdatesSettings={() => setShowSettings(true)}
        onInstallUpdateWhenIdle={async () => {
          setUpdate(await window.agentico.installUpdateWhenIdle());
        }}
        routeRequest={
          showSettings
            ? {
                id: 1,
                event: { target: 'settings', settingsSection: 'updates' },
              }
            : null
        }
      />
    </div>
  );
}

/**
 * The attention popover inside the real shell: the same WorkspaceShell the app
 * mounts, so the real bell sits in a real toolbar — badge and all — over a real
 * feature cockpit, and the surface it opens is the real AttentionInbox popover.
 * The mixed snapshot (an ownerless verification gate plus feature-owned
 * permission and review items) comes from the mock API. The scene does not open
 * the popover: the evidence spec clicks the bell, mirroring how the
 * creation-sheet and ama-panel scenes leave the interaction to the spec.
 */
function AttentionPopoverScene(): React.ReactElement {
  const [drafts, setDrafts] = React.useState(emptyAttentionDrafts());
  const [attentionItems, setAttentionItems] = React.useState<AttentionItem[]>([]);

  React.useEffect(() => {
    void window.agentico.getAttention().then((snapshot) => setAttentionItems(snapshot.items));
  }, []);

  const refreshAttention = React.useCallback(async () => {
    const snapshot = await window.agentico.getAttention();
    setAttentionItems(snapshot.items);
    return snapshot.items;
  }, []);

  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <WorkspaceShell
        attentionItems={attentionItems}
        refreshAttention={refreshAttention}
        attentionDrafts={drafts}
        setAttentionDrafts={setDrafts}
      />
    </div>
  );
}

function SettingsUpdateScene({ scene }: { scene: string }): React.ReactElement {
  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <header className="toolbar">
        <div className="toolbar__title">
          <p className="toolbar__title-name">Agentico</p>
        </div>
      </header>
      <div className="tab-panel" style={{ flex: 1, minHeight: 0 }}>
        <SettingsPanel
          routeRequest={{
            id: 1,
            event: {
              target: 'settings',
              settingsSection: scene === 'settings-diagnostics' ? 'diagnostics' : 'updates',
            },
          }}
        />
      </div>
    </div>
  );
}

function OverviewLanesScene({ empty = false }: { empty?: boolean }): React.ReactElement {
  const attentionItems: AttentionItem[] = empty
    ? []
    : [
        {
          kind: 'permission',
          id: 'perm-updater',
          featureId: 'updater-auto-1',
          sessionId: 'sess-updater',
          phase: 'Review',
          toolName: 'Bash',
          summary: 'Approve the plan before implementation continues.',
          input: { command: 'apply plan' },
          waitingSince: '2026-07-23T09:05:00Z',
        },
      ];
  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <header className="toolbar">
        <div className="toolbar__title">
          <p className="toolbar__title-name">Agentico</p>
        </div>
      </header>
      <WorkspaceShell
        attentionItems={attentionItems}
        refreshAttention={async () => attentionItems}
      />
    </div>
  );
}

/**
 * The creation sheet inside the real shell: the same WorkspaceShell the app
 * mounts, so the sheet descends from a real toolbar over a live, dimmed
 * Overview instead of hand-built chrome. The scene opens it through the real
 * "New feature" entry point; the evidence spec drives the steps from there.
 */
function CreationSheetScene(): React.ReactElement {
  const attentionItems: AttentionItem[] = [
    {
      kind: 'permission',
      id: 'perm-updater',
      featureId: 'updater-auto-1',
      sessionId: 'sess-updater',
      phase: 'Review',
      toolName: 'Bash',
      summary: 'Approve the plan before implementation continues.',
      input: { command: 'apply plan' },
      waitingSince: '2026-07-23T09:05:00Z',
    },
  ];
  const [opened, setOpened] = React.useState(false);
  React.useEffect(() => {
    if (opened) return;
    const timer = window.setInterval(() => {
      const trigger = Array.from(document.querySelectorAll('button')).find(
        (button) => button.textContent === 'New feature',
      );
      if (trigger !== undefined) {
        trigger.click();
        setOpened(true);
      }
    }, 50);
    return () => window.clearInterval(timer);
  }, [opened]);
  // No stand-in title bar here: the shell's own toolbar has to be the window's
  // top row so the sheet descends onto it exactly as it does in the app.
  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <WorkspaceShell
        attentionItems={attentionItems}
        refreshAttention={async () => attentionItems}
      />
    </div>
  );
}

/**
 * The floating AMA panel inside the real shell: the same WorkspaceShell the app
 * mounts (so the panel floats over a live cockpit and the sidebar footer shows
 * its own active-session state) plus the real AmaPanel, opened from the
 * persisted preference exactly as the app opens it. The evidence spec drives
 * the attachment, confirmation, drag, resize, and expand states from here.
 */
function AmaPanelScene(): React.ReactElement {
  const [drafts, setDrafts] = React.useState(emptyAttentionDrafts());
  const [attentionItems, setAttentionItems] = React.useState<AttentionItem[]>([]);
  const [amaSessionActive, setAmaSessionActive] = React.useState(false);

  React.useEffect(() => {
    void window.agentico.getAttention().then((snapshot) => setAttentionItems(snapshot.items));
  }, []);

  const refreshAttention = React.useCallback(async () => {
    const snapshot = await window.agentico.getAttention();
    setAttentionItems(snapshot.items);
    return snapshot.items;
  }, []);

  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <WorkspaceShell
        attentionItems={attentionItems}
        refreshAttention={refreshAttention}
        attentionDrafts={drafts}
        setAttentionDrafts={setDrafts}
        amaSessionActive={amaSessionActive}
      />
      <AmaPanel
        attentionItems={attentionItems}
        refreshAttention={refreshAttention}
        attentionDrafts={drafts}
        setAttentionDrafts={setDrafts}
        routeRequest={null}
        onSessionActiveChange={setAmaSessionActive}
      />
    </div>
  );
}

function CaptureApp() {
  const scene = getScene();

  if (scene.startsWith('creation-sheet')) {
    return <CreationSheetScene />;
  }
  if (scene === 'ama-panel') {
    return <AmaPanelScene />;
  }
  if (scene === 'overview-lanes') {
    return <OverviewLanesScene />;
  }
  if (scene === 'overview-empty') {
    return <OverviewLanesScene empty />;
  }

  if (scene === 'attention-popover') {
    return <AttentionPopoverScene />;
  }
  if (scene === 'update-popover') {
    return <UpdateAppScene />;
  }
  if (
    scene === 'settings-updates-ready' ||
    scene === 'settings-updates-deb' ||
    scene === 'settings-install-now-confirm' ||
    scene === 'settings-diagnostics'
  ) {
    return <SettingsUpdateScene scene={scene} />;
  }

  if (scene === 'archive' || scene === 'pinned' || scene === 'constrained') {
    return <ArchiveScene scene={scene} />;
  }
  if (scene === 'monaco-lazy-load') {
    return <MonacoLazyLoadScene />;
  }
  if (scene === 'rewind-confirm' || scene === 'fork') {
    return <RewindScene />;
  }
  if (scene === 'repo-instrument') {
    return <RepoInstrumentScene />;
  }
  if (scene === 'run-gauge') {
    return <RunGaugeScene />;
  }
  if (scene === 'run-gauge-reviewing') {
    return <RunGaugeScene mode="reviewing" />;
  }
  if (scene === 'run-gauge-verifying') {
    return <RunGaugeScene mode="verifying" />;
  }
  if (scene === 'run-gauge-rest') {
    return <RunGaugeScene mode="rest" />;
  }
  if (scene === 'run-gauge-final-review') {
    return <RunGaugeScene mode="final-review" />;
  }
  if (scene === 'run-gauge-held-question') {
    return <RunGaugeScene mode="held-question" />;
  }
  if (scene === 'run-gauge-paused') {
    return <RunGaugeScene mode="paused" />;
  }
  if (scene === 'run-gauge-files') {
    return <RunGaugeScene filesView />;
  }
  if (scene === 'cockpit-redesign-live') {
    return <CockpitRedesignScene variant="live" />;
  }
  if (scene === 'cockpit-redesign-review12') {
    return <CockpitRedesignScene variant="review12" />;
  }
  if (scene === 'cockpit-redesign-popup') {
    return <CockpitRedesignScene variant="popup" />;
  }
  if (scene === 'cockpit-redesign-sealed') {
    return <CockpitRedesignScene variant="sealed" />;
  }
  if (scene === 'cockpit-redesign-verification') {
    return <CockpitRedesignScene variant="verification" />;
  }
  if (scene === 'connection-shell') {
    return <ConnectionShellScene />;
  }
  if (scene === 'setup-wizard') {
    return <SetupWizardScene />;
  }
  if (scene.startsWith('aftercare') || scene === 'refactor-pass') {
    return <AftercareScene />;
  }
  if (scene === 'feature-question' || scene === 'feature-question-bench') {
    return <FeatureQuestionScene scene={scene} />;
  }
  if (scene.startsWith('post-cycle-') || scene.startsWith('gate-sheet-')) {
    return <PostImplementationScene scene={scene} />;
  }
  if (scene === 'refactor-launch') {
    return <CycleModalScene />;
  }
  if (scene === 'bulk-preview' || scene === 'bulk-queue') {
    return <BulkPreviewScene />;
  }
  if (scene === 'recovery' || scene === 'recovery-constrained') {
    return <RecoveryScene scene={scene} />;
  }
  if (
    scene === 'completion-inspect' ||
    scene === 'completion-publish' ||
    scene === 'completion-constrained' ||
    scene === 'completion-delete'
  ) {
    return <CompletionScene scene={scene} />;
  }
  if (scene.startsWith('background-')) {
    return <BackgroundScene scene={scene} />;
  }
  return <div>Unknown scene: {scene}</div>;
}

const themeParam = new URLSearchParams(window.location.search).get('theme');
if (themeParam === 'light' || themeParam === 'dark') {
  document.documentElement.dataset['theme'] = themeParam;
}

const container = document.getElementById('root');
if (!container) throw new Error('#root missing');

const mockControls = installMockApi(getScene());
(window as typeof window & { __agenticoMock?: typeof mockControls }).__agenticoMock = mockControls;

createRoot(container).render(
  <React.StrictMode>
    <CaptureApp />
  </React.StrictMode>,
);
