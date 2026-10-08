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

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import type { SupervisorBackgroundTask } from '../../../../shared/ipc';
import { SupervisorBackgroundWork, backgroundWorkSummary } from './SupervisorBackgroundWork';
import { supervisorRowView } from '../WorkspaceShell';
import { supervisorState } from '../../test/agenticoMock';

afterEach(cleanup);
const monitor: SupervisorBackgroundTask = {
  id: '1:cron:watch',
  providerId: 'watch',
  generation: 1,
  kind: 'scheduled',
  title: 'Watch translation questions',
  state: 'watching',
  schedule: 'every minute',
  detail: 'Schedule armed; individual checks are not reported',
  startedAt: '2026-10-08T12:00:00Z',
  updatedAt: '2026-10-08T12:00:00Z',
  expiresAt: '',
};

it('shows confirmed background work at rest and waits for acknowledgement of a stop', () => {
  const onStop = vi.fn();
  const { rerender } = render(
    <SupervisorBackgroundWork tasks={[monitor]} onStop={onStop} stopDisabled={false} />,
  );
  const panel = screen.getByRole('region', { name: 'Background work' });
  expect(within(panel).getByRole('status')).toHaveTextContent('1 monitor watching');
  fireEvent.click(within(panel).getByText('Watch translation questions'));
  expect(within(panel).getByText('Schedule: every minute')).toBeVisible();
  expect(within(panel).getByText(/individual checks are not reported/)).toBeVisible();
  fireEvent.click(within(panel).getByRole('button', { name: 'Ask to stop' }));
  expect(onStop).toHaveBeenCalledWith(monitor);
  expect(within(panel).getByText('Watching', { exact: true })).toBeVisible();
  rerender(
    <SupervisorBackgroundWork
      tasks={[{ ...monitor, state: 'stopped', detail: 'Schedule cancelled' }]}
      onStop={onStop}
      stopDisabled={false}
    />,
  );
  expect(within(panel).queryByText('Watching', { exact: true })).toBeNull();
  fireEvent.click(within(panel).getByText('Recent activity (1)'));
  expect(within(panel).getByText('Stopped', { exact: true })).toBeVisible();
  expect(within(panel).queryByRole('button', { name: 'Ask to stop' })).toBeNull();
});

it('makes lost execution visible and cannot request a stop for a dead session', () => {
  const task = {
    ...monitor,
    state: 'interrupted' as const,
    detail: 'Session ended; execution is no longer confirmed',
  };
  render(<SupervisorBackgroundWork tasks={[task]} onStop={vi.fn()} stopDisabled={true} />);
  expect(screen.getByText('Interrupted', { exact: true })).toBeVisible();
  expect(screen.getByRole('status')).toHaveTextContent('1 task needs attention');
  expect(screen.queryByRole('button')).toBeNull();
  expect(backgroundWorkSummary([task])).not.toContain('watching');
});

it('advertises background work in the sidebar while the conversation is idle', () => {
  const status = {
    state: supervisorState({ lifecycle: 'idle', backgroundTasks: [monitor] }),
    lifecycle: 'idle' as const,
    unread: true,
    windowFocused: true,
  };
  expect(supervisorRowView(status)).toMatchObject({ subline: '1 monitor watching', unread: true });
  expect(
    supervisorRowView({
      ...status,
      state: { ...status.state, backgroundTasks: [{ ...monitor, state: 'interrupted' }] },
    }),
  ).toMatchObject({ subline: '1 task needs attention', tone: 'attention' });
});
