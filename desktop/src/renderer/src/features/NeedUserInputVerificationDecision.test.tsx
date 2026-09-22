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

import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem, VerificationGateAction } from '../../../shared/ipc';
import {
  hasStructuredVerificationDecision,
  NeedUserInputVerificationDecision,
  verificationResumeLabel,
} from './NeedUserInputVerificationDecision';

afterEach(cleanup);

const gate: Extract<AttentionItem, { kind: 'gate' }> = {
  kind: 'gate',
  id: 'verification-gate-1',
  featureId: 'abcd1234ef567890',
  waitingSince: '2026-07-29T00:00:00Z',
  questions: [{ index: 1, prompt: 'How should Agentico continue?', answer: '' }],
  verification: {
    blockers: [
      {
        itemId: 'blocker-1',
        name: 'Deployment smoke test',
        repoName: 'repo-a',
        command: 'make deploy-smoke',
        reason: 'missing declared capability "Okta session"',
        capabilities: ['Okta session'],
        remediation: 'Make Okta session available, then retry verification.',
      },
    ],
    allowedActions: ['RETRY_AFTER_AUTH', 'WAIVE'],
  },
};

describe('NeedUserInputVerificationDecision', () => {
  it('presents blocked verification evidence and reports the selected action', async () => {
    const onSelect = vi.fn();
    const user = userEvent.setup();
    render(
      <NeedUserInputVerificationDecision
        item={gate}
        selectedAction=""
        idPrefix="verification-gate-1"
        onSelect={onSelect}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Deployment smoke test' })).toBeVisible();
    expect(screen.getByText('repo-a')).toBeVisible();
    expect(screen.getByText('make deploy-smoke')).toBeVisible();
    expect(screen.getByText('missing declared capability "Okta session"')).toBeVisible();
    expect(screen.getByText('Make Okta session available, then retry verification.')).toBeVisible();
    expect(
      screen.getByRole('radio', { name: /I've granted access — retry verification/ }),
    ).not.toBeChecked();
    expect(
      screen.getByRole('radio', { name: /Waive blocked checks and continue/ }),
    ).not.toBeChecked();

    await user.click(screen.getByRole('radio', { name: /retry verification/ }));
    expect(onSelect).toHaveBeenLastCalledWith('RETRY_AFTER_AUTH');
    await user.click(screen.getByRole('radio', { name: /Waive blocked checks/ }));
    expect(onSelect).toHaveBeenLastCalledWith('WAIVE');
    expect(
      screen.queryByRole('radio', { name: /Accept a faithful substitute/ }),
    ).not.toBeInTheDocument();
  });

  it('offers the substitute decision only when the server allows it', async () => {
    const onSelect = vi.fn();
    const user = userEvent.setup();
    render(
      <NeedUserInputVerificationDecision
        item={{
          ...gate,
          verification: {
            ...gate.verification!,
            allowedActions: ['RETRY_AFTER_AUTH', 'WAIVE', 'ALLOW_SUBSTITUTE'],
          },
        }}
        selectedAction=""
        idPrefix="verification-gate-1"
        onSelect={onSelect}
      />,
    );

    const substitute = screen.getByRole('radio', {
      name: /Accept a faithful substitute for the blocked evidence/,
    });
    expect(substitute).not.toBeChecked();
    expect(substitute.closest('label')).not.toHaveAttribute('data-tone');
    expect(
      screen.getByText(
        'The rows stay required; a local rendering or equivalent capture is accepted instead of the third-party surface.',
      ),
    ).toBeVisible();
    await user.click(substitute);
    expect(onSelect).toHaveBeenLastCalledWith('ALLOW_SUBSTITUTE');
  });

  it('recognizes only complete structured verification decisions', () => {
    expect(hasStructuredVerificationDecision(gate)).toBe(true);

    const withoutBlockers = {
      ...gate,
      verification: { ...gate.verification!, blockers: [] },
    };
    const withoutActions = {
      ...gate,
      verification: { ...gate.verification!, allowedActions: [] },
    };
    const withPartialActions = {
      ...gate,
      verification: {
        ...gate.verification!,
        allowedActions: ['WAIVE'] satisfies VerificationGateAction[],
      },
    };
    const withSubstitute = {
      ...gate,
      verification: {
        ...gate.verification!,
        allowedActions: [
          'WAIVE',
          'RETRY_AFTER_AUTH',
          'ALLOW_SUBSTITUTE',
        ] satisfies VerificationGateAction[],
      },
    };
    const multipleQuestions = {
      ...gate,
      questions: [...gate.questions, { index: 2, prompt: 'Another question?', answer: '' }],
    };

    expect(hasStructuredVerificationDecision(withoutBlockers)).toBe(false);
    expect(hasStructuredVerificationDecision(withoutActions)).toBe(false);
    expect(hasStructuredVerificationDecision(withPartialActions)).toBe(false);
    expect(hasStructuredVerificationDecision(withSubstitute)).toBe(true);
    expect(hasStructuredVerificationDecision(multipleQuestions)).toBe(false);
  });

  it('labels the resume verb after the selected decision', () => {
    expect(verificationResumeLabel('')).toBe('Retry verification');
    expect(verificationResumeLabel('RETRY_AFTER_AUTH')).toBe('Retry verification');
    expect(verificationResumeLabel('WAIVE')).toBe('Waive and resume');
    expect(verificationResumeLabel('ALLOW_SUBSTITUTE')).toBe('Accept substitute and resume');
  });
});
