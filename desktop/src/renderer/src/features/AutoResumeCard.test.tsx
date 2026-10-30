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

import { act, cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AutoResumeCard, formatCountdown } from './AutoResumeCard';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const START = Date.parse('2026-10-29T15:15:00Z');

const AUTO_RESUME = {
  provider: 'claude' as const,
  resetAt: '2026-10-29T18:10:00Z',
  resumeAt: '2026-10-29T18:12:00Z',
  scheduledAt: '2026-10-29T15:15:00Z',
};

describe('formatCountdown', () => {
  it('formats hours, minutes, and seconds with padding', () => {
    expect(formatCountdown((2 * 3600 + 5 * 60 + 9) * 1000)).toBe('2h 05m 09s');
    expect(formatCountdown((4 * 60 + 7) * 1000)).toBe('4m 07s');
    expect(formatCountdown(12_000)).toBe('12s');
    expect(formatCountdown(-5_000)).toBe('0s');
  });

  it('rounds partial seconds up so the countdown never shows 0s early', () => {
    expect(formatCountdown(400)).toBe('1s');
  });
});

describe('AutoResumeCard', () => {
  it('explains the limit and counts down to the resume', () => {
    vi.useFakeTimers();
    let now = START;
    render(
      <AutoResumeCard
        autoResume={AUTO_RESUME}
        cancelEnabled
        busy={false}
        onCancel={() => {}}
        now={() => now}
      />,
    );

    expect(screen.getByRole('region', { name: 'Usage-limit auto-resume' })).toBeTruthy();
    expect(screen.getByText('Claude usage limit reached')).toBeTruthy();
    expect(screen.getByText('2h 57m 00s')).toBeTruthy();

    act(() => {
      now += 61_000;
      vi.advanceTimersByTime(1000);
    });
    expect(screen.getByText('2h 55m 59s')).toBeTruthy();

    act(() => {
      now = Date.parse(AUTO_RESUME.resumeAt);
      vi.advanceTimersByTime(1000);
    });
    expect(screen.getByText('Resuming now…')).toBeTruthy();
  });

  it('names Codex when Codex reported the limit', () => {
    render(
      <AutoResumeCard
        autoResume={{ ...AUTO_RESUME, provider: 'codex' }}
        cancelEnabled
        busy={false}
        onCancel={() => {}}
        now={() => START}
      />,
    );
    expect(screen.getByText('Codex usage limit reached')).toBeTruthy();
  });

  it('cancels through the callback', async () => {
    const onCancel = vi.fn();
    render(
      <AutoResumeCard
        autoResume={AUTO_RESUME}
        cancelEnabled
        busy={false}
        onCancel={onCancel}
        now={() => START}
      />,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Cancel auto-resume' }));
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it('disables cancel with the server reason when the catalog disables it', () => {
    render(
      <AutoResumeCard
        autoResume={AUTO_RESUME}
        cancelEnabled={false}
        cancelDisabledReason="The parent has an active child."
        busy={false}
        onCancel={() => {}}
        now={() => START}
      />,
    );
    const cancel = screen.getByRole('button', { name: 'Cancel auto-resume' });
    expect((cancel as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('The parent has an active child.')).toBeTruthy();
  });
});
