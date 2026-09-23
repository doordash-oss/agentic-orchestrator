import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { CreationDefaults } from '../../../shared/ipc';
import { creationDefaults, installAgenticoMock, ipcError } from '../test/agenticoMock';
import { CreateFeatureForm } from './CreateFeatureForm';
import type { CreationDraftState } from './creationDrafts';

afterEach(cleanup);

function defaults(configured: boolean): CreationDefaults {
  return {
    ...creationDefaults(),
    defaults: {
      ...creationDefaults().defaults,
      slackConfigured: configured,
      slackDefaults: {
        categories: { progress: true, needsInput: false, problems: true },
        recipientNames: ['Workspace alerts'],
      },
    },
  } as CreationDefaults;
}

async function openContract(configured = true, retainedDraft?: CreationDraftState) {
  const mock = installAgenticoMock({ defaults: defaults(configured) });
  const onDraftDetach = vi.fn();
  const user = userEvent.setup();
  const form = render(
    <CreateFeatureForm
      onCreated={vi.fn()}
      onClose={vi.fn()}
      retainedDraft={retainedDraft}
      onDraftDetach={onDraftDetach}
    />,
  );
  await screen.findByRole('dialog', { name: 'New feature' });
  if (retainedDraft === undefined) {
    await screen.findByRole('button', { name: 'Next: Describe' });
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Notifications sample');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
  }
  return { mock, user, form, onDraftDetach };
}

describe('creation Notifications', () => {
  it('shows inherited categories and additive recipients only when Slack is configured', async () => {
    const { form } = await openContract();
    const group = within(screen.getByRole('group', { name: 'Notifications' }));
    expect(group.getByRole('checkbox', { name: /Mute/ })).not.toBeChecked();
    expect(group.getByRole('combobox', { name: 'Progress' })).toHaveValue('');
    expect(group.getByRole('combobox', { name: 'Needs input' })).toHaveValue('');
    expect(group.getByRole('combobox', { name: 'Problems' })).toHaveValue('');
    expect(group.getByRole('option', { name: 'Workspace default (off)' })).toBeVisible();
    expect(group.getByText(/Workspace alerts/)).toBeVisible();
    expect(group.getByRole('textbox', { name: 'Recipient 1' })).toHaveValue('');
    form.unmount();

    await openContract(false);
    const unconfigured = within(screen.getByRole('group', { name: 'Notifications' }));
    expect(unconfigured.getByText('Set up Slack in Settings')).toBeVisible();
    expect(unconfigured.queryByRole('checkbox')).toBeNull();
    expect(unconfigured.queryByRole('textbox')).toBeNull();
  });

  it('resolves on Enter and blur, blocks invalid rows, and sends only changed settings', async () => {
    const { mock, user } = await openContract();
    const group = within(screen.getByRole('group', { name: 'Notifications' }));
    const first = group.getByRole('textbox', { name: 'Recipient 1' });
    mock.api.resolveSlackRecipient.mockResolvedValueOnce({
      typedText: '#alerts',
      kind: 'channel',
      id: 'C123',
      displayName: '#alerts',
    });
    await user.type(first, '#alerts{Enter}');
    expect(mock.api.resolveSlackRecipient).toHaveBeenCalledWith({ input: '#alerts' });
    expect(await group.findByText('#alerts')).toBeVisible();
    await user.type(first, 'x');
    expect(group.queryByText('#alerts', { exact: true })).toBeNull();
    await user.clear(first);
    mock.api.resolveSlackRecipient.mockResolvedValueOnce({
      typedText: '#alerts',
      kind: 'channel',
      id: 'C123',
      displayName: '#alerts',
    });
    await user.type(first, '#alerts{Enter}');
    expect(await group.findByText('#alerts')).toBeVisible();
    await user.click(group.getByRole('button', { name: 'Add recipient' }));
    const second = group.getByRole('textbox', { name: 'Recipient 2' });
    mock.api.resolveSlackRecipient.mockRejectedValueOnce(
      ipcError('slack_recipient_not_found', 'Recipient not found.'),
    );
    await user.type(second, '#missing');
    await user.tab();
    expect(await group.findByText('Recipient not found.')).toBeVisible();
    expect(second).toHaveAttribute('aria-invalid', 'true');
    expect(second).toHaveAttribute('aria-describedby', expect.stringContaining('error'));
    await user.click(screen.getByRole('button', { name: 'Create and start' }));
    expect(mock.api.createFeature).not.toHaveBeenCalled();
    expect(group.getByText(/Resolve or remove every recipient/)).toBeVisible();
    await user.click(group.getByRole('button', { name: 'Remove recipient 2' }));
    await user.selectOptions(group.getByRole('combobox', { name: 'Progress' }), 'off');
    await user.click(group.getByRole('checkbox', { name: /Mute/ }));
    expect(group.getByRole('combobox', { name: 'Progress' })).toBeEnabled();
    expect(group.getByText(/no effect while muted/i)).toBeVisible();
    await user.click(group.getByRole('checkbox', { name: /Mute/ }));
    expect(group.getByRole('combobox', { name: 'Progress' })).toHaveValue('off');
    await user.click(screen.getByRole('button', { name: 'Create and start' }));
    await waitFor(() => expect(mock.api.createFeature).toHaveBeenCalledOnce());
    expect(mock.api.createFeature.mock.calls[0]?.[0]).toMatchObject({
      slackNotifications: {
        progress: 'off',
        recipients: [{ typedText: '#alerts', kind: 'channel', id: 'C123', displayName: '#alerts' }],
      },
    });
  });

  it('keeps notifications in the in-memory detach snapshot', async () => {
    const { user, form, onDraftDetach } = await openContract();
    const group = within(screen.getByRole('group', { name: 'Notifications' }));
    await user.click(group.getByRole('checkbox', { name: /Mute/ }));
    await user.selectOptions(group.getByRole('combobox', { name: 'Problems' }), 'off');
    await user.type(group.getByRole('textbox', { name: 'Recipient 1' }), '#draft');
    form.unmount();
    const retained = onDraftDetach.mock.calls[0]?.[0] as CreationDraftState;
    expect(retained).toMatchObject({ slackMuted: true, slackOverrides: { problems: 'off' } });
    await openContract(true, retained);
    const restored = within(screen.getByRole('group', { name: 'Notifications' }));
    expect(restored.getByRole('checkbox', { name: /Mute/ })).toBeChecked();
    expect(restored.getByRole('combobox', { name: 'Problems' })).toHaveValue('off');
    expect(restored.getByRole('textbox', { name: 'Recipient 1' })).toHaveValue('#draft');
  });

  it('submits mute and independent overrides without discarding them', async () => {
    const { mock, user } = await openContract();
    const group = within(screen.getByRole('group', { name: 'Notifications' }));
    await user.selectOptions(group.getByRole('combobox', { name: 'Needs input' }), 'on');
    await user.click(group.getByRole('checkbox', { name: /Mute/ }));
    await user.click(screen.getByRole('button', { name: 'Create and start' }));
    await waitFor(() => expect(mock.api.createFeature).toHaveBeenCalledOnce());
    expect(mock.api.createFeature.mock.calls[0]?.[0]).toMatchObject({
      slackNotifications: { mode: 'muted', needsInput: 'on' },
    });
    expect(mock.api.createFeature.mock.calls[0]?.[0].slackNotifications).not.toHaveProperty(
      'problems',
    );
  });

  it('omits the Slack object when unchanged or not configured', async () => {
    const { mock, user, form } = await openContract();
    await user.click(screen.getByRole('button', { name: 'Create and start' }));
    await waitFor(() => expect(mock.api.createFeature).toHaveBeenCalledOnce());
    expect(mock.api.createFeature.mock.calls[0]?.[0]).not.toHaveProperty('slackNotifications');
    form.unmount();

    const unconfigured = await openContract(false);
    await unconfigured.user.click(screen.getByRole('button', { name: 'Create and start' }));
    await waitFor(() => expect(unconfigured.mock.api.createFeature).toHaveBeenCalledOnce());
    expect(unconfigured.mock.api.createFeature.mock.calls[0]?.[0]).not.toHaveProperty(
      'slackNotifications',
    );
  });
});
