/**
 * The preload folds SafeErrors into `Error("CODE: message [remediation]")`.
 * This recovers the stable code so the wizard can react per failure kind
 * while only ever displaying the already-redacted text.
 */
export interface WizardError {
  code: string;
  message: string;
}

export function parseIpcError(err: unknown): WizardError {
  const raw = err instanceof Error ? err.message : '';
  const match = /^([A-Za-z0-9_]+):\s*([\s\S]*)$/.exec(raw);
  if (match !== null && match[2] !== undefined && match[2] !== '') {
    return { code: match[1] ?? 'E_IPC', message: match[2] };
  }
  return {
    code: 'E_IPC',
    message: raw === '' ? 'The application core did not respond.' : raw,
  };
}
