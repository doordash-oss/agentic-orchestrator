/**
 * Main-process bulk resume/retry service. Composes a fresh authoritative
 * preview from per-feature action catalogues (no server-side bulk endpoint
 * needed — the preview is derived from the existing feature detail surface).
 * The renderer owns the sequential dispatch queue for direct UI state
 * updates; this service provides only the fresh server-authored preview.
 */
import { type BulkPreview, type BulkPreviewRow, type FeatureSnapshot } from '../shared/ipc';
import type { FeatureService } from './features';

export class BulkService {
  constructor(private readonly featureService: FeatureService) {}

  async preview(): Promise<BulkPreview> {
    const features = await this.featureService.listFeatures();
    const eligible: BulkPreviewRow[] = [];
    const excluded: BulkPreviewRow[] = [];

    for (const summary of features) {
      let snapshot: FeatureSnapshot | null = null;
      try {
        snapshot = await this.featureService.getFeature(summary.id);
      } catch {
        excluded.push({
          featureId: summary.id,
          featureName: summary.name,
          action: 'resume',
          enabled: false,
          disabledReason: 'Could not load feature detail from server.',
        });
        continue;
      }

      const resumeAction = snapshot.actions.find((a) => a.id === 'resume');
      const retryAction = snapshot.actions.find((a) => a.id === 'retry');

      if (resumeAction?.enabled === true) {
        eligible.push({
          featureId: summary.id,
          featureName: summary.name,
          action: 'resume',
          enabled: true,
          repos: summary.repos,
        });
      } else if (retryAction?.enabled === true) {
        eligible.push({
          featureId: summary.id,
          featureName: summary.name,
          action: 'retry',
          enabled: true,
          repos: summary.repos,
        });
      } else {
        const reason =
          resumeAction?.disabledReasons?.[0]?.message ??
          retryAction?.disabledReasons?.[0]?.message ??
          'No resume or retry action available.';
        const action = retryAction !== undefined ? 'retry' : 'resume';
        excluded.push({
          featureId: summary.id,
          featureName: summary.name,
          action,
          enabled: false,
          disabledReason: reason,
          repos: summary.repos,
        });
      }
    }

    const previewId = `bulk-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    return { previewId, eligible, excluded };
  }
}
