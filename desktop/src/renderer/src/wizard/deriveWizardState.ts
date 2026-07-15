/**
 * Pure projection from the authoritative server readiness snapshot to the
 * wizard's presentation state. There is deliberately no local "completed
 * step" memory anywhere: reload, reconnect, and cancellation all re-derive
 * the current step from the latest snapshot, so the wizard can never drift
 * from the server or trust stale renderer state.
 */
import type { ReadinessIssue, ReadinessIssueCode, ReadinessSnapshot } from '../../../shared/ipc';

export const WIZARD_STEPS = ['providers', 'models', 'workspace', 'repository', 'ready'] as const;

export type WizardStepId = (typeof WIZARD_STEPS)[number];

/** The mandatory gates, in wizard order. */
export type WizardGateId = Exclude<WizardStepId, 'ready'>;

const GATE_ORDER: readonly WizardGateId[] = ['providers', 'models', 'workspace', 'repository'];

/** Which flattened issue codes belong to which step's blocker list. */
const ISSUE_STEP: Record<ReadinessIssueCode, WizardStepId | null> = {
  missing_executable: 'providers',
  unsupported_version: 'providers',
  unauthenticated: 'providers',
  models_unavailable: 'models',
  invalid_workspace_root: 'workspace',
  invalid_repository: 'repository',
  // Cross-cutting: reported via configurationIssue, not a step blocker.
  invalid_configuration: null,
};

export interface WizardState {
  steps: readonly WizardStepId[];
  /** The first unsatisfied gate, or 'ready' when all gates pass. */
  activeStep: WizardStepId;
  activeIndex: number;
  /** True only when every mandatory gate AND the server's own ready flag pass. */
  complete: boolean;
  gates: Record<WizardGateId, boolean>;
  /** Invalid runtime configuration blocks everything; shown as a banner. */
  configurationIssue: ReadinessIssue | null;
  /** Outstanding issues that belong to the active step. */
  blockers: readonly ReadinessIssue[];
}

export function deriveWizardState(snapshot: ReadinessSnapshot): WizardState {
  const gates: Record<WizardGateId, boolean> = {
    providers: snapshot.providers.some((provider) => provider.ready),
    models: snapshot.models.available,
    workspace: snapshot.workspaceRoots.some((root) => root.valid),
    repository: snapshot.repositories.some((repository) => repository.valid),
  };

  const firstBlocked = GATE_ORDER.find((gate) => !gates[gate]);
  const activeStep: WizardStepId = firstBlocked ?? 'ready';
  const activeIndex = WIZARD_STEPS.indexOf(activeStep);

  const configurationIssue = snapshot.configuration.valid
    ? null
    : (snapshot.configuration.issue ?? {
        code: 'invalid_configuration' as const,
        message: 'The runtime configuration is unusable.',
      });

  // Completion is server-authoritative: the snapshot's own ready flag must
  // agree (it covers providers, models, and configuration), plus the
  // workspace/repository gates the desktop wizard is responsible for.
  const complete =
    snapshot.ready && configurationIssue === null && GATE_ORDER.every((gate) => gates[gate]);

  const blockers = snapshot.issues.filter((issue) => ISSUE_STEP[issue.code] === activeStep);

  return {
    steps: WIZARD_STEPS,
    activeStep,
    activeIndex,
    complete,
    gates,
    configurationIssue,
    blockers,
  };
}
