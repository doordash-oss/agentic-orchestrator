/**
 * Runtime readiness wizard, shown only while the runtime cannot run work at
 * all. Every step derives entirely from the latest authoritative readiness
 * snapshot (deriveWizardState); the only local state is transient
 * presentation (a refresh flag, announcements). Provider remediation is an
 * external flow: the server-supplied CLI command is shown for copying — the
 * app never runs provider auth itself and never sees provider credentials.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import type { ProviderReadiness, ReadinessSnapshot } from '../../../../shared/ipc';
import { useNarrowViewport } from '../../hooks';
import { deriveWizardState, type WizardStepId } from '../../wizard/deriveWizardState';
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { PhaseRailTrack } from '../../features/PhaseRailRow';
import { stepSegments } from '../../features/phaseRail';

const STEP_LABELS: Record<WizardStepId, string> = {
  providers: 'Providers',
  models: 'Models',
  ready: 'Ready',
};

export interface SetupWizardProps {
  snapshot: ReadinessSnapshot;
  /** Receives every fresh authoritative snapshot produced by an action. */
  onSnapshot(next: ReadinessSnapshot): void;
}

export function SetupWizard({ snapshot, onSnapshot }: SetupWizardProps) {
  const derived = deriveWizardState(snapshot);
  const narrow = useNarrowViewport();

  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<WizardError | null>(null);
  const [announcement, setAnnouncement] = useState('');
  const [helpCollapsed, setHelpCollapsed] = useState(false);
  const errorRef = useRef<HTMLDivElement | null>(null);

  // Presentation prefs only; corrupt or missing settings fall back to defaults.
  useEffect(() => {
    let alive = true;
    void window.agentico
      .getSettings()
      .then((settings) => {
        if (alive) setHelpCollapsed(settings.wizard.collapsedHelp);
      })
      .catch(() => {
        // Defaults stand — the wizard never depends on local preferences.
      });
    return () => {
      alive = false;
    };
  }, []);

  // Failed actions move focus to the error region for keyboard/AT users.
  useEffect(() => {
    if (error !== null) {
      errorRef.current?.focus();
    }
  }, [error]);

  const checkAgain = useCallback(() => {
    setRefreshing(true);
    setError(null);
    void window.agentico
      .refreshReadiness()
      .then((snapshot) => {
        onSnapshot(snapshot);
        setAnnouncement('Readiness rechecked against the runtime.');
      })
      .catch((err: unknown) => setError(parseIpcError(err)))
      .finally(() => setRefreshing(false));
  }, [onSnapshot]);

  const persistCollapsedHelp = useCallback((collapsedHelp: boolean) => {
    void window.agentico
      .getSettings()
      .then((settings) =>
        window.agentico.updateSettings({ wizard: { ...settings.wizard, collapsedHelp } }),
      )
      .catch(() => {
        // Never block setup on preference persistence.
      });
  }, []);

  const copyCommand = useCallback((command: string, providerName: string) => {
    const clipboard: Clipboard | undefined = navigator.clipboard;
    if (clipboard === undefined) {
      setAnnouncement('Copy is unavailable — select the command text manually.');
      return;
    }
    clipboard.writeText(command).then(
      () => setAnnouncement(`Copied the ${providerName} command.`),
      () => setAnnouncement('Copy failed — select the command text manually.'),
    );
  }, []);

  const toggleHelp = useCallback(() => {
    setHelpCollapsed((current) => {
      persistCollapsedHelp(!current);
      return !current;
    });
  }, [persistCollapsedHelp]);

  return (
    <section
      className="shell-card setup-wizard"
      aria-label="First-launch setup"
      {...(narrow ? { 'data-narrow': 'true' } : {})}
    >
      <header className="shell-card__identity">
        <h1 className="shell-card__title">Set up Agentico</h1>
        <button
          type="button"
          className="setup-wizard__help-toggle"
          aria-expanded={!helpCollapsed}
          onClick={toggleHelp}
        >
          {helpCollapsed ? 'Show help' : 'Hide help'}
        </button>
      </header>

      <PhaseRailTrack
        segments={stepSegments(
          derived.steps.map((id) => ({ id, label: STEP_LABELS[id] })),
          derived.activeIndex,
        )}
        tone={derived.configurationIssue !== null ? 'error' : 'progress'}
        label="Setup progress"
      />

      {derived.configurationIssue !== null ? (
        <div className="setup-wizard__banner" role="alert">
          <span className="setup-wizard__error-code">invalid_configuration</span>
          <p className="setup-wizard__banner-message">{derived.configurationIssue.message}</p>
          {derived.configurationIssue.remedy !== undefined ? (
            <code className="setup-wizard__code">{derived.configurationIssue.remedy}</code>
          ) : null}
        </div>
      ) : null}

      <p className="setup-wizard__announcement" role="status" aria-live="polite">
        {announcement}
      </p>

      {error !== null ? (
        <div
          ref={errorRef}
          tabIndex={-1}
          role="alert"
          className="setup-wizard__error"
          aria-label="Setup error"
        >
          <span className="setup-wizard__error-code">{error.code}</span>
          <p className="setup-wizard__error-message">{error.message}</p>
        </div>
      ) : null}

      {derived.activeStep === 'providers' ? (
        <ProvidersStep
          providers={snapshot.providers}
          helpCollapsed={helpCollapsed}
          refreshing={refreshing}
          onCheckAgain={checkAgain}
          onCopy={copyCommand}
        />
      ) : null}

      {derived.activeStep === 'models' ? (
        <ModelsStep
          snapshot={snapshot}
          helpCollapsed={helpCollapsed}
          refreshing={refreshing}
          onCheckAgain={checkAgain}
        />
      ) : null}

      {derived.activeStep === 'ready' ? (
        <div className="setup-step">
          <h2 className="setup-step__title">Almost there</h2>
          <p className="setup-step__help">
            Every setup gate is satisfied, but the runtime still reports itself unready. Fix the
            issue above, then check again.
          </p>
          <button
            type="button"
            className="setup-wizard__action"
            onClick={checkAgain}
            disabled={refreshing}
          >
            {refreshing ? 'Checking…' : 'Check again'}
          </button>
        </div>
      ) : null}
    </section>
  );
}

// --- Providers ----------------------------------------------------------------

interface ProvidersStepProps {
  providers: readonly ProviderReadiness[];
  helpCollapsed: boolean;
  refreshing: boolean;
  onCheckAgain(): void;
  onCopy(command: string, providerName: string): void;
}

function ProvidersStep({
  providers,
  helpCollapsed,
  refreshing,
  onCheckAgain,
  onCopy,
}: ProvidersStepProps) {
  return (
    <div className="setup-step" aria-labelledby="setup-step-providers">
      <h2 id="setup-step-providers" className="setup-step__title">
        Connect a provider
      </h2>
      {!helpCollapsed ? (
        <p className="setup-step__help">
          Agentico drives coding agents through provider CLIs installed on this machine. Install and
          sign in to at least one provider in your own terminal — authentication always happens in
          the provider&apos;s external flow — then check again.
        </p>
      ) : null}
      {providers.length === 0 ? (
        <p className="setup-step__empty">
          The runtime reported no providers. Check the runtime configuration, then check again.
        </p>
      ) : (
        <ul className="provider-list">
          {providers.map((provider) => (
            <ProviderRow key={provider.name} provider={provider} onCopy={onCopy} />
          ))}
        </ul>
      )}
      <button
        type="button"
        className="setup-wizard__action"
        onClick={onCheckAgain}
        disabled={refreshing}
      >
        {refreshing ? 'Checking…' : 'Check again'}
      </button>
    </div>
  );
}

interface ProviderRowProps {
  provider: ProviderReadiness;
  onCopy(command: string, providerName: string): void;
}

function ProviderRow({ provider, onCopy }: ProviderRowProps) {
  const issue = provider.issue;
  const remedy = issue?.remedy;
  const executable = provider.installed ? 'installed' : 'not found';
  const authentication = !provider.installed
    ? '—'
    : issue?.code === 'unauthenticated'
      ? 'not signed in'
      : provider.ready
        ? 'signed in'
        : '—';
  return (
    <li className="provider-row" data-ready={provider.ready}>
      <div className="provider-row__head">
        <span className="provider-row__name">{provider.name}</span>
        <span className="provider-row__state" data-ready={provider.ready}>
          <span aria-hidden="true">{provider.ready ? '●' : '✕'}</span>{' '}
          {provider.ready ? 'Ready' : 'Needs attention'}
        </span>
      </div>
      <dl className="provider-row__facts">
        <div className="provider-row__fact">
          <dt>Executable</dt>
          <dd>{executable}</dd>
        </div>
        <div className="provider-row__fact">
          <dt>Version</dt>
          <dd>{provider.version ?? '—'}</dd>
        </div>
        <div className="provider-row__fact">
          <dt>Authentication</dt>
          <dd>{authentication}</dd>
        </div>
      </dl>
      {issue !== undefined ? (
        <div className="provider-row__issue">
          <p className="provider-row__issue-message">{issue.message}</p>
          {remedy !== undefined ? (
            <p className="provider-row__remedy">
              <span className="setup-step__hint">Run in your terminal, then check again:</span>
              <code className="setup-wizard__code">{remedy}</code>
              <button
                type="button"
                className="provider-row__copy"
                aria-label={`Copy the ${provider.name} command`}
                onClick={() => onCopy(remedy, provider.name)}
              >
                Copy
              </button>
            </p>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

// --- Models ---------------------------------------------------------------------

interface StepWithSnapshotProps {
  snapshot: ReadinessSnapshot;
  helpCollapsed: boolean;
  refreshing: boolean;
  onCheckAgain(): void;
}

function ModelsStep({ snapshot, helpCollapsed, refreshing, onCheckAgain }: StepWithSnapshotProps) {
  const models = snapshot.models;
  return (
    <div className="setup-step" aria-labelledby="setup-step-models">
      <h2 id="setup-step-models" className="setup-step__title">
        Model availability
      </h2>
      {!helpCollapsed ? (
        <p className="setup-step__help">
          Models are discovered from your authenticated providers by the runtime. If none are
          available yet, finish the provider sign-in in your terminal and check again.
        </p>
      ) : null}
      {models.available && models.models !== undefined && models.models.length > 0 ? (
        <ul className="model-list">
          {models.models.map((model) => (
            <li key={model} className="model-list__item">
              {model}
            </li>
          ))}
        </ul>
      ) : (
        <p className="setup-step__empty">
          {models.issue?.message ?? 'No models are available yet.'}
        </p>
      )}
      {models.issue?.remedy !== undefined ? (
        <code className="setup-wizard__code">{models.issue.remedy}</code>
      ) : null}
      <button
        type="button"
        className="setup-wizard__action"
        onClick={onCheckAgain}
        disabled={refreshing}
      >
        {refreshing ? 'Checking…' : 'Check again'}
      </button>
    </div>
  );
}
