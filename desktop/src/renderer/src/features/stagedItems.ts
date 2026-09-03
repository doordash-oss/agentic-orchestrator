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
 * The renderer-side model for files staged for submission while connected to
 * a remote server. Local connections keep submitting plain paths (the
 * `images`/`attachments` string arrays); remote connections stage each file
 * through the upload channel and submit opaque single-use references.
 *
 * A staged item goes uploading → ready | failed. Every item carries the
 * identity of the server that accepted it: after a server switch, items
 * staged elsewhere stay visible with a "staged on another server" badge, are
 * excluded from the submission, and block it until removed.
 */
import type {
  CanonicalError,
  CreationFileKind,
  CreationFileUploadResult,
  StagedUpload,
} from '../../../shared/ipc';

export type UploadItemState = 'uploading' | 'ready' | 'failed';

export interface ComposerUploadItem {
  /** Renderer-local chip identity; survives state flips across retries. */
  id: string;
  kind: CreationFileKind;
  /** Display name (basename of the picked/dropped/pasted file). */
  name: string;
  /** The local source path; used for retry and dedupe, never submitted. */
  sourcePath: string;
  state: UploadItemState;
  /** Present once state is 'ready'. */
  upload?: StagedUpload;
  /** User-visible failure summary when state is 'failed'. */
  message?: string;
}

/** Chip badge for a staged item the current connection cannot consume. */
export const STAGED_ON_OTHER_SERVER = 'Staged on another server';

/** Footer/status explanation shown while foreign or in-flight items block submit. */
export const STAGED_ITEMS_BLOCK_SUBMIT =
  'Remove attachments that are uploading, failed, or staged on another server to submit.';

export function basenameOf(filePath: string): string {
  return filePath.split(/[\\/]/).at(-1) ?? filePath;
}

/** One pending chip per path, inserted while the upload request is in flight. */
export function pendingUploadItems(
  kind: CreationFileKind,
  paths: readonly string[],
): ComposerUploadItem[] {
  return paths.map((sourcePath) => ({
    id: crypto.randomUUID(),
    kind,
    name: basenameOf(sourcePath),
    sourcePath,
    state: 'uploading' as const,
  }));
}

/**
 * Applies a batch upload result to the pending items: successes become ready
 * items carrying the staged reference, failures become retryable failed
 * items. `results` are returned in request order matching `pending`.
 */
export function reconcileUploadResults(
  items: readonly ComposerUploadItem[],
  pending: readonly ComposerUploadItem[],
  results: ReadonlyArray<CreationFileUploadResult>,
): ComposerUploadItem[] {
  const outcomeById = new Map<string, ComposerUploadItem>();
  pending.forEach((item, index) => {
    const result = results[index];
    if (result === undefined) return;
    outcomeById.set(
      item.id,
      result.ok
        ? { ...item, state: 'ready', upload: result.upload }
        : {
            ...item,
            state: 'failed',
            message: uploadFailureText(result.error),
          },
    );
  });
  return items.map((item) => outcomeById.get(item.id) ?? item);
}

/** The failed chip's text: the canonical summary, with the remediation hint appended when authored. */
function uploadFailureText(error: CanonicalError): string {
  const hint = error.remediation?.hint;
  return hint === undefined || hint === '' ? error.summary : `${error.summary} ${hint}`;
}

/** Marks every pending item failed after a wholesale transport failure. */
export function failPendingUploads(
  items: readonly ComposerUploadItem[],
  pending: readonly ComposerUploadItem[],
  message: string,
): ComposerUploadItem[] {
  const pendingIds = new Set(pending.map((item) => item.id));
  return items.map((item) =>
    pendingIds.has(item.id) && item.state === 'uploading'
      ? { ...item, state: 'failed', message }
      : item,
  );
}

/**
 * True when the item cannot be consumed by the current connection: still
 * uploading, failed (until removed or retried), or staged on another server.
 */
export function isBlockingStagedItem(item: ComposerUploadItem, serverKey: string | null): boolean {
  if (item.state !== 'ready') return true;
  return item.upload === undefined || item.upload.serverKey !== serverKey;
}

/** True for a ready item produced by a different server identity. */
export function isStagedOnOtherServer(item: ComposerUploadItem, serverKey: string | null): boolean {
  return item.state === 'ready' && item.upload !== undefined && item.upload.serverKey !== serverKey;
}

/**
 * The submittable references of one kind: ready items scoped to the connected
 * server only. Callers must block submission while
 * `items.some((item) => isBlockingStagedItem(item, serverKey))`.
 */
export function submittableReferences(
  items: readonly ComposerUploadItem[],
  kind: CreationFileKind,
  serverKey: string | null,
): string[] {
  return items
    .filter(
      (item) =>
        item.kind === kind &&
        item.state === 'ready' &&
        item.upload !== undefined &&
        item.upload.serverKey === serverKey,
    )
    .map((item) => (item.upload as StagedUpload).reference);
}
