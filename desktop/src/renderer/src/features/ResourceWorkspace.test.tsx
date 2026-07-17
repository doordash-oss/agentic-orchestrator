import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ResourceWorkspace } from './ResourceWorkspace';
import { installAgenticoMock, type AgenticoMock } from '../test/agenticoMock';

vi.mock('monaco-editor', () => ({
  editor: {
    create: vi.fn((_host: HTMLElement) => ({
      getValue: vi.fn(() => ''),
      onDidChangeModelContent: vi.fn(),
      dispose: vi.fn(),
    })),
    createModel: vi.fn((value: string) => ({ value, dispose: vi.fn() })),
    createDiffEditor: vi.fn((_host: HTMLElement) => ({
      setModel: vi.fn(),
      dispose: vi.fn(),
    })),
    setTheme: vi.fn(),
  },
}));

describe('ResourceWorkspace', () => {
  let mock: AgenticoMock;

  beforeEach(() => {
    mock = installAgenticoMock();
  });

  it('renders the browser and loads the catalogue', async () => {
    mock.api.listResources.mockResolvedValue({
      resources: [
        {
          id: 'r-skill-test',
          kind: 'skill',
          label: 'Skills — test-skill / SKILL.md',
          contentType: 'markdown',
          revision: 'sha256:abc',
          validatable: true,
          hierarchy: ['Skills', 'test-skill', 'SKILL.md'],
        },
      ],
    });
    render(<ResourceWorkspace />);
    await waitFor(() => {
      expect(screen.getByText('test-skill')).toBeTruthy();
    });
  });

  it('shows empty state when no resources exist', async () => {
    mock.api.listResources.mockResolvedValue({ resources: [] });
    render(<ResourceWorkspace />);
    await waitFor(() => {
      expect(screen.getByText('No editable resources found.')).toBeTruthy();
    });
  });

  it('shows error when catalogue fails', async () => {
    mock.api.listResources.mockRejectedValue(new Error('E_HTTP: server down'));
    render(<ResourceWorkspace />);
    await waitFor(() => {
      expect(screen.getByText(/server down/i)).toBeTruthy();
    });
  });

  it('loads a resource into the editor when selected', async () => {
    const user = userEvent.setup();
    mock.api.listResources.mockResolvedValue({
      resources: [
        {
          id: 'r-runtime-cfg',
          kind: 'runtime_config',
          label: 'Runtime Configuration',
          contentType: 'yaml',
          revision: 'sha256:rt',
          validatable: true,
          hierarchy: ['Runtime', 'Configuration'],
        },
      ],
    });
    mock.api.readResource.mockResolvedValue({
      id: 'r-runtime-cfg',
      kind: 'runtime_config',
      label: 'Runtime Configuration',
      contentType: 'yaml',
      revision: 'sha256:rt',
      text: 'defaults:\n  pipeline: moonshot\n',
      validatable: true,
      hierarchy: ['Runtime', 'Configuration'],
    });
    mock.api.loadLocalResourceDraft.mockResolvedValue(null);
    render(<ResourceWorkspace />);
    await waitFor(() => {
      expect(screen.getByText('Configuration')).toBeTruthy();
    });
    await user.click(screen.getByText('Configuration'));
    await waitFor(() => {
      expect(mock.api.readResource).toHaveBeenCalledWith('r-runtime-cfg');
    });
  });

  it('filters resources by search text', async () => {
    const user = userEvent.setup();
    mock.api.listResources.mockResolvedValue({
      resources: [
        {
          id: 'r-skill-1',
          kind: 'skill',
          label: 'Skills — alpha',
          contentType: 'markdown',
          revision: 'sha256:a',
          validatable: true,
          hierarchy: ['Skills', 'alpha'],
        },
        {
          id: 'r-skill-2',
          kind: 'skill',
          label: 'Skills — beta',
          contentType: 'markdown',
          revision: 'sha256:b',
          validatable: true,
          hierarchy: ['Skills', 'beta'],
        },
      ],
    });
    render(<ResourceWorkspace />);
    await waitFor(() => {
      expect(screen.getByText('alpha')).toBeTruthy();
      expect(screen.getByText('beta')).toBeTruthy();
    });
    await user.type(screen.getByPlaceholderText('Filter resources…'), 'alpha');
    await waitFor(() => {
      expect(screen.getByText('alpha')).toBeTruthy();
      expect(screen.queryByText('beta')).toBeNull();
    });
  });

  it('filters resources by kind button', async () => {
    const user = userEvent.setup();
    mock.api.listResources.mockResolvedValue({
      resources: [
        {
          id: 'r-fc-1',
          kind: 'feature_config',
          label: 'Feature A — Configuration',
          contentType: 'yaml',
          revision: 'sha256:fa',
          validatable: true,
          hierarchy: ['Features', 'Feature A', 'Configuration'],
          featureId: 'feat-a',
        },
        {
          id: 'r-skill-1',
          kind: 'skill',
          label: 'Skills — alpha',
          contentType: 'markdown',
          revision: 'sha256:a',
          validatable: true,
          hierarchy: ['Skills', 'alpha'],
        },
      ],
    });
    render(<ResourceWorkspace />);
    await waitFor(() => {
      expect(screen.getByText('Feature A')).toBeTruthy();
      expect(screen.getByText('alpha')).toBeTruthy();
    });
    await user.click(screen.getAllByText('Features')[0]!);
    await waitFor(() => {
      expect(screen.getByText('Feature A')).toBeTruthy();
      expect(screen.queryByText('alpha')).toBeNull();
    });
  });
});
