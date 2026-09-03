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

import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ReviewSession } from '../../../shared/ipc';
import { installAgenticoMock, ipcError } from '../test/agenticoMock';
import { ReviewSurface } from './ReviewSurface';

const mockEditor = {
  dispose: vi.fn(),
  onDidChangeModelContent: vi.fn(),
  getValue: vi.fn(() => ''),
  setValue: vi.fn(),
};

vi.mock('monaco-editor', () => ({
  editor: {
    create: vi.fn(() => mockEditor),
    createModel: vi.fn(() => ({ dispose: vi.fn() })),
    createDiffEditor: vi.fn(() => ({ dispose: vi.fn(), setModel: vi.fn() })),
    setTheme: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const FEATURE_ID = 'abcd1234ef567890';
const session: ReviewSession = {
  featureId: FEATURE_ID,
  reviewId: 'phase-plan',
  reviewMode: 'phase_plan',
  targetPhase: 'Plan',
  runNumber: 5,
  artifactId: 'phase-plan.md',
  text: '# Current plan',
  draftRevision: 'revision-current',
  sourceRevision: 'source-current',
  canIterate: true,
};

interface MockApi {
  openReview: ReturnType<typeof vi.fn>;
  validateReview: ReturnType<typeof vi.fn>;
  loadLocalReviewDraft: ReturnType<typeof vi.fn>;
  readReview: ReturnType<typeof vi.fn>;
  discardLocalReviewDraft: ReturnType<typeof vi.fn>;
  saveLocalReviewDraft: ReturnType<typeof vi.fn>;
  saveReview: ReturnType<typeof vi.fn>;
  decideReview: ReturnType<typeof vi.fn>;
  getSettings: ReturnType<typeof vi.fn>;
}

function installMocks(
  overrides?: Partial<{
    localDraft: unknown;
    validation: unknown;
  }>,
): MockApi {
  const mock = installAgenticoMock();
  const api = mock.api as unknown as MockApi;
  api.openReview.mockResolvedValue(session);
  api.validateReview.mockResolvedValue(
    overrides?.validation ?? {
      applicable: true,
      valid: true,
      revision: session.draftRevision,
      findings: [],
    },
  );
  api.loadLocalReviewDraft.mockResolvedValue(overrides?.localDraft ?? null);
  api.readReview.mockResolvedValue(session);
  return api;
}

describe('ReviewSurface recovery and containment', () => {
  it('restores a matching local draft with an honest recovery label and can discard it', async () => {
    const api = installMocks({
      localDraft: {
        runtimeId: 'default-runtime',
        featureId: FEATURE_ID,
        reviewId: session.reviewId,
        baseDraftRevision: session.draftRevision,
        text: '# Recovered plan',
        savedAt: '2026-07-16T00:00:00.000Z',
      },
    });
    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);
    expect(await screen.findByText('Recovered unsaved draft')).toBeVisible();

    await userEvent.setup().click(screen.getByRole('button', { name: 'Discard recovered draft' }));
    expect(api.discardLocalReviewDraft).toHaveBeenCalledWith({
      runtimeId: 'default-runtime',
      featureId: FEATURE_ID,
      reviewId: session.reviewId,
      baseDraftRevision: session.draftRevision,
    });
    // After discard the state label flips to "Saved draft".
    expect(await screen.findByText('Saved draft')).toBeVisible();
  });

  it('shows a compare-to-server action on the recovered draft', async () => {
    installMocks({
      localDraft: {
        runtimeId: 'default-runtime',
        featureId: FEATURE_ID,
        reviewId: session.reviewId,
        baseDraftRevision: session.draftRevision,
        text: '# Recovered plan',
        savedAt: '2026-07-16T00:00:00.000Z',
      },
    });
    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);
    expect(await screen.findByText('Recovered unsaved draft')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Compare to server' })).toBeVisible();
  });

  it('renders sanitized Markdown structure in preview mode', async () => {
    installMocks({
      localDraft: {
        runtimeId: 'default-runtime',
        featureId: FEATURE_ID,
        reviewId: session.reviewId,
        baseDraftRevision: session.draftRevision,
        text: '# Plan\n\n## Overview\n\n- Item one\n- Item two\n\n```js\nalert(1)\n```\n\n[unsafe](javascript:alert(1))\n<script>alert(1)</script>',
        savedAt: '2026-07-16T00:00:00.000Z',
      },
    });
    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);
    await screen.findByText('Recovered unsaved draft');

    await userEvent.setup().click(screen.getByRole('button', { name: 'Preview' }));
    const preview = screen.getByLabelText('Sanitized Markdown preview');
    // Headings render as real elements.
    expect(preview.querySelector('h1')).not.toBeNull();
    expect(preview.querySelector('h2')).not.toBeNull();
    // Lists render as real elements.
    expect(preview.querySelector('ul')).not.toBeNull();
    // Code blocks render as real elements.
    expect(preview.querySelector('pre > code')).not.toBeNull();
    // Scripts and unsafe links are inert.
    expect(preview.querySelector('script')).toBeNull();
    expect(preview.querySelector('a')).toBeNull();
  });

  it('opens explicit reconciliation when a recovered base revision is stale', async () => {
    installMocks({
      localDraft: {
        runtimeId: 'default-runtime',
        featureId: FEATURE_ID,
        reviewId: session.reviewId,
        baseDraftRevision: 'revision-old',
        text: '# My local change',
        savedAt: '2026-07-16T00:00:00.000Z',
      },
    });
    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);

    expect(
      await screen.findByRole('region', { name: 'Reconcile stale review draft' }),
    ).toBeVisible();
    expect(screen.getByRole('button', { name: 'Take server draft' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Keep editing mine' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Replace server with mine' })).toBeVisible();
    // Base is unavailable for recovered-stale drafts — show honest note, not wrong text.
    expect(screen.getByText(/base draft is unavailable/)).toBeVisible();
  });

  it('take-server resolution discards the local draft with the correct 4-field key', async () => {
    const api = installMocks({
      localDraft: {
        runtimeId: 'default-runtime',
        featureId: FEATURE_ID,
        reviewId: session.reviewId,
        baseDraftRevision: 'revision-old',
        text: '# My local change',
        savedAt: '2026-07-16T00:00:00.000Z',
      },
    });
    api.discardLocalReviewDraft.mockResolvedValue(true);
    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);

    await screen.findByRole('region', { name: 'Reconcile stale review draft' });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Take server draft' }));

    // The discard call must carry exactly the 4 key fields — no text or savedAt.
    expect(api.discardLocalReviewDraft).toHaveBeenCalledWith({
      runtimeId: 'default-runtime',
      featureId: FEATURE_ID,
      reviewId: session.reviewId,
      baseDraftRevision: 'revision-old',
    });
    expect(await screen.findByText('Using the current server draft.')).toBeVisible();
  });

  it('shows a disabled-reason when Approve is blocked by validation failure', async () => {
    installMocks({
      validation: {
        applicable: true,
        valid: false,
        revision: session.draftRevision,
        findings: [
          { code: 'missing_title', message: 'Phase plans need a top-level Markdown title.' },
        ],
      },
    });
    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);
    await screen.findByLabelText('Review editor');
    expect(await screen.findByText('Fix validation findings before approving.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Approve' })).toBeDisabled();
    // Findings render as FieldError elements in the labelled findings list.
    const findings = screen.getByLabelText('Validation findings');
    const fieldError = within(findings).getByText('Phase plans need a top-level Markdown title.');
    expect(fieldError).toHaveClass('field-error');
    expect(fieldError).toHaveAttribute('id', 'review-finding-missing_title');
    expect(document.querySelector('.review-surface__findings')).toBeNull();
  });

  it('completes the save and warns when the local draft discard fails', async () => {
    const api = installMocks({
      localDraft: {
        runtimeId: 'default-runtime',
        featureId: FEATURE_ID,
        reviewId: session.reviewId,
        baseDraftRevision: session.draftRevision,
        text: '# Recovered plan',
        savedAt: '2026-07-16T00:00:00.000Z',
      },
    });
    api.saveReview.mockResolvedValue({
      type: 'saved',
      session: { ...session, text: '# Recovered plan' },
    });
    api.discardLocalReviewDraft.mockRejectedValue(new Error('disk full'));

    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);
    await screen.findByText('Recovered unsaved draft');

    await userEvent.setup().click(screen.getByRole('button', { name: 'Save draft' }));

    expect(api.saveReview).toHaveBeenCalledWith({
      featureId: FEATURE_ID,
      reviewId: session.reviewId,
      baseRevision: session.draftRevision,
      text: '# Recovered plan',
    });
    // A local recovery-copy failure renders as a compact ErrorSurface with
    // the old lead as its caption.
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(
      within(alert).getByText('Saved, but the local recovery copy could not be removed.'),
    ).toHaveClass('error-surface__caption');
    expect(within(alert).getByText('E_INTERNAL')).toHaveClass('error-surface__code');
  });

  it('renders "Saving…" as a status line and a failed save as a compact ErrorSurface', async () => {
    const api = installMocks({
      localDraft: {
        runtimeId: 'default-runtime',
        featureId: FEATURE_ID,
        reviewId: session.reviewId,
        baseDraftRevision: session.draftRevision,
        text: '# Recovered plan',
        savedAt: '2026-07-16T00:00:00.000Z',
      },
    });
    let rejectSave!: (reason: Error) => void;
    api.saveReview.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectSave = reject;
        }),
    );
    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);
    await screen.findByText('Recovered unsaved draft');

    await userEvent.setup().click(screen.getByRole('button', { name: 'Save draft' }));
    // While the request is in flight the notice slot is a plain status line.
    expect(screen.getByRole('status')).toHaveTextContent('Saving draft…');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();

    rejectSave(ipcError('E_INTERNAL', 'the server refused the draft'));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(alert).getByText('Save failed')).toHaveClass('error-surface__caption');
    expect(within(alert).getByText('E_INTERNAL')).toHaveClass('error-surface__code');
    expect(within(alert).getByText('the server refused the draft')).toBeVisible();
    expect(screen.queryByText('Saving draft…')).not.toBeInTheDocument();
  });

  it('shows a disabled-reason when Iterate is blocked by a dirty draft', async () => {
    installMocks({
      localDraft: {
        runtimeId: 'default-runtime',
        featureId: FEATURE_ID,
        reviewId: session.reviewId,
        baseDraftRevision: session.draftRevision,
        text: '# Recovered plan',
        savedAt: '2026-07-16T00:00:00.000Z',
      },
    });
    render(<ReviewSurface featureId={FEATURE_ID} onResolved={() => Promise.resolve()} />);
    await screen.findByText('Recovered unsaved draft');
    expect(screen.getByText('Save the draft before iterating.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Iterate' })).toBeDisabled();
  });
});
