/**
 * The preload folds SafeErrors into `Error("CODE: message [remediation]")`.
 * This recovers the stable code so the wizard can react per failure kind
 * while only ever displaying the already-redacted text.
 */
export interface WizardError {
  code: string;
  message: string;
  remediation?: string;
  dirtyWorktrees?: Array<{
    repo?: string;
    path?: string;
    staged?: string[];
    unstaged?: string[];
    untracked?: string[];
    stagedTotal?: number;
    unstagedTotal?: number;
    untrackedTotal?: number;
  }>;
}

export function parseIpcError(err: unknown): WizardError {
  const raw = err instanceof Error ? err.message : '';
  const attached =
    err instanceof Error
      ? (err as Error & { code?: unknown; remediation?: unknown; details?: unknown })
      : undefined;
  const attachedCode =
    typeof attached?.code === 'string' && attached.code !== '' ? attached.code : undefined;
  const remediation =
    typeof attached?.remediation === 'string' && attached.remediation !== ''
      ? attached.remediation
      : undefined;
  const attachedMessage =
    attachedCode === undefined
      ? undefined
      : raw
          .replace(new RegExp(`^${attachedCode.replace(/[.*+?^${}()|[\\]\\]/g, '\\$&')}:\\s*`), '')
          .replace(
            remediation === undefined
              ? /$^/
              : new RegExp(`\\s*${remediation.replace(/[.*+?^${}()|[\\]\\]/g, '\\$&')}$`),
            '',
          );
  const match = /^([A-Za-z0-9_]+):\s*([\s\S]*)$/.exec(raw);
  if (attachedCode !== undefined || (match !== null && match[2] !== undefined && match[2] !== '')) {
    const details =
      err instanceof Error && 'details' in err
        ? (err as Error & { details?: { dirtyWorktrees?: WizardError['dirtyWorktrees'] } }).details
        : undefined;
    return {
      code: attachedCode ?? match?.[1] ?? 'E_IPC',
      message: attachedMessage ?? match?.[2] ?? raw,
      ...(remediation === undefined ? {} : { remediation }),
      ...(details?.dirtyWorktrees === undefined ? {} : { dirtyWorktrees: details.dirtyWorktrees }),
    };
  }
  return {
    code: 'E_IPC',
    message: raw === '' ? 'The application core did not respond.' : raw,
  };
}
