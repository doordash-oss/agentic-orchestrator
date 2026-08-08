/**
 * Typed, redaction-safe errors shared by the main process, preload, and
 * renderer. Messages must never contain raw payloads, tokens, or absolute
 * user paths — anything crossing the IPC boundary goes through these helpers.
 */

export interface SafeError {
  /** Stable machine-readable code, e.g. `E_PAYLOAD_TOO_LARGE`. */
  code: string;
  /** Human-readable, redacted description. */
  message: string;
  /** Optional actionable next step for the user. */
  remediation?: string;
  details?: {
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
  };
}

export class SafeErrorException extends Error {
  readonly safe: SafeError;

  constructor(safe: SafeError) {
    super(`${safe.code}: ${safe.message}`);
    this.name = 'SafeErrorException';
    this.safe = safe;
  }
}

export function safeError(
  code: string,
  message: string,
  remediation?: string,
  details?: SafeError['details'],
): SafeError {
  return {
    code,
    message,
    ...(remediation === undefined ? {} : { remediation }),
    ...(details === undefined ? {} : { details }),
  };
}

/** A request outran its client-side bound; the server operation may still be running. */
export const E_REQUEST_TIMEOUT = 'E_REQUEST_TIMEOUT';

/**
 * The typed timeout error. Distinct from a failure: the mutation was accepted
 * and may still be completing server-side, so callers must reconcile rather
 * than retry.
 */
export function requestTimeoutError(): SafeError {
  return safeError(
    E_REQUEST_TIMEOUT,
    'The runtime did not answer within the request bound; the operation may still be running.',
    'Wait for the feature to refresh — retrying could repeat work that already succeeded.',
  );
}

export function isRequestTimeout(err: unknown): boolean {
  return err instanceof SafeErrorException && err.safe.code === E_REQUEST_TIMEOUT;
}

/** True for a fetch/DOM abort, whose raw message reads as an opaque failure. */
function isAbortError(err: unknown): boolean {
  return err instanceof Error && (err.name === 'AbortError' || err.name === 'TimeoutError');
}

const BEARER_RE = /bearer\s+[a-z0-9._~+/=-]+/gi;
const TOKEN_PARAM_RE = /([?&](?:token|access_token|bearer|key|secret)=)[^\s&"']+/gi;
const USER_PATH_RE = /(?:\/Users|\/home)\/[^\s:"']+/g;

/** Strips token material and absolute user paths from free-form text. */
export function redactText(text: string): string {
  return text
    .replace(BEARER_RE, '[redacted]')
    .replace(TOKEN_PARAM_RE, '$1[redacted]')
    .replace(USER_PATH_RE, '[path]');
}

/**
 * Converts an arbitrary thrown value into a SafeError. SafeErrorExceptions
 * pass through untouched; Error messages are redacted; anything else (which
 * could hold raw payload data) is replaced with a generic message.
 */
export function toSafeError(err: unknown, fallbackCode: string): SafeError {
  if (err instanceof SafeErrorException) {
    return err.safe;
  }
  if (isAbortError(err)) {
    return requestTimeoutError();
  }
  if (err instanceof Error) {
    return safeError(fallbackCode, redactText(err.message));
  }
  return safeError(fallbackCode, 'An unexpected error occurred.');
}
