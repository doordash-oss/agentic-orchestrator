/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * Typed, redaction-safe errors shared by the main process, preload, and
 * renderer. Messages must never contain raw payloads, tokens, or absolute
 * user paths — anything crossing the IPC boundary goes through these helpers.
 */
import type { CanonicalError } from './api/parse';

export interface SafeError {
  /** Stable machine-readable code, e.g. `E_PAYLOAD_TOO_LARGE`. */
  code: string;
  /** Human-readable, redacted description. */
  message: string;
  /** Optional actionable next step for the user. */
  remediation?: string;
}

export class SafeErrorException extends Error {
  readonly safe: SafeError;

  constructor(safe: SafeError) {
    super(`${safe.code}: ${safe.message}`);
    this.name = 'SafeErrorException';
    this.safe = safe;
  }
}

/**
 * A server-emitted canonical error. The parsed catalog-rendered object
 * crosses IPC unchanged: the catalog owns the human text, so the main
 * process never re-wraps or re-redacts it.
 */
export class CanonicalErrorException extends Error {
  readonly canonical: CanonicalError;

  constructor(canonical: CanonicalError) {
    super(`${canonical.code}: ${canonical.title} — ${canonical.summary}`);
    this.name = 'CanonicalErrorException';
    this.canonical = canonical;
  }
}

export function safeError(code: string, message: string, remediation?: string): SafeError {
  return {
    code,
    message,
    ...(remediation === undefined ? {} : { remediation }),
  };
}

/**
 * The IPC envelope's error branch: a canonical server error passes through
 * unchanged; anything else degrades to the redacted safe-error shape.
 */
export function toEnvelopeError(err: unknown, fallbackCode: string): SafeError | CanonicalError {
  if (err instanceof CanonicalErrorException) {
    return err.canonical;
  }
  return toSafeError(err, fallbackCode);
}

/** A request outran its client-side bound; the server operation may still be running. */
export const E_REQUEST_TIMEOUT = 'E_REQUEST_TIMEOUT';

/**
 * The distinct locality refusal: local-filesystem work (pickers, clipboard
 * capture, repository file walks, local path submission) is meaningless
 * while the active connection targets a remote server. Main-process guards
 * throw this before touching the filesystem or the network; the renderer
 * surfaces the message verbatim.
 */
export const E_REQUIRES_LOCAL_SERVER = 'E_REQUIRES_LOCAL_SERVER';

export function requiresLocalServerError(): SafeError {
  return safeError(
    E_REQUIRES_LOCAL_SERVER,
    'This action requires a local server.',
    'Connect to a locally running Agentico server, then retry.',
  );
}

export function isRequiresLocalServerError(err: unknown): boolean {
  return err instanceof SafeErrorException && err.safe.code === E_REQUIRES_LOCAL_SERVER;
}

/**
 * Prefix marking an Error message that carries a full canonical error as
 * JSON. Custom Error properties do not survive the context bridge, so the
 * message itself is the only channel that reliably carries the canonical
 * object from preload to the renderer.
 */
export const CANONICAL_ERROR_MESSAGE_PREFIX = 'E_CANONICAL_ERROR ';

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
 * Removes exact secret occurrences from free-form text (split/join, never
 * regex). Used at the server-boundary: an untrusted server can echo a
 * presented bearer back in free-text fields like its display name, so every
 * server-controlled string that lands in IPC state or persisted settings
 * passes through this with the credential in scope.
 */
export function stripSecrets(text: string, secrets: readonly string[]): string {
  let out = text;
  for (const secret of secrets) {
    if (secret.length > 0) {
      out = out.split(secret).join('[redacted]');
    }
  }
  return out;
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
