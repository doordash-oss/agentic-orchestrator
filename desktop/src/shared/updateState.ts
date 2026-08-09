import type { UpdateState } from './ipc';

export function canInstallInApp(update: UpdateState | null): boolean {
  return (
    update !== null &&
    update.packageFormat !== 'deb' &&
    update.signatureStatus === 'verified' &&
    (update.status === 'ready' || update.status === 'scheduled')
  );
}

export function hasActiveWork(update: UpdateState | null): boolean {
  return update?.activeWorkSummary !== undefined && update.activeWorkSummary.trim() !== '';
}

export function installWhenIdleLabel({
  scheduling,
  scheduled,
}: {
  scheduling: boolean;
  scheduled: boolean;
}): string {
  if (scheduling) return 'Scheduling…';
  if (scheduled) return 'Scheduled for Idle';
  return 'Install When Idle';
}
