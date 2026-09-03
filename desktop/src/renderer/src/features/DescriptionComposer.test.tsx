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

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it } from 'vitest';
import type { ConnectionState, RepositoryFileRef } from '../../../shared/ipc';
import { installAgenticoMock } from '../test/agenticoMock';
import { FILE_SEARCH_REQUIRES_LOCAL_SERVER } from '../localServerCopy';
import { STAGED_ON_OTHER_SERVER, type ComposerUploadItem } from './stagedItems';
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
  serverKey: 'server-key-1',
};

/** A controlled host so staged images/attachments/references render as chips. */
function Harness() {
  const [value, setValue] = useState('');
  const [images, setImages] = useState<readonly string[]>([]);
  const [attachments, setAttachments] = useState<readonly string[]>([]);
  const [imageUploads, setImageUploads] = useState<readonly ComposerUploadItem[]>([]);
  const [attachmentUploads, setAttachmentUploads] = useState<readonly ComposerUploadItem[]>([]);
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
      imageUploads={imageUploads}
      attachmentUploads={attachmentUploads}
      repositoryFiles={files}
      onValueChange={setValue}
      onImagesChange={(update) => setImages(update)}
      onAttachmentsChange={(update) => setAttachments(update)}
      onImageUploadsChange={(update) => setImageUploads(update)}
      onAttachmentUploadsChange={(update) => setAttachmentUploads(update)}
      onRepositoryFilesChange={(update) => setFiles(update)}
      onError={() => undefined}
    />
  );
}

const IMAGE_FILE = () => new File(['image'], 'image.png', { type: 'image/png' });
const DOC_FILE = () => new File(['doc'], 'notes.pdf', { type: 'application/pdf' });

describe('DescriptionComposer on a remote server', () => {
  it('keeps the attach affordances enabled and stages picks as uploads', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    mock.api.pickCreationFiles.mockResolvedValue({ paths: ['/shots/one.png'] });
    render(<Harness />);
    const user = userEvent.setup();

    const attach = await screen.findByRole('button', { name: 'Attach files or photos' });
    expect(attach).toBeEnabled();
    expect(screen.getByText(/files upload to the server\./)).toBeVisible();

    await user.click(attach);
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));

    expect(mock.api.pickCreationFiles).toHaveBeenCalledWith('image');
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledWith('image', ['/shots/one.png']);
    expect(await screen.findByText(/one\.png/)).toBeVisible();
    // Nothing local is staged as a path remotely.
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledTimes(1);
  });

  it('shows per-item uploading state while the batch is in flight', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    let release: (result: unknown) => void = () => undefined;
    mock.api.uploadCreationFiles.mockImplementation(
      () => new Promise((resolve) => (release = resolve)),
    );
    mock.api.importDroppedCreationFiles.mockImplementation(() => ({
      paths: ['/shots/dropped.png'],
    }));
    render(<Harness />);
    const textarea = await screen.findByLabelText('Description');

    fireEvent.drop(textarea.closest('.composer')!, { dataTransfer: { files: [IMAGE_FILE()] } });

    expect(await screen.findByText('Uploading…')).toBeVisible();
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledWith('image', ['/shots/dropped.png']);

    await act(async () => {
      release({
        results: [
          {
            ok: true,
            name: 'dropped.png',
            upload: {
              reference: 'ref-dropped',
              kind: 'image',
              name: 'dropped.png',
              size: 4,
              serverKey: 'server-key-1',
            },
          },
        ],
      });
    });
    expect(screen.queryByText('Uploading…')).not.toBeInTheDocument();
    expect(screen.getByText(/dropped\.png/)).toBeVisible();
  });

  it('marks failed items with the message and a retry/remove pair', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    mock.api.pickCreationFiles.mockResolvedValue({ paths: ['/shots/big.png'] });
    mock.api.uploadCreationFiles.mockResolvedValue({
      results: [
        {
          ok: false,
          name: 'big.png',
          error: {
            code: 'request_too_large',
            message: 'File exceeds limit.',
            remediation: 'Choose a smaller file.',
          },
        },
      ],
    });
    render(<Harness />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));

    const failure = await screen.findByText(/File exceeds limit\./);
    expect(failure).toBeVisible();
    expect(await screen.findByRole('button', { name: 'Retry big.png' })).toBeVisible();

    // The error path is recoverable without re-picking: retry re-uploads the source.
    mock.api.uploadCreationFiles.mockImplementation(() => new Promise(() => undefined));
    await user.click(screen.getByRole('button', { name: 'Retry big.png' }));
    expect(mock.api.uploadCreationFiles).toHaveBeenLastCalledWith('image', ['/shots/big.png']);
    expect(await screen.findByText('Uploading…')).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Remove big.png' }));
    expect(screen.queryByText('big.png')).not.toBeInTheDocument();
  });

  it('imports pasted files as uploads and stages path-less clipboard bitmaps', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
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
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledWith('image', ['/safe/pasted.png']);
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledWith('attachment', ['/safe/notes.pdf']);

    mock.api.importDroppedCreationFiles.mockImplementation(() => ({ paths: [] }));
    fireEvent.paste(textarea, {
      clipboardData: { files: [IMAGE_FILE()], items: [{ type: 'image/png' }] },
    });
    expect(await screen.findByText(/clipboard-image\.png/)).toBeVisible();
    expect(mock.api.uploadCreationFiles).toHaveBeenCalledWith('image', [
      '/tmp/clipboard-image.png',
    ]);
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
    expect(mock.api.uploadCreationFiles).not.toHaveBeenCalled();
  });

  it('badges uploads staged on another server after a switch', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    mock.api.pickCreationFiles.mockResolvedValue({ paths: ['/shots/one.png'] });
    render(<Harness />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Attach files or photos' }));
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    expect(await screen.findByText(/one\.png/)).toBeVisible();
    expect(screen.queryByText(STAGED_ON_OTHER_SERVER)).not.toBeInTheDocument();

    act(() =>
      mock.emitConnection({
        ...REMOTE_CONNECTION,
        serverKey: 'server-key-2',
      }),
    );

    expect(await screen.findByText(STAGED_ON_OTHER_SERVER)).toBeVisible();

    act(() => mock.emitConnection(REMOTE_CONNECTION));
    expect(screen.queryByText(STAGED_ON_OTHER_SERVER)).not.toBeInTheDocument();
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

  it('switches the staging strategy live with the connection kind', async () => {
    const mock = installAgenticoMock({ connection: REMOTE_CONNECTION });
    mock.api.pickCreationFiles.mockResolvedValue({ paths: ['/safe/one.png'] });
    render(<Harness />);
    const user = userEvent.setup();

    expect(await screen.findByText(/files upload to the server\./)).toBeVisible();

    act(() => mock.emitConnection(LOCAL_CONNECTION));

    const attach = screen.getByRole('button', { name: 'Attach files or photos' });
    expect(attach).toBeEnabled();
    expect(
      screen.getByText('Paste or drop images and documents anywhere in the description.'),
    ).toBeVisible();

    await user.click(attach);
    await user.click(screen.getByRole('menuitem', { name: 'Add photos' }));
    expect(mock.api.pickCreationFiles).toHaveBeenCalledWith('image');
    // Locally the pick stages a path directly — no upload round trip.
    expect(mock.api.uploadCreationFiles).not.toHaveBeenCalled();
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
    expect(mock.api.uploadCreationFiles).not.toHaveBeenCalled();
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
  it('keeps attach affordances enabled under every connection; only mention search stays local-only', async () => {
    const mock = installAgenticoMock();
    render(<Harness />);

    // The mock default connection is mid-resolution (local-permissive).
    const attach = await screen.findByRole('button', { name: 'Attach files or photos' });
    expect(attach).toBeEnabled();

    for (const connection of [REMOTE_CONNECTION, LOCAL_CONNECTION]) {
      act(() => mock.emitConnection(connection));
      expect(screen.getByRole('button', { name: 'Attach files or photos' })).toBeEnabled();
    }
  });
});
