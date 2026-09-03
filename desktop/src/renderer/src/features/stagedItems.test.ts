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

import { describe, expect, it } from 'vitest';
import type { StagedUpload } from '../../../shared/ipc';
import {
  failPendingUploads,
  isBlockingStagedItem,
  isStagedOnOtherServer,
  pendingUploadItems,
  reconcileUploadResults,
  submittableReferences,
  type ComposerUploadItem,
} from './stagedItems';

function upload(serverKey: string, reference = 'ref-1'): StagedUpload {
  return { reference, kind: 'image', name: 'a.png', size: 4, serverKey };
}

function readyItem(serverKey: string, reference = 'ref-1'): ComposerUploadItem {
  return {
    id: `id-${reference}`,
    kind: 'image',
    name: 'a.png',
    sourcePath: '/shots/a.png',
    state: 'ready',
    upload: upload(serverKey, reference),
  };
}

describe('stagedItems', () => {
  it('creates pending uploading chips with file display names', () => {
    const pending = pendingUploadItems('image', ['/shots/a.png', '/docs/spec.pdf']);
    expect(pending).toHaveLength(2);
    expect(pending[0]).toMatchObject({ kind: 'image', name: 'a.png', state: 'uploading' });
    expect(pending[1]?.sourcePath).toBe('/docs/spec.pdf');
  });

  it('reconciles per-file results in request order', () => {
    const pending = pendingUploadItems('image', ['/shots/a.png', '/shots/b.png']);
    const items = reconcileUploadResults(['other', ...pending] as ComposerUploadItem[], pending, [
      { ok: true, name: 'a.png', upload: upload('server-1', 'ref-a') },
      {
        ok: false,
        name: 'b.png',
        error: {
          code: 'request_too_large',
          class: 'needs_action',
          title: 'Request too large',
          summary: 'Too large.',
          remediation: { hint: 'Shrink it.' },
        },
      },
    ]);
    expect(items[0]).toBe('other');
    expect(items[1]).toMatchObject({ state: 'ready', upload: { reference: 'ref-a' } });
    expect(items[2]).toMatchObject({
      state: 'failed',
      message: 'Too large. Shrink it.',
    });
    // Identities survive the flip so chips do not remount.
    expect(items[1]?.id).toBe(pending[0]?.id);
  });

  it('renders the failed chip text without the remediation hint when none is authored', () => {
    const pending = pendingUploadItems('image', ['/shots/a.png']);
    const items = reconcileUploadResults(pending, pending, [
      {
        ok: false,
        name: 'a.png',
        error: {
          code: 'request_too_large',
          class: 'needs_action',
          title: 'Request too large',
          summary: 'Too large.',
        },
      },
    ]);
    expect(items[0]).toMatchObject({ state: 'failed', message: 'Too large.' });
  });

  it('marks pending items failed on a wholesale transport failure', () => {
    const pending = pendingUploadItems('image', ['/shots/a.png']);
    const items = failPendingUploads(pending, pending, 'E_NOT_CONNECTED: down');
    expect(items[0]).toMatchObject({ state: 'failed', message: 'E_NOT_CONNECTED: down' });
  });

  it('blocks anything but a ready item scoped to the connected server', () => {
    expect(isBlockingStagedItem(readyItem('server-1'), 'server-1')).toBe(false);
    expect(isBlockingStagedItem(readyItem('server-2'), 'server-1')).toBe(true);
    expect(isBlockingStagedItem(readyItem('server-2'), null)).toBe(true);
    expect(isBlockingStagedItem({ ...readyItem('server-1'), state: 'uploading' }, 'server-1')).toBe(
      true,
    );
    expect(isBlockingStagedItem({ ...readyItem('server-1'), state: 'failed' }, 'server-1')).toBe(
      true,
    );
  });

  it('flags only ready uploads produced by another server identity', () => {
    expect(isStagedOnOtherServer(readyItem('server-2'), 'server-1')).toBe(true);
    expect(isStagedOnOtherServer(readyItem('server-1'), 'server-1')).toBe(false);
    expect(
      isStagedOnOtherServer({ ...readyItem('server-2'), state: 'uploading' }, 'server-1'),
    ).toBe(false);
  });

  it('collects only same-server ready references for submission', () => {
    const items = [
      readyItem('server-1', 'ref-keep'),
      readyItem('server-2', 'ref-foreign'),
      { ...readyItem('server-1', 'ref-pending'), state: 'uploading' as const },
      { ...readyItem('server-1', 'ref-doc'), kind: 'attachment' as const },
    ];
    expect(submittableReferences(items, 'image', 'server-1')).toEqual(['ref-keep']);
    expect(submittableReferences(items, 'attachment', 'server-1')).toEqual(['ref-doc']);
  });
});
