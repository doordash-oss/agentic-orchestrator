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
 * Parses errors thrown by the preload IPC bridge into the one canonical
 * error shape.
 *
 * Preload rethrows every envelope error as a sentinel-prefixed canonical
 * message and attaches `code`, `remediation`, and the full `canonical`
 * object (attachments survive in the same world — tests and mocks — but not
 * across the context bridge, so the sentinel message is the authoritative
 * channel). An unparseable rejection degrades to the catalog's
 * E_IPC_UNREACHABLE canonical.
 */
import { CANONICAL_ERROR_MESSAGE_PREFIX, buildCanonicalError } from '../../../shared/errors';
import type { CanonicalError } from '../../../shared/api/parse';

export function parseIpcError(err: unknown): CanonicalError {
  const raw = err instanceof Error ? err.message : '';
  const attached =
    err instanceof Error
      ? (err as Error & { code?: unknown; remediation?: unknown; canonical?: unknown })
      : undefined;
  // Custom Error properties do not survive the context bridge, so preload
  // carries the canonical object in the message behind a sentinel prefix.
  // Attachment is still honored when present (same-world mocks, tests).
  const carried = parseCanonicalMessage(raw);
  const canonical = isCanonicalError(attached?.canonical)
    ? attached.canonical
    : (carried ?? undefined);
  if (canonical !== undefined) {
    return canonical;
  }
  return buildCanonicalError('E_IPC_UNREACHABLE');
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
