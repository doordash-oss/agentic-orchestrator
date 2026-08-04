import type { AttentionItem, FeatureSnapshot } from '../../../shared/ipc';

export type CyclePhase = 'idle' | 'loading' | 'ready' | 'dispatching' | 'error';

export function humanizeFreshness(freshness: string): string {
  switch (freshness) {
    case 'up_to_date':
      return 'Up to date';
    case 'behind':
      return 'Behind main';
    case 'unknown':
      return 'Unknown';
    default:
      return freshness.charAt(0).toUpperCase() + freshness.slice(1).replace(/_/g, ' ');
  }
}

export function CycleGateNotice({
  featureId,
  snapshot: _snapshot,
  attentionItems = [],
  onOpenGate,
}: {
  featureId: string;
  snapshot: FeatureSnapshot;
  attentionItems?: AttentionItem[];
  onOpenGate?: (featureId: string) => void;
}): React.ReactElement | null {
  const cycleGateItems = attentionItems.filter(
    (item): item is Extract<AttentionItem, { kind: 'gate' }> =>
      item.kind === 'gate' && item.cycleType !== undefined,
  );
  if (cycleGateItems.length === 0) return null;

  return (
    <div className="cycle-journey__gate" role="alert">
      <p className="cycle-journey__gate-heading">
        Waiting for your answers —{' '}
        {cycleGateItems.length > 0
          ? `${cycleGateItems.length} shared gate${cycleGateItems.length === 1 ? '' : 's'}`
          : 'shared gate'}{' '}
        across repositories
      </p>
      <p className="cycle-journey__gate-detail">
        A repository cycle is paused waiting for answers. Resolving this gate resumes the aggregate
        cycle without duplicating work.
      </p>
      {onOpenGate !== undefined ? (
        <button
          type="button"
          className="cycle-journey__action"
          onClick={() => onOpenGate(featureId)}
        >
          Open gate resolution
        </button>
      ) : null}
    </div>
  );
}

export function CycleFooter({
  onCancel,
  primaryLabel,
  primaryDisabled = false,
  busy = false,
  onPrimary,
}: {
  onCancel(): void;
  primaryLabel: string;
  primaryDisabled?: boolean;
  busy?: boolean;
  onPrimary(): void;
}): React.ReactElement {
  return (
    <footer className="cycle-modal__footer">
      <button type="button" className="cycle-modal__cancel" onClick={onCancel} disabled={busy}>
        Cancel
      </button>
      <button
        type="button"
        className="cycle-modal__primary"
        disabled={primaryDisabled || busy}
        onClick={onPrimary}
      >
        {primaryLabel}
      </button>
    </footer>
  );
}
