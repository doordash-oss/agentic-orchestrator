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
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { UpdateState } from '../../../shared/ipc';
import { UpdatePopover, updateNoticePending } from './UpdatePopover';

afterEach(cleanup);

const readyUpdate: UpdateState = {
  status: 'ready',
  currentVersion: '0.1.0',
  targetVersion: '0.2.0',
  packageFormat: 'macos',
  signatureStatus: 'verified',
  message: 'A verified update is downloaded and ready to install.',
};

/** The toolbar owns the open state in the app; mirror that here. */
function Harness({
  update = readyUpdate,
  dismissedVersion = null,
  scheduling = false,
  onDismiss = vi.fn(),
  onOpenSettings = vi.fn(),
  onInstallWhenIdle = async () => {},
}: {
  update?: UpdateState | null;
  dismissedVersion?: string | null;
  scheduling?: boolean;
  onDismiss?: (version: string) => void;
  onOpenSettings?: () => void;
  onInstallWhenIdle?: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <UpdatePopover
      update={update}
      dismissedVersion={dismissedVersion}
      scheduling={scheduling}
      open={open}
      onOpenChange={setOpen}
      onDismiss={onDismiss}
      onOpenSettings={onOpenSettings}
      onInstallWhenIdle={onInstallWhenIdle}
    />
  );
}

function trigger(): HTMLElement {
  return screen.getByRole('button', { name: 'Show available update' });
}

function popover(): HTMLElement | null {
  return screen.queryByRole('region', { name: 'Available update' });
}

describe('updateNoticePending', () => {
  it('matches exactly the statuses and dismissal state the in-flow banner used', () => {
    expect(updateNoticePending(readyUpdate, null)).toBe(true);
    expect(updateNoticePending({ ...readyUpdate, status: 'scheduled' }, null)).toBe(true);
    expect(updateNoticePending({ ...readyUpdate, status: 'available' }, null)).toBe(true);
    expect(updateNoticePending({ ...readyUpdate, status: 'installing' }, null)).toBe(false);
    expect(updateNoticePending({ ...readyUpdate, status: 'idle' }, null)).toBe(false);
    expect(updateNoticePending(readyUpdate, '0.2.0')).toBe(false);
    expect(updateNoticePending(readyUpdate, '0.1.5')).toBe(true);
    expect(updateNoticePending({ ...readyUpdate, targetVersion: undefined }, null)).toBe(false);
    expect(updateNoticePending(null, null)).toBe(false);
  });
});

describe('UpdatePopover', () => {
  it('shows a click-only trigger that never opens the popover by itself', () => {
    render(<Harness />);
    expect(trigger()).toBeVisible();
    expect(trigger()).toHaveAttribute('aria-expanded', 'false');
    expect(popover()).not.toBeInTheDocument();
  });

  it('renders nothing at all when the version is dismissed', () => {
    render(<Harness dismissedVersion="0.2.0" />);
    expect(screen.queryByRole('button', { name: 'Show available update' })).not.toBeInTheDocument();
  });

  it('carries the headline, message, and the Updates and Dismiss actions', async () => {
    const onOpenSettings = vi.fn();
    const onDismiss = vi.fn();
    render(<Harness onOpenSettings={onOpenSettings} onDismiss={onDismiss} />);
    const user = userEvent.setup();

    await user.click(trigger());
    const surface = popover();
    expect(surface).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Agentico 0.2.0 is available' })).toBeVisible();
    expect(surface).toHaveTextContent('A verified update is downloaded and ready to install.');
    // Install When Idle is for interrupting active work; there is none here.
    expect(screen.queryByRole('button', { name: 'Install When Idle' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Updates' }));
    expect(onOpenSettings).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(onDismiss).toHaveBeenCalledWith('0.2.0');
  });

  it('offers Install When Idle only with installable active work, and reflects the scheduled state', async () => {
    const onInstallWhenIdle = vi.fn(async () => {});
    const activeWork: UpdateState = {
      ...readyUpdate,
      activeWorkSummary: '1 workflow and AMA session are active.',
    };
    const view = render(<Harness update={activeWork} onInstallWhenIdle={onInstallWhenIdle} />);
    const user = userEvent.setup();

    await user.click(trigger());
    expect(popover()).toHaveTextContent('1 workflow and AMA session are active.');
    const install = screen.getByRole('button', { name: 'Install When Idle' });
    await user.click(install);
    expect(onInstallWhenIdle).toHaveBeenCalledTimes(1);

    // The popover stays open across state refreshes, so Install When Idle can
    // report back in place — exactly what the packaged journey asserts.
    view.rerender(
      <Harness update={{ ...activeWork, status: 'scheduled' }} onInstallWhenIdle={vi.fn()} />,
    );
    expect(screen.getByRole('button', { name: 'Scheduled for Idle' })).toBeDisabled();

    view.rerender(<Harness update={activeWork} scheduling onInstallWhenIdle={vi.fn()} />);
    expect(screen.getByRole('button', { name: 'Scheduling…' })).toBeDisabled();
  });

  it('withholds Install When Idle for a format that cannot be replaced in app', async () => {
    render(
      <Harness
        update={{
          ...readyUpdate,
          status: 'available',
          packageFormat: 'deb',
          activeWorkSummary: '1 workflow is active.',
        }}
      />,
    );
    await userEvent.click(trigger());
    expect(screen.queryByRole('button', { name: 'Install When Idle' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Updates' })).toBeVisible();
  });

  it('dismisses on Escape and on an outside pointer, returning focus to the trigger', async () => {
    render(<Harness />);
    const user = userEvent.setup();

    await user.click(trigger());
    await user.keyboard('{Escape}');
    expect(popover()).not.toBeInTheDocument();
    expect(trigger()).toHaveFocus();

    await user.click(trigger());
    await user.click(document.body);
    expect(popover()).not.toBeInTheDocument();

    await user.click(trigger());
    await user.click(trigger());
    expect(popover()).not.toBeInTheDocument();
  });
});
