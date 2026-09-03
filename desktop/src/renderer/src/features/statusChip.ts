/**
 * The single cockpit status chip, derived from the snapshot's highest-severity
 * owned error: `<owner verb> — <class label>` for entries whose owner names a
 * verb (the active pass, setup, publishing), the bare class label for a
 * top-level run failure. The chip's accessible name carries the catalog title
 * and its click focuses the owning card through the error-card registry. A
 * snapshot with no owned errors renders no chip at all — the plain status
 * label stays.
 */
import type { ErrorReference, FeatureSnapshot, OwnedError } from '../../../shared/ipc';
import { ERROR_CLASS_LABELS } from '../../../shared/ipc';
import { highestSeverityError } from './featureView';
import { passActiveVerb } from './refactor/refactorPassModel';

export interface ErrorStatusChip {
  /** `<owner verb> — <class label>`, or the bare class label. */
  label: string;
  /** From the class: blocking reads danger, needs_action reads attention. */
  tone: 'attention' | 'danger';
  /** The catalog title, for the accessible name and tooltip. */
  title: string;
  /** The entry the chip reflects (always the highest-severity one). */
  entry: OwnedError;
}

/** The owner verb an entry's scope and key imply, if any. */
function chipVerb(snapshot: FeatureSnapshot, ref: ErrorReference): string | undefined {
  if (ref.scope === 'setup') return 'Setup';
  if (ref.scope === 'repository') return 'Publishing';
  const child = snapshot.activeChild;
  if ((ref.scope === 'run' || ref.scope === 'transaction') && child !== undefined) {
    if (ref.featureId === child.id) return passActiveVerb(child.kind);
  }
  return undefined;
}

/** The error chip for a snapshot, or undefined when it owns no errors. */
export function errorStatusChip(snapshot: FeatureSnapshot): ErrorStatusChip | undefined {
  const entry = highestSeverityError(snapshot.errors);
  if (entry === undefined) return undefined;
  const classLabel = ERROR_CLASS_LABELS[entry.error.class];
  const verb = chipVerb(snapshot, entry.ref);
  return {
    label: verb === undefined ? classLabel : `${verb} — ${classLabel}`,
    tone: entry.error.class === 'blocking' ? 'danger' : 'attention',
    title: entry.error.title,
    entry,
  };
}
