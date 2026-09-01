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
 * Parses errors thrown by the preload IPC bridge into the renderer's legacy
 * error shape, plus the canonical object when the main process carried one.
 *
 * Preload rethrows with a `CODE: text` message and attaches `code`,
 * `remediation`, and — for server-emitted canonical errors — the full
 * `canonical` object. Legacy consumers keep reading `code`/`message`/
 * `remediation`; migrated surfaces read `canonical` directly.
 */
import { CANONICAL_ERROR_MESSAGE_PREFIX } from '../../../shared/errors';
import type { CanonicalError } from '../../../shared/api/parse';

export interface WizardError {
  code: string;
  message: string;
  remediation?: string;
  /** The catalog-rendered server error, when the rejection was canonical. */
  canonical?: CanonicalError;
}

export function parseIpcError(err: unknown): WizardError {
  const raw = err instanceof Error ? err.message : '';
  const attached =
    err instanceof Error
      ? (err as Error & { code?: unknown; remediation?: unknown; canonical?: unknown })
      : undefined;
  const attachedCode =
    typeof attached?.code === 'string' && attached.code !== '' ? attached.code : undefined;
  const remediation =
    typeof attached?.remediation === 'string' && attached.remediation !== ''
      ? attached.remediation
      : undefined;
  // Custom Error properties do not survive the context bridge, so preload
  // carries the canonical object in the message behind a sentinel prefix.
  // Attachment is still honored when present (same-world mocks, tests).
  const carried = parseCanonicalMessage(raw);
  const canonical = isCanonicalError(attached?.canonical)
    ? attached.canonical
    : (carried ?? undefined);
  if (canonical !== undefined) {
    // The catalog owns the text: message is the summary and the remediation
    // is the catalog hint.
    return {
      code: canonical.code,
      message: canonical.summary,
      ...(canonical.remediation?.hint === undefined
        ? {}
        : { remediation: canonical.remediation.hint }),
      canonical,
    };
  }
  const attachedMessage =
    attachedCode === undefined
      ? undefined
      : raw
          .replace(new RegExp(`^${attachedCode.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}:\\s*`), '')
          .replace(
            remediation === undefined
              ? /$^/
              : new RegExp(`\\s*${remediation.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`),
            '',
          );
  const match = /^([A-Za-z0-9_]+):\s*([\s\S]*)$/.exec(raw);
  if (attachedCode !== undefined || (match !== null && match[2] !== undefined && match[2] !== '')) {
    return {
      code: attachedCode ?? match?.[1] ?? 'E_IPC',
      message: attachedMessage ?? match?.[2] ?? raw,
      ...(remediation === undefined ? {} : { remediation }),
    };
  }
  return {
    code: 'E_IPC',
    message: raw === '' ? 'The application core did not respond.' : raw,
  };
}

function isCanonicalError(value: unknown): value is CanonicalError {
  if (typeof value !== 'object' || value === null) return false;
  const candidate = value as Partial<CanonicalError>;
  return (
    typeof candidate.code === 'string' &&
    (candidate.class === 'blocking' ||
      candidate.class === 'needs_action' ||
      candidate.class === 'warning') &&
    typeof candidate.title === 'string' &&
    typeof candidate.summary === 'string'
  );
}

/** Recovers a canonical error carried in a sentinel-prefixed message. */
function parseCanonicalMessage(raw: string): CanonicalError | null {
  if (!raw.startsWith(CANONICAL_ERROR_MESSAGE_PREFIX)) return null;
  try {
    const parsed: unknown = JSON.parse(raw.slice(CANONICAL_ERROR_MESSAGE_PREFIX.length));
    return isCanonicalError(parsed) ? parsed : null;
  } catch {
    return null;
  }
}

/**
 * Adapts a parsed IPC error to the canonical shape for ErrorSurface. A
 * canonical server error passes through; a legacy transport error (E_*
 * codes) maps to a blocking error whose summary is the safe message.
 */
export function canonicalFromWizardError(error: WizardError): CanonicalError {
  if (error.canonical !== undefined) return error.canonical;
  return {
    code: error.code,
    class: 'blocking',
    title: 'Request failed',
    summary: error.message,
    ...(error.remediation === undefined ? {} : { remediation: { hint: error.remediation } }),
  };
}
