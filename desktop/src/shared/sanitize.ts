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
 * Payload hygiene enforced at both the server-response and IPC boundaries.
 * Everything here fails closed with typed SafeErrorExceptions.
 */
import { SafeErrorException, safeError } from './errors';

/** Maximum accepted payload size at any trust boundary (5 MiB). */
export const MAX_PAYLOAD_BYTES = 5 * 1024 * 1024;

const FORBIDDEN_KEYS = new Set(['__proto__', 'constructor', 'prototype']);

/**
 * Rejects any value whose object tree carries `__proto__`, `constructor`, or
 * `prototype` as an own enumerable key (as produced by `JSON.parse` of a
 * malicious document). The error never echoes offending values.
 */
export function assertNoPrototypePollution(value: unknown): void {
  const stack: unknown[] = [value];
  while (stack.length > 0) {
    const current = stack.pop();
    if (current === null || typeof current !== 'object') {
      continue;
    }
    if (Array.isArray(current)) {
      for (const item of current) {
        stack.push(item);
      }
      continue;
    }
    // Object.keys misses an own `__proto__` data property created by
    // JSON.parse on some engines' fast paths, so enumerate own property
    // names and filter to enumerable ones explicitly.
    for (const key of Object.getOwnPropertyNames(current)) {
      const descriptor = Object.getOwnPropertyDescriptor(current, key);
      if (descriptor === undefined || !descriptor.enumerable) {
        continue;
      }
      if (FORBIDDEN_KEYS.has(key)) {
        throw new SafeErrorException(
          safeError(
            'E_UNSAFE_PAYLOAD',
            'Payload rejected: it contains a prototype-polluting key.',
            'This indicates a malicious or corrupted source; do not retry with the same data.',
          ),
        );
      }
      stack.push((current as Record<string, unknown>)[key]);
    }
  }
}

/** Rejects text payloads whose UTF-8 encoding exceeds `maxBytes`. */
export function assertWithinByteSize(text: string, maxBytes: number = MAX_PAYLOAD_BYTES): void {
  const bytes = new TextEncoder().encode(text).byteLength;
  if (bytes > maxBytes) {
    throw new SafeErrorException(
      safeError(
        'E_PAYLOAD_TOO_LARGE',
        `Payload rejected: ${bytes} bytes exceeds the ${maxBytes}-byte limit.`,
        'Reduce the requested data size or report this as a server bug.',
      ),
    );
  }
}
