import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installAgenticoMock, creationDefaults } from '../test/agenticoMock';
import { CreateFeatureForm } from './CreateFeatureForm';

afterEach(cleanup);

async function renderForm(mock = installAgenticoMock()) {
  const onCreated = vi.fn();
  render(<CreateFeatureForm onCreated={onCreated} />);
  await screen.findByRole('button', { name: 'Create feature' });
  return { mock, onCreated };
}

async function fillValid(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('Name'), 'Search revamp');
  await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
}

describe('CreateFeatureForm defaults', () => {
  it('loads fresh server defaults on mount and prefills the branch choice', async () => {
    const { mock } = await renderForm();
    expect(mock.api.getCreationDefaults).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('radio', { name: /New feature branch/ })).toBeChecked();
    const panel = screen.getByRole('region', { name: 'Server defaults' });
    expect(panel).toHaveTextContent('medium');
    expect(panel).toHaveTextContent('balanced');
    expect(panel).toHaveTextContent('model-plan');
    expect(panel).toHaveTextContent('Set when the feature is created. You can change it later.');
  });

  it('shows repository eligibility: invalid repositories are disabled with the reason', async () => {
    await renderForm();
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeEnabled();
    const invalid = screen.getByRole('checkbox', { name: /repo-b/ });
    expect(invalid).toBeDisabled();
    expect(screen.getByText('Not a git repository.')).toBeInTheDocument();
  });

  it('surfaces a defaults-load failure with a retry affordance', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults
      .mockRejectedValueOnce(new Error('E_NOT_CONNECTED: The app is not connected.'))
      .mockResolvedValueOnce(creationDefaults());
    const user = userEvent.setup();
    render(<CreateFeatureForm onCreated={vi.fn()} />);
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('E_NOT_CONNECTED');
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('button', { name: 'Create feature' })).toBeInTheDocument();
  });
});

describe('CreateFeatureForm validation', () => {
  it('requires a name, links the error to the control, and moves focus there', async () => {
    const { mock, onCreated } = await renderForm();
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Create feature' }));

    const input = screen.getByLabelText('Name');
    expect(input).toHaveAttribute('aria-invalid', 'true');
    const errorId = input.getAttribute('aria-describedby');
    expect(errorId).toBe('feature-name-error');
    expect(document.getElementById(errorId!)).toHaveTextContent('Enter a feature name.');
    expect(input).toHaveFocus();
    expect(mock.api.createFeature).not.toHaveBeenCalled();
    expect(onCreated).not.toHaveBeenCalled();
  });

  it('requires at least one repository before submitting', async () => {
    const { mock } = await renderForm();
    const user = userEvent.setup();
    await user.type(screen.getByLabelText('Name'), 'Search revamp');
    await user.click(screen.getByRole('button', { name: 'Create feature' }));
    expect(screen.getByText('Select at least one repository.')).toBeInTheDocument();
    expect(mock.api.createFeature).not.toHaveBeenCalled();
  });
});

describe('CreateFeatureForm submission', () => {
  it('creates the feature, dispatches durable setup, and reports the new tab', async () => {
    const { mock, onCreated } = await renderForm();
    const user = userEvent.setup();
    await fillValid(user);
    await user.click(screen.getByRole('radio', { name: /current branch/ }));
    await user.click(screen.getByRole('button', { name: 'Create feature' }));

    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(mock.api.createFeature).toHaveBeenCalledWith({
      name: 'Search revamp',
      description: '',
      repoKeys: ['repo-a'],
      useCurrentBranch: true,
    });
    expect(mock.api.dispatchFeatureSetup).toHaveBeenCalledWith('abcd1234ef567890');
    expect(onCreated).toHaveBeenCalledWith({
      featureId: 'abcd1234ef567890',
      name: 'Search revamp',
    });
  });

  it('prevents duplicate submission while a create is pending', async () => {
    const mock = installAgenticoMock();
    let release: (value: { featureId: string }) => void = () => undefined;
    mock.api.createFeature.mockImplementation(() => new Promise((resolve) => (release = resolve)));
    const { onCreated } = await renderForm(mock);
    const user = userEvent.setup();
    await fillValid(user);

    const submit = screen.getByRole('button', { name: 'Create feature' });
    await user.click(submit);
    expect(screen.getByRole('button', { name: 'Creating…' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Creating…' }));
    expect(mock.api.createFeature).toHaveBeenCalledTimes(1);

    release({ featureId: 'abcd1234ef567890' });
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
    expect(mock.api.createFeature).toHaveBeenCalledTimes(1);
  });

  it('maps the structured not_ready rejection to a focused form-level alert', async () => {
    const mock = installAgenticoMock();
    mock.api.createFeature.mockRejectedValue(
      new Error('not_ready: runtime is not ready to create features claude is not signed in.'),
    );
    const { onCreated } = await renderForm(mock);
    const user = userEvent.setup();
    await fillValid(user);
    await user.click(screen.getByRole('button', { name: 'Create feature' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('not_ready');
    expect(alert).toHaveTextContent('claude is not signed in');
    await waitFor(() => expect(alert).toHaveFocus());
    expect(onCreated).not.toHaveBeenCalled();
    // The form stays usable for another attempt.
    expect(screen.getByRole('button', { name: 'Create feature' })).toBeEnabled();
  });

  it('maps a server-side name rejection back onto the name control', async () => {
    const mock = installAgenticoMock();
    mock.api.createFeature.mockRejectedValue(new Error('bad_request: name is required'));
    await renderForm(mock);
    const user = userEvent.setup();
    await fillValid(user);
    await user.click(screen.getByRole('button', { name: 'Create feature' }));

    await waitFor(() =>
      expect(screen.getByLabelText('Name')).toHaveAttribute('aria-invalid', 'true'),
    );
    expect(document.getElementById('feature-name-error')).toHaveTextContent('name is required');
    expect(screen.getByLabelText('Name')).toHaveFocus();
  });

  it('still opens the tab when the setup dispatch fails after a successful create', async () => {
    const mock = installAgenticoMock();
    mock.api.dispatchFeatureSetup.mockRejectedValue(new Error('conflict: already running'));
    const { onCreated } = await renderForm(mock);
    const user = userEvent.setup();
    await fillValid(user);
    await user.click(screen.getByRole('button', { name: 'Create feature' }));
    await waitFor(() => expect(onCreated).toHaveBeenCalledTimes(1));
  });
});
