import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import type { ConnectionState, RepositoryFileRef } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import {
  ATTACHMENT_REQUIRES_LOCAL_SERVER,
  FILE_SEARCH_REQUIRES_LOCAL_SERVER,
} from '../localServerCopy';
import { DescriptionComposer } from './DescriptionComposer';

afterEach(cleanup);

const LOCAL_CONNECTION: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Connected.',
  ownership: 'app-owned',
  kind: 'local',
};

const REMOTE_CONNECTION: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Connected.',
  ownership: 'external',
  kind: 'remote',
};

/** A controlled host so staged images/attachments/references render as chips. */
function Harness() {
  const [value, setValue] = useState('');
  const [images, setImages] = useState<readonly string[]>([]);
  const [attachments, setAttachments] = useState<readonly string[]>([]);
  const [files, setFiles] = useState<readonly RepositoryFileRef[]>([]);
  return (
    <DescriptionComposer
      id="description"
      label="Description"
      placeholder="Describe the work"
      value={value}
      repoKeys={['repo-a']}
      images={images}
      attachments={attachments}
      repositoryFiles={files}
      onValueChange={setValue}
      onImagesChange={(update) => setImages(update)}
      onAttachmentsChange={(update) => setAttachments(update)}
      onRepositoryFilesChange={(update) => setFiles(update)}
      onError={() => undefined}
    />
  );
}

const IMAGE_FILE = () => new File(['image'], 'image.png', { type: 'image/png' });
const DOC_FILE = () => new File(['doc'], 'notes.pdf', { type: 'application/pdf' });

describe('DescriptionComposer on a remote server', () => {
  it('disables the attach picker with the shared explanation and swaps the hint', async () => {
    installAgenticoMock({ connection: REMOTE_CONNECTION });
    render(<Harness />);
    const user = userEvent.setup();

    const attach = await screen.findByRole('button', { name: 'Attach files or photos' });
    expect(attach).toBeDisabled();
    expect(attach).toHaveAttribute('title', ATTACHMENT_REQUIRES_LOCAL_SERVER);
    expect(screen.getByText(ATTACHMENT_REQUIRES_LOCAL_SERVER)).toBeVisible();

    await user.click(attach);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('intercepts pasted files with the explanation and stages nothing', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    render(<Harness />);
    const textarea = await screen.findByLabelText('Description');

    fireEvent.paste(textarea, {
      clipboardData: {
        files: [IMAGE_FILE(), DOC_FILE()],
        items: [{ type: 'image/png' }],
      },
    });

    // The permanently visible hint carries the same copy; the transient
    // intercept notice is the live region.
    expect(screen.getByRole('status')).toHaveTextContent(ATTACHMENT_REQUIRES_LOCAL_SERVER);
    expect(mock.api.importDroppedCreationFiles).not.toHaveBeenCalled();
    expect(mock.api.readClipboardImage).not.toHaveBeenCalled();
    expect(screen.queryByLabelText('Attached files')).not.toBeInTheDocument();
  });

  it('leaves plain-text pastes untouched', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    render(<Harness />);
    const textarea = await screen.findByLabelText('Description');

    fireEvent.paste(textarea, {
      clipboardData: { files: [], items: [{ type: 'text/plain' }] },
    });

    expect(screen.queryByRole('status')).not.toBeInTheDocument();
    expect(mock.api.importDroppedCreationFiles).not.toHaveBeenCalled();
    expect(mock.api.readClipboardImage).not.toHaveBeenCalled();
  });

  it('intercepts dropped files with the explanation and stages nothing', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    render(<Harness />);
    const textarea = await screen.findByLabelText('Description');
    const composer = textarea.closest('.composer')!;

    fireEvent.drop(composer, { dataTransfer: { files: [IMAGE_FILE()] } });

    expect(screen.getByRole('status')).toHaveTextContent(ATTACHMENT_REQUIRES_LOCAL_SERVER);
    expect(mock.api.importDroppedCreationFiles).not.toHaveBeenCalled();
    expect(screen.queryByLabelText('Attached files')).not.toBeInTheDocument();
  });

  it('keeps opening the @-mention popover but explains instead of searching', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    render(<Harness />);
    const user = userEvent.setup();
    const textarea = await screen.findByLabelText('Description');

    await user.type(textarea, 'look at @app');

    const popover = await screen.findByRole('listbox', { name: 'Repository files' });
    expect(popover).toHaveTextContent(FILE_SEARCH_REQUIRES_LOCAL_SERVER);
    expect(popover).not.toHaveTextContent('Searching');
    expect(popover).not.toHaveTextContent('No files match');

    // Past the 120ms debounce, still no search dispatch and no cancel churn.
    await waitFor(() => expect(mock.api.searchCreationFiles).not.toHaveBeenCalled(), {
      timeout: 400,
    });
    await new Promise((resolve) => setTimeout(resolve, 250));
    expect(mock.api.searchCreationFiles).not.toHaveBeenCalled();
    expect(mock.api.cancelCreationFileSearch).not.toHaveBeenCalled();
  });

  it('re-enables every affordance live when the server switches back to local', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    mock.api.pickCreationFiles.mockResolvedValue({ paths: ['/safe/one.png'] });
    render(<Harness />);
    const user = userEvent.setup();

    expect(await screen.findByRole('button', { name: 'Attach files or photos' })).toBeDisabled();

    act(() => mock.emitConnection(LOCAL_CONNECTION));

    const attach = screen.getByRole('button', { name: 'Attach files or photos' });
    expect(attach).toBeEnabled();
    expect(attach).not.toHaveAttribute('title');
    expect(
      screen.getByText('Paste or drop images and documents anywhere in the description.'),
    ).toBeVisible();

    await user.click(attach);
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    expect(mock.api.pickCreationFiles).toHaveBeenCalledWith('image');
    expect(await screen.findByText(/one\.png/)).toBeVisible();
  });
});

describe('DescriptionComposer on a local server', () => {
  it('attaches photos and files through the picker menu', async () => {
    const mock = installAgenticoMock({ connection: LOCAL_CONNECTION });
    mock.api.pickCreationFiles.mockImplementation((kind: string) =>
      Promise.resolve({ paths: kind === 'image' ? ['/safe/one.png'] : ['/safe/spec.pdf'] }),
    );
    render(<Harness />);
    const user = userEvent.setup();

    const attach = await screen.findByRole('button', { name: 'Attach files or photos' });
    expect(attach).toBeEnabled();
    await user.click(attach);
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    await user.click(attach);
    await user.click(screen.getByRole('menuitem', { name: 'Add files' }));

    expect(await screen.findByText(/one\.png/)).toBeVisible();
    expect(screen.getByText(/spec\.pdf/)).toBeVisible();
    expect(screen.queryByText(ATTACHMENT_REQUIRES_LOCAL_SERVER)).not.toBeInTheDocument();
  });

  it('imports pasted files and materializes path-less clipboard bitmaps', async () => {
    const mock = installAgenticoMock({ connection: LOCAL_CONNECTION });
    mock.api.importDroppedCreationFiles.mockImplementation((kind: string) => ({
      paths: kind === 'image' ? ['/safe/pasted.png'] : ['/safe/notes.pdf'],
    }));
    mock.api.readClipboardImage.mockResolvedValue({ paths: ['/tmp/clipboard-image.png'] });
    render(<Harness />);
    const textarea = await screen.findByLabelText('Description');

    fireEvent.paste(textarea, {
      clipboardData: { files: [IMAGE_FILE(), DOC_FILE()], items: [{ type: 'image/png' }] },
    });
    expect(await screen.findByText(/pasted\.png/)).toBeVisible();
    expect(screen.getByText(/notes\.pdf/)).toBeVisible();

    mock.api.importDroppedCreationFiles.mockImplementation(() => ({ paths: [] }));
    fireEvent.paste(textarea, {
      clipboardData: { files: [IMAGE_FILE()], items: [{ type: 'image/png' }] },
    });
    expect(await screen.findByText(/clipboard-image\.png/)).toBeVisible();
  });

  it('drops files onto the composer and stages them', async () => {
    const mock = installAgenticoMock({ connection: LOCAL_CONNECTION });
    mock.api.importDroppedCreationFiles.mockImplementation((kind: string) => ({
      paths: kind === 'image' ? ['/safe/dropped.png'] : [],
    }));
    render(<Harness />);
    const textarea = await screen.findByLabelText('Description');

    fireEvent.drop(textarea.closest('.composer')!, { dataTransfer: { files: [IMAGE_FILE()] } });

    expect(mock.api.importDroppedCreationFiles).toHaveBeenCalledWith('image', expect.any(Array));
    expect(await screen.findByText(/dropped\.png/)).toBeVisible();
  });

  it('searches @-mentions and resolves a pick into a staged reference', async () => {
    const mock = installAgenticoMock({ connection: LOCAL_CONNECTION });
    mock.api.searchCreationFiles.mockImplementation((request: { requestId: string }) =>
      Promise.resolve({
        requestId: request.requestId,
        files: [{ repoKey: 'repo-a', path: 'src/app.ts' }],
        truncated: false,
        cancelled: false,
      }),
    );
    render(<Harness />);
    const user = userEvent.setup();
    const textarea = await screen.findByLabelText('Description');

    await user.type(textarea, 'look at @app');

    const option = await screen.findByRole('option', { name: /src\/app\.ts/ });
    expect(mock.api.searchCreationFiles).toHaveBeenCalled();
    await user.click(option);

    expect(textarea).toHaveValue('look at @repo-a/src/app.ts ');
    expect(await screen.findByText('@repo-a/src/app.ts')).toBeVisible();
    expect(screen.queryByText(FILE_SEARCH_REQUIRES_LOCAL_SERVER)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Remove reference repo-a/src/app.ts' }));
    expect(screen.queryByText('@repo-a/src/app.ts')).not.toBeInTheDocument();
  });
});

describe('DescriptionComposer locality gating matrix', () => {
  it('gates only on an authoritative remote connection; connecting states behave locally', async () => {
    const mock = installAgenticoMock();
    expect(mock).toBeDefined();
    render(<Harness />);

    // The mock default connection is mid-resolution, not ready: nothing gates.
    const attach = await screen.findByRole('button', { name: 'Attach files or photos' });
    expect(attach).toBeEnabled();

    act(() => mock.emitConnection(REMOTE_CONNECTION));
    expect(attach).toBeDisabled();

    act(() => mock.emitConnection(LOCAL_CONNECTION));
    expect(attach).toBeEnabled();
  });
});
