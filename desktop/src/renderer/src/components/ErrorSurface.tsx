/**
 * The canonical error presentation, shared by every surface that renders a
 * parsed CanonicalError. One header row — class icon, class label word, and
 * the stable catalog code as a quiet mono tag — above a fixed disclosure
 * ladder: caption, title, summary, remediation (hint plus the primary-action
 * slot), structured details, raw diagnostics.
 *
 * The `full` variant spreads the last two over two native disclosures; the
 * `compact` variant folds both behind one and tightens spacing for inline
 * use. Class drives the whole treatment at once — left rule, icon, label
 * word — so the surface never signals by color alone.
 */
import type { Ref } from 'react';
import type { CanonicalError } from '../../../shared/api/parse';
import { CircleAlertIcon, TriangleAlertIcon, WrenchIcon } from './icons';

export interface ErrorSurfaceAction {
  /** Enabled actions render as the primary button. */
  enabled: boolean;
  label: string;
  /** Shown as text instead of a button when the action is disabled. */
  disabledReason?: string;
}

export interface ErrorSurfaceProps {
  error: CanonicalError;
  variant?: 'full' | 'compact';
  /** Context caption, e.g. "Rebase was rejected". Rendered first, as a lead-in. */
  caption?: string;
  /**
   * Resolves a referenced remediation action ID against the feature's action
   * catalog. When omitted or when the error references no action, the slot
   * renders nothing.
   */
  resolveAction?: (actionId: string) => ErrorSurfaceAction | undefined;
  /** Invoked when the primary action button is clicked. */
  onAction?: (actionId: string) => void;
  /** Lands on the root div so a hosting surface can focus the banner. */
  rootRef?: Ref<HTMLDivElement>;
  /** Forwarded to the root div when a host focuses it programmatically. */
  rootTabIndex?: number;
}

type ErrorClass = CanonicalError['class'];

const CLASS_LABEL: Record<ErrorClass, string> = {
  blocking: 'Failed',
  needs_action: 'Needs your action',
  warning: 'Warning',
};

const CLASS_ROLE: Record<ErrorClass, 'alert' | 'status'> = {
  blocking: 'alert',
  needs_action: 'alert',
  warning: 'status',
};

const CLASS_ICON: Record<ErrorClass, typeof CircleAlertIcon> = {
  blocking: CircleAlertIcon,
  needs_action: WrenchIcon,
  warning: TriangleAlertIcon,
};

/** BEM modifier per class; the enum's underscore flattens to a hyphen. */
const CLASS_MODIFIER: Record<ErrorClass, string> = {
  blocking: 'error-surface--blocking',
  needs_action: 'error-surface--needs-action',
  warning: 'error-surface--warning',
};

type ErrorContext = NonNullable<CanonicalError['context']>;

type ShaField =
  | 'parent_anchor_sha'
  | 'expected_ref_sha'
  | 'child_head_sha'
  | 'candidate_sha'
  | 'merge_head'
  | 'observed_sha';

const SHA_FIELDS = [
  { field: 'parent_anchor_sha', label: 'Parent anchor' },
  { field: 'expected_ref_sha', label: 'Expected ref' },
  { field: 'child_head_sha', label: 'Child head' },
  { field: 'candidate_sha', label: 'Candidate' },
  { field: 'merge_head', label: 'Merge head' },
  { field: 'observed_sha', label: 'Observed' },
] as const satisfies ReadonlyArray<{ field: ShaField; label: string }>;

function contextHasContent(context: CanonicalError['context']): boolean {
  if (context == null) return false;
  if (context.repositories != null && context.repositories.length > 0) return true;
  if (context.phase != null) return true;
  if (context.setup_task != null) return true;
  const command = context.command;
  return command != null && (command.exit_code != null || (command.log_paths?.length ?? 0) > 0);
}

function FileGroup({ label, paths }: { label: string; paths: readonly string[] }) {
  return (
    <div className="error-surface__file-group">
      <span className="error-surface__file-list-label">{label}</span>
      <ul className="error-surface__file-list">
        {paths.map((path) => (
          <li key={path}>{path}</li>
        ))}
      </ul>
    </div>
  );
}

function StructuredDetails({ context }: { context: ErrorContext }) {
  const repos = context.repositories ?? [];
  return (
    <div className="error-surface__details-body">
      {repos.length > 0 && (
        <ul className="error-surface__repos">
          {repos.map((repo) => (
            <li className="error-surface__repo" key={repo.name}>
              <div className="error-surface__repo-head">
                <span className="error-surface__repo-name">{repo.name}</span>
                {repo.branch != null && (
                  <span className="error-surface__repo-branch">{repo.branch}</span>
                )}
                {repo.rebase_target != null && (
                  <span className="error-surface__repo-rebase-target">
                    onto {repo.rebase_target}
                  </span>
                )}
              </div>
              {repo.remote_only_commits != null && (
                <p className="error-surface__sha">
                  <span className="error-surface__sha-label">Remote-only commits</span>
                  <code className="error-surface__sha-value">{repo.remote_only_commits}</code>
                </p>
              )}
              {repo.dirty_files != null && repo.dirty_files.length > 0 && (
                <FileGroup label="Dirty files" paths={repo.dirty_files} />
              )}
              {repo.conflict_files != null && repo.conflict_files.length > 0 && (
                <FileGroup label="Conflict files" paths={repo.conflict_files} />
              )}
              {SHA_FIELDS.map(({ field, label }) => {
                const value: string | undefined = repo[field];
                return value != null ? (
                  <p className="error-surface__sha" key={field}>
                    <span className="error-surface__sha-label">{label}</span>
                    <code className="error-surface__sha-value">{value}</code>
                  </p>
                ) : null;
              })}
            </li>
          ))}
        </ul>
      )}
      {context.setup_task != null && (
        <div className="error-surface__setup-task">
          <p className="error-surface__setup-task-label">Task: {context.setup_task.label}</p>
          <p className="error-surface__setup-task-kind">{context.setup_task.kind}</p>
        </div>
      )}
      {context.phase != null && (
        <div className="error-surface__phase">
          <p className="error-surface__phase-name">Phase: {context.phase.name}</p>
          {context.phase.iteration != null && (
            <p className="error-surface__phase-iteration">Iteration {context.phase.iteration}</p>
          )}
        </div>
      )}
      {context.command != null && (
        <div className="error-surface__command">
          {context.command.exit_code != null && (
            <p className="error-surface__command-exit">Exit code: {context.command.exit_code}</p>
          )}
          {context.command.log_paths != null && context.command.log_paths.length > 0 && (
            <FileGroup label="Logs" paths={context.command.log_paths} />
          )}
        </div>
      )}
    </div>
  );
}

export function ErrorSurface({
  error,
  variant = 'full',
  caption,
  resolveAction,
  onAction,
  rootRef,
  rootTabIndex,
}: ErrorSurfaceProps) {
  const compact = variant === 'compact';
  const ClassIcon = CLASS_ICON[error.class];
  const remediation = error.remediation;
  const remediationHint = remediation?.hint;
  // Only the first referenced action drives the slot; later IDs exist for the
  // catalog's benefit, not the surface.
  const actionId = remediation?.actions?.[0];
  const action = actionId != null ? resolveAction?.(actionId) : undefined;
  const actionSlot =
    action != null && actionId != null && action.enabled ? (
      <button type="button" className="error-surface__action" onClick={() => onAction?.(actionId)}>
        {action.label}
      </button>
    ) : action != null && action.disabledReason != null ? (
      <span className="error-surface__action-reason">{action.disabledReason}</span>
    ) : null;
  const remediationHasContent =
    (remediationHint != null && remediationHint !== '') || actionSlot != null;
  const diagnostics =
    error.diagnostics != null && error.diagnostics !== '' ? error.diagnostics : null;
  const hasDetails = contextHasContent(error.context);
  // Ternary (not `&&`) so an absent block is strictly null; `false != null`
  // would otherwise render an empty disclosure.
  const detailsBody =
    hasDetails && error.context != null ? <StructuredDetails context={error.context} /> : null;

  return (
    <div
      ref={rootRef}
      role={CLASS_ROLE[error.class]}
      tabIndex={rootTabIndex}
      className={`error-surface error-surface--${variant} ${CLASS_MODIFIER[error.class]}`}
    >
      <div className="error-surface__header">
        <span className="error-surface__icon" data-icon={error.class}>
          <ClassIcon />
        </span>
        <span className="error-surface__label">{CLASS_LABEL[error.class]}</span>
        <code className="error-surface__code">{error.code}</code>
      </div>
      {caption != null && <p className="error-surface__caption">{caption}</p>}
      <p className="error-surface__title">{error.title}</p>
      <p className="error-surface__summary">{error.summary}</p>
      {remediationHasContent && (
        <div className="error-surface__remediation">
          {remediationHint != null && (
            <p className="error-surface__remediation-hint">{remediationHint}</p>
          )}
          {actionSlot}
        </div>
      )}
      {/* Explain-in-chat slot: a future in-panel action will render here. */}
      {compact ? (
        (detailsBody != null || diagnostics != null) && (
          <details className="error-surface__details error-surface__details--compact">
            <summary>More detail</summary>
            {detailsBody}
            {diagnostics != null && (
              <pre className="error-surface__diagnostics-pre">{diagnostics}</pre>
            )}
          </details>
        )
      ) : (
        <>
          {detailsBody != null && (
            <details className="error-surface__details">
              <summary>Details</summary>
              {detailsBody}
            </details>
          )}
          {diagnostics != null && (
            <details className="error-surface__diagnostics">
              <summary>Diagnostics</summary>
              <pre className="error-surface__diagnostics-pre">{diagnostics}</pre>
            </details>
          )}
        </>
      )}
    </div>
  );
}
