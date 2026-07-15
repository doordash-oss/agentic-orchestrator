/**
 * Mandatory first-launch readiness wizard. Every step derives entirely
 * from the latest authoritative readiness snapshot (deriveWizardState);
 * the only local state is transient presentation (busy flags, announcements,
 * a pending folder awaiting consent). Provider remediation is an external
 * flow: the server-supplied CLI command is shown for copying — the app never
 * runs provider auth itself and never sees provider credentials.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import type { ProviderReadiness, ReadinessSnapshot } from '../../../../shared/ipc';
import { useNarrowViewport } from '../../hooks';
import { deriveWizardState, type WizardStepId } from '../../wizard/deriveWizardState';
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { PhaseSpine } from '../PhaseSpine';
import { ConsentDialog } from './ConsentDialog';

const STEP_LABELS: Record<WizardStepId, string> = {
  providers: 'Providers',
  models: 'Models',
  workspace: 'Workspace',
  repository: 'Repository',
  ready: 'Ready',
};

type BusyKind = 'refresh' | 'pick' | 'init' | null;

export interface SetupWizardProps {
  snapshot: ReadinessSnapshot;
  /** Receives every fresh authoritative snapshot produced by an action. */
  onSnapshot(next: ReadinessSnapshot): void;
}

export function SetupWizard({ snapshot, onSnapshot }: SetupWizardProps) {
  const derived = deriveWizardState(snapshot);
  const narrow = useNarrowViewport();

  const [busy, setBusy] = useState<BusyKind>(null);
  const [error, setError] = useState<WizardError | null>(null);
  const [announcement, setAnnouncement] = useState('');
  /** Folder chosen for initialization; kept recoverable across rejection/failure. */
  const [pendingInitPath, setPendingInitPath] = useState<string | null>(null);
  const [consentOpen, setConsentOpen] = useState(false);
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

  const run = useCallback(async (kind: BusyKind, task: () => Promise<void>): Promise<boolean> => {
    setBusy(kind);
    setError(null);
    try {
      await task();
      return true;
    } catch (err) {
      setError(parseIpcError(err));
      return false;
    } finally {
      setBusy(null);
    }
  }, []);

  const persistWizardPrefs = useCallback(
    (patch: Partial<{ collapsedHelp: boolean; lastRepositoryPathHint: string | null }>) => {
      void window.agentico
        .getSettings()
        .then((settings) =>
          window.agentico.updateSettings({ wizard: { ...settings.wizard, ...patch } }),
        )
        .catch(() => {
          // Never block setup on preference persistence.
        });
    },
    [],
  );

  const checkAgain = useCallback(() => {
    void run('refresh', async () => {
      onSnapshot(await window.agentico.refreshReadiness());
      setAnnouncement('Readiness rechecked against the runtime.');
    });
  }, [onSnapshot, run]);

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

  const chooseWorkspaceFolder = useCallback(() => {
    void run('pick', async () => {
      const picked = await window.agentico.pickWorkspaceDirectory();
      if (picked.path === null) {
        setAnnouncement('Folder selection cancelled.');
        return;
      }
      onSnapshot(await window.agentico.addWorkspaceRoot(picked.path));
      setAnnouncement('Workspace folder added.');
    });
  }, [onSnapshot, run]);

  const chooseRepositoryFolder = useCallback(() => {
    void run('pick', async () => {
      const picked = await window.agentico.pickWorkspaceDirectory();
      if (picked.path === null) {
        setAnnouncement('Folder selection cancelled.');
        return;
      }
      const known = snapshot.repositories.find(
        (repository) => repository.path === picked.path && repository.valid,
      );
      if (known !== undefined) {
        setAnnouncement(`${known.name} is already an available repository.`);
        persistWizardPrefs({ lastRepositoryPathHint: picked.path });
        return;
      }
      setPendingInitPath(picked.path);
      setConsentOpen(true);
    });
  }, [onSnapshot, persistWizardPrefs, run, snapshot.repositories]);

  const confirmInit = useCallback(() => {
    const path = pendingInitPath;
    if (path === null) {
      return;
    }
    void run('init', async () => {
      const next = await window.agentico.initRepository({ path, consent: true });
      setPendingInitPath(null);
      onSnapshot(next);
      setAnnouncement('Repository initialized and discovered.');
      persistWizardPrefs({ lastRepositoryPathHint: path });
    }).then(() => {
      // Close on success and on failure alike; a failure keeps the chosen
      // folder recoverable with the safe reason shown in the error region.
      setConsentOpen(false);
    });
  }, [onSnapshot, pendingInitPath, persistWizardPrefs, run]);

  const cancelConsent = useCallback(() => {
    setConsentOpen(false);
    setAnnouncement('Initialization cancelled. The folder choice is kept below.');
  }, []);

  const toggleHelp = useCallback(() => {
    setHelpCollapsed((current) => {
      persistWizardPrefs({ collapsedHelp: !current });
      return !current;
    });
  }, [persistWizardPrefs]);

  const stages = derived.steps.map((id) => ({ id, label: STEP_LABELS[id] }));

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

      <PhaseSpine
        stages={stages}
        activeIndex={derived.activeIndex}
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
          busy={busy}
          onCheckAgain={checkAgain}
          onCopy={copyCommand}
        />
      ) : null}

      {derived.activeStep === 'models' ? (
        <ModelsStep
          snapshot={snapshot}
          helpCollapsed={helpCollapsed}
          busy={busy}
          onCheckAgain={checkAgain}
        />
      ) : null}

      {derived.activeStep === 'workspace' ? (
        <WorkspaceStep
          snapshot={snapshot}
          helpCollapsed={helpCollapsed}
          busy={busy}
          onChooseFolder={chooseWorkspaceFolder}
        />
      ) : null}

      {derived.activeStep === 'repository' ? (
        <RepositoryStep
          snapshot={snapshot}
          helpCollapsed={helpCollapsed}
          busy={busy}
          pendingInitPath={pendingInitPath}
          onChooseFolder={chooseRepositoryFolder}
          onReopenConsent={() => setConsentOpen(true)}
          onDiscardPending={() => {
            setPendingInitPath(null);
            setAnnouncement('Folder choice discarded.');
          }}
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
            disabled={busy !== null}
          >
            {busy === 'refresh' ? 'Checking…' : 'Check again'}
          </button>
        </div>
      ) : null}

      {consentOpen && pendingInitPath !== null ? (
        <ConsentDialog
          path={pendingInitPath}
          busy={busy === 'init'}
          onConfirm={confirmInit}
          onCancel={cancelConsent}
        />
      ) : null}
    </section>
  );
}

// --- Providers ----------------------------------------------------------------

interface ProvidersStepProps {
  providers: readonly ProviderReadiness[];
  helpCollapsed: boolean;
  busy: BusyKind;
  onCheckAgain(): void;
  onCopy(command: string, providerName: string): void;
}

function ProvidersStep({
  providers,
  helpCollapsed,
  busy,
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
        disabled={busy !== null}
      >
        {busy === 'refresh' ? 'Checking…' : 'Check again'}
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
  busy: BusyKind;
  onCheckAgain(): void;
}

function ModelsStep({ snapshot, helpCollapsed, busy, onCheckAgain }: StepWithSnapshotProps) {
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
        disabled={busy !== null}
      >
        {busy === 'refresh' ? 'Checking…' : 'Check again'}
      </button>
    </div>
  );
}

// --- Workspace --------------------------------------------------------------------

interface WorkspaceStepProps {
  snapshot: ReadinessSnapshot;
  helpCollapsed: boolean;
  busy: BusyKind;
  onChooseFolder(): void;
}

function WorkspaceStep({ snapshot, helpCollapsed, busy, onChooseFolder }: WorkspaceStepProps) {
  return (
    <div className="setup-step" aria-labelledby="setup-step-workspace">
      <h2 id="setup-step-workspace" className="setup-step__title">
        Choose a workspace
      </h2>
      {!helpCollapsed ? (
        <p className="setup-step__help">
          A workspace root is the folder where the runtime discovers your repositories. The choice
          is stored in the runtime configuration on the server side, never locally.
        </p>
      ) : null}
      {snapshot.workspaceRoots.length === 0 ? (
        <p className="setup-step__empty">No workspace folder is configured yet.</p>
      ) : (
        <ul className="path-list">
          {snapshot.workspaceRoots.map((root) => (
            <li key={root.path} className="path-list__item" data-valid={root.valid}>
              <span className="path-list__state" data-valid={root.valid}>
                <span aria-hidden="true">{root.valid ? '●' : '✕'}</span>{' '}
                {root.valid ? 'valid' : 'invalid'}
              </span>
              <code className="path-list__path">{root.path}</code>
              {root.issue !== undefined ? (
                <span className="path-list__issue">{root.issue.message}</span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      <button
        type="button"
        className="setup-wizard__action"
        onClick={onChooseFolder}
        disabled={busy !== null}
      >
        {busy === 'pick' ? 'Waiting for folder…' : 'Choose workspace folder…'}
      </button>
    </div>
  );
}

// --- Repository ----------------------------------------------------------------------

interface RepositoryStepProps {
  snapshot: ReadinessSnapshot;
  helpCollapsed: boolean;
  busy: BusyKind;
  pendingInitPath: string | null;
  onChooseFolder(): void;
  onReopenConsent(): void;
  onDiscardPending(): void;
}

function RepositoryStep({
  snapshot,
  helpCollapsed,
  busy,
  pendingInitPath,
  onChooseFolder,
  onReopenConsent,
  onDiscardPending,
}: RepositoryStepProps) {
  return (
    <div className="setup-step" aria-labelledby="setup-step-repository">
      <h2 id="setup-step-repository" className="setup-step__title">
        Pick a repository
      </h2>
      {!helpCollapsed ? (
        <p className="setup-step__help">
          Repositories are discovered inside your workspace by the runtime. Pick an existing git
          repository, or choose a plain folder and Agentico will offer to initialize it — with your
          explicit consent, on the server side.
        </p>
      ) : null}
      {snapshot.repositories.length === 0 ? (
        <p className="setup-step__empty">No repositories were discovered in the workspace yet.</p>
      ) : (
        <ul className="path-list">
          {snapshot.repositories.map((repository) => (
            <li key={repository.path} className="path-list__item" data-valid={repository.valid}>
              <span className="path-list__state" data-valid={repository.valid}>
                <span aria-hidden="true">{repository.valid ? '●' : '✕'}</span>{' '}
                {repository.valid ? 'valid' : 'invalid'}
              </span>
              <span className="path-list__name">{repository.name}</span>
              <code className="path-list__path">{repository.path}</code>
              {repository.issue !== undefined ? (
                <span className="path-list__issue">{repository.issue.message}</span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {pendingInitPath !== null ? (
        <div className="setup-step__pending">
          <p className="setup-step__pending-label">Chosen folder (not initialized yet):</p>
          <code className="path-list__path">{pendingInitPath}</code>
          <div className="setup-step__pending-actions">
            <button
              type="button"
              className="setup-wizard__action"
              onClick={onReopenConsent}
              disabled={busy !== null}
            >
              Initialize this folder…
            </button>
            <button
              type="button"
              className="setup-wizard__action"
              onClick={onDiscardPending}
              disabled={busy !== null}
            >
              Discard choice
            </button>
          </div>
        </div>
      ) : null}
      <button
        type="button"
        className="setup-wizard__action"
        onClick={onChooseFolder}
        disabled={busy !== null}
      >
        {busy === 'pick' ? 'Waiting for folder…' : 'Choose repository folder…'}
      </button>
    </div>
  );
}
