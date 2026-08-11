/**
 * Shared description composer for the creation and refactor wizards: one
 * textarea that accepts pasted or dropped images and documents, an attach
 * menu, removable file chips, and @-mention search over repository files.
 */
import {
  useEffect,
  useRef,
  useState,
  type ClipboardEvent,
  type DragEvent,
  type KeyboardEvent,
} from 'react';
import {
  CREATION_ATTACHMENT_LIMIT,
  CREATION_IMAGE_LIMIT,
  CREATION_REPOSITORY_FILE_LIMIT,
  type CreationFileKind,
  type RepositoryFileRef,
} from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { useConnectionState } from '../hooks';
import { FILE_SEARCH_REQUIRES_LOCAL_SERVER } from '../localServerCopy';
import {
  failPendingUploads,
  isStagedOnOtherServer,
  pendingUploadItems,
  reconcileUploadResults,
  STAGED_ON_OTHER_SERVER,
  type ComposerUploadItem,
} from './stagedItems';

interface MentionToken {
  /** Index of the "@" character in the description. */
  start: number;
  query: string;
}

/** An "@token" immediately before the caret, at start-of-text or after whitespace. */
function detectMention(text: string, caret: number): MentionToken | null {
  const match = /(^|[\s([{])@([^\s@]*)$/.exec(text.slice(0, caret));
  if (match === null) return null;
  const query = match[2] ?? '';
  return { start: caret - query.length - 1, query };
}

function basename(path: string): string {
  return path.split(/[\\/]/).pop() ?? 'Selected file';
}

function unique(items: readonly string[]): string[] {
  return [...new Set(items)];
}

export interface DescriptionComposerProps {
  id: string;
  label: string;
  placeholder: string;
  value: string;
  /** Repositories the @-mention search covers. */
  repoKeys: readonly string[];
  /** Local-path attachments (local connections; remote refuses these at submit). */
  images: readonly string[];
  attachments: readonly string[];
  /** Server-staged attachments in every state (uploading/ready/failed). */
  imageUploads: readonly ComposerUploadItem[];
  attachmentUploads: readonly ComposerUploadItem[];
  repositoryFiles: readonly RepositoryFileRef[];
  onValueChange(value: string): void;
  onImagesChange(update: (items: readonly string[]) => readonly string[]): void;
  onAttachmentsChange(update: (items: readonly string[]) => readonly string[]): void;
  onImageUploadsChange(
    update: (items: readonly ComposerUploadItem[]) => readonly ComposerUploadItem[],
  ): void;
  onAttachmentUploadsChange(
    update: (items: readonly ComposerUploadItem[]) => readonly ComposerUploadItem[],
  ): void;
  onRepositoryFilesChange(
    update: (files: readonly RepositoryFileRef[]) => readonly RepositoryFileRef[],
  ): void;
  onError(error: WizardError): void;
}

export function DescriptionComposer({
  id,
  label,
  placeholder,
  value,
  repoKeys,
  images,
  attachments,
  imageUploads,
  attachmentUploads,
  repositoryFiles,
  onValueChange,
  onImagesChange,
  onAttachmentsChange,
  onImageUploadsChange,
  onAttachmentUploadsChange,
  onRepositoryFilesChange,
  onError,
}: DescriptionComposerProps) {
  const [mention, setMention] = useState<MentionToken | null>(null);
  const [mentionResults, setMentionResults] = useState<readonly RepositoryFileRef[]>([]);
  const [mentionIndex, setMentionIndex] = useState(0);
  const [mentionStatus, setMentionStatus] = useState<'idle' | 'searching'>('idle');
  const [attachMenuOpen, setAttachMenuOpen] = useState(false);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const attachMenuRef = useRef<HTMLDivElement | null>(null);
  // Locality follows the live connection: remote connections stage files
  // through the upload channel; only the @-mention repository search stays
  // local-only (its copy is shared with the AMA panel via localServerCopy).
  const connection = useConnectionState();
  const remote = connection.status === 'ready' && connection.kind === 'remote';
  const serverKey = connection.status === 'ready' ? (connection.serverKey ?? null) : null;

  useEffect(() => {
    if (!attachMenuOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!attachMenuRef.current?.contains(event.target as Node)) setAttachMenuOpen(false);
    };
    window.addEventListener('pointerdown', onPointerDown);
    return () => window.removeEventListener('pointerdown', onPointerDown);
  }, [attachMenuOpen]);

  useEffect(() => {
    // Remote connections never search: the popover explains the limitation
    // instead of spinning on an endpoint the server cannot serve.
    if (remote || mention === null || mention.query === '' || repoKeys.length === 0) {
      setMentionResults([]);
      setMentionStatus('idle');
      return;
    }
    const requestId = crypto.randomUUID();
    let dispatched = false;
    const timer = window.setTimeout(() => {
      dispatched = true;
      setMentionStatus('searching');
      window.agentico
        .searchCreationFiles({ requestId, repoKeys: [...repoKeys], query: mention.query })
        .then((result) => {
          if (!result.cancelled) {
            setMentionResults(result.files);
            setMentionIndex(0);
            setMentionStatus('idle');
          }
        })
        .catch((err: unknown) => onError(parseIpcError(err)));
    }, 120);
    return () => {
      window.clearTimeout(timer);
      if (dispatched) void window.agentico.cancelCreationFileSearch(requestId);
    };
  }, [remote, mention, onError, repoKeys]);

  /**
   * Stages picked/dropped/pasted local paths on the connected server. The
   * pending chips land immediately (uploading state), then flip per-file to
   * ready or failed — the renderer's per-item progress surface.
   */
  const stageRemotely = (kind: CreationFileKind, paths: readonly string[]): void => {
    if (paths.length === 0) return;
    const apply = kind === 'image' ? onImageUploadsChange : onAttachmentUploadsChange;
    const limit = kind === 'image' ? CREATION_IMAGE_LIMIT : CREATION_ATTACHMENT_LIMIT;
    const pending = pendingUploadItems(kind, paths);
    apply((items) => {
      const known = new Set(items.map((item) => item.sourcePath));
      const additions = pending.filter((item) => !known.has(item.sourcePath));
      return [...items, ...additions].slice(0, limit);
    });
    window.agentico
      .uploadCreationFiles(kind, paths)
      .then((result) => {
        apply((items) => reconcileUploadResults(items, pending, result.results));
      })
      .catch((err: unknown) => {
        const message = parseIpcError(err).message;
        apply((items) => failPendingUploads(items, pending, message));
      });
  };

  const retryUpload = (item: ComposerUploadItem): void => {
    const apply = item.kind === 'image' ? onImageUploadsChange : onAttachmentUploadsChange;
    apply((items) => items.filter((candidate) => candidate.id !== item.id));
    stageRemotely(item.kind, [item.sourcePath]);
  };

  const removeUploadItem = (item: ComposerUploadItem): void => {
    const apply = item.kind === 'image' ? onImageUploadsChange : onAttachmentUploadsChange;
    apply((items) => items.filter((candidate) => candidate.id !== item.id));
  };

  const importFiles = (files: FileList | readonly File[]): number => {
    const list = Array.from(files);
    let importedCount = 0;
    const photos = list.filter((file) => file.type.startsWith('image/'));
    const documents = list.filter((file) => !file.type.startsWith('image/'));
    if (photos.length > 0) {
      const imported = window.agentico.importDroppedCreationFiles('image', photos);
      importedCount += imported.paths.length;
      if (remote) {
        stageRemotely('image', imported.paths);
      } else {
        onImagesChange((items) =>
          unique([...items, ...imported.paths]).slice(0, CREATION_IMAGE_LIMIT),
        );
      }
    }
    if (documents.length > 0) {
      const imported = window.agentico.importDroppedCreationFiles('attachment', documents);
      importedCount += imported.paths.length;
      if (remote) {
        stageRemotely('attachment', imported.paths);
      } else {
        onAttachmentsChange((items) =>
          unique([...items, ...imported.paths]).slice(0, CREATION_ATTACHMENT_LIMIT),
        );
      }
    }
    return importedCount;
  };

  const pickFiles = async (kind: 'image' | 'attachment'): Promise<void> => {
    setAttachMenuOpen(false);
    try {
      const result = await window.agentico.pickCreationFiles(kind);
      if (remote) {
        stageRemotely(kind, result.paths);
      } else if (kind === 'image')
        onImagesChange((current) =>
          unique([...current, ...result.paths]).slice(0, CREATION_IMAGE_LIMIT),
        );
      else
        onAttachmentsChange((current) =>
          unique([...current, ...result.paths]).slice(0, CREATION_ATTACHMENT_LIMIT),
        );
    } catch (err) {
      onError(parseIpcError(err));
    }
  };

  const syncMention = (element: HTMLTextAreaElement): void => {
    setMention(detectMention(element.value, element.selectionStart));
  };

  const applyMention = (file: RepositoryFileRef): void => {
    if (mention === null) return;
    const reference = `@${file.repoKey}/${file.path}`;
    const tokenEnd = mention.start + mention.query.length + 1;
    onValueChange(`${value.slice(0, mention.start)}${reference} ${value.slice(tokenEnd)}`);
    onRepositoryFilesChange((files) =>
      files.some((item) => item.repoKey === file.repoKey && item.path === file.path)
        ? files
        : [...files, file].slice(0, CREATION_REPOSITORY_FILE_LIMIT),
    );
    const caret = mention.start + reference.length + 1;
    setMention(null);
    setMentionResults([]);
    requestAnimationFrame(() => {
      const element = textareaRef.current;
      if (element !== null) {
        element.focus();
        element.setSelectionRange(caret, caret);
      }
    });
  };

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>): void => {
    if (mention === null || mentionResults.length === 0) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setMentionIndex((index) => (index + 1) % mentionResults.length);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setMentionIndex((index) => (index - 1 + mentionResults.length) % mentionResults.length);
    } else if (event.key === 'Enter' || event.key === 'Tab') {
      event.preventDefault();
      const file = mentionResults[mentionIndex];
      if (file !== undefined) applyMention(file);
    } else if (event.key === 'Escape') {
      setMention(null);
      setMentionResults([]);
    }
  };

  const onPaste = (event: ClipboardEvent): void => {
    const hasImage = Array.from(event.clipboardData.items ?? []).some((item) =>
      item.type.startsWith('image/'),
    );
    if (!hasImage && event.clipboardData.files.length === 0) return;
    event.preventDefault();
    const importedCount = importFiles(event.clipboardData.files);
    if (hasImage && importedCount === 0) {
      void window.agentico
        .readClipboardImage()
        .then((result) => {
          if (remote) {
            stageRemotely('image', result.paths);
          } else {
            onImagesChange((items) =>
              unique([...items, ...result.paths]).slice(0, CREATION_IMAGE_LIMIT),
            );
          }
        })
        .catch((err: unknown) => onError(parseIpcError(err)));
    }
  };

  const onDrop = (event: DragEvent): void => {
    event.preventDefault();
    if (event.dataTransfer.files.length === 0) return;
    importFiles(event.dataTransfer.files);
  };

  return (
    <div className="composer" onDragOver={(event) => event.preventDefault()} onDrop={onDrop}>
      <label className="form-field">
        <span className="form-field__label">{label}</span>
        <textarea
          ref={textareaRef}
          id={id}
          className="form-field__input form-field__input--multiline"
          value={value}
          maxLength={10000}
          rows={6}
          placeholder={placeholder}
          onChange={(event) => {
            onValueChange(event.target.value);
            syncMention(event.target);
          }}
          onSelect={(event) => syncMention(event.currentTarget)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
        />
      </label>
      {mention !== null && repoKeys.length > 0 ? (
        <div className="composer__mentions" role="listbox" aria-label="Repository files">
          {remote ? (
            <p className="composer__mentions-hint" role="status">
              {FILE_SEARCH_REQUIRES_LOCAL_SERVER}
            </p>
          ) : mention.query === '' ? (
            <p className="composer__mentions-hint">Keep typing to search repository files…</p>
          ) : mentionStatus === 'searching' && mentionResults.length === 0 ? (
            <p className="composer__mentions-hint" role="status">
              Searching {repoKeys.join(', ')}…
            </p>
          ) : mentionResults.length === 0 ? (
            <p className="composer__mentions-hint">No files match “{mention.query}”.</p>
          ) : (
            mentionResults.map((file, index) => (
              <button
                key={`${file.repoKey}:${file.path}`}
                type="button"
                role="option"
                aria-selected={index === mentionIndex}
                data-active={index === mentionIndex}
                className="composer__mention-option"
                onMouseEnter={() => setMentionIndex(index)}
                onClick={() => applyMention(file)}
              >
                <b>{file.repoKey}</b>
                <span>{file.path}</span>
              </button>
            ))
          )}
        </div>
      ) : null}
      <div className="composer__toolbar">
        <div className="composer__attach" ref={attachMenuRef}>
          <button
            type="button"
            className="composer__attach-button"
            aria-label="Attach files or photos"
            aria-haspopup="menu"
            aria-expanded={attachMenuOpen}
            onClick={() => setAttachMenuOpen((open) => !open)}
          >
            +
          </button>
          {attachMenuOpen ? (
            <div className="composer__attach-menu" role="menu">
              <button type="button" role="menuitem" onClick={() => void pickFiles('image')}>
                Add photos
              </button>
              <button type="button" role="menuitem" onClick={() => void pickFiles('attachment')}>
                Add files
              </button>
            </div>
          ) : null}
        </div>
        <span className="composer__hint">
          {remote
            ? 'Paste or drop images and documents anywhere in the description; files upload to the server.'
            : 'Paste or drop images and documents anywhere in the description.'}
        </span>
      </div>
      {images.length > 0 ||
      attachments.length > 0 ||
      imageUploads.length > 0 ||
      attachmentUploads.length > 0 ? (
        <ol className="composer__chips" aria-label="Attached files">
          {images.map((path) => (
            <li key={path} className="composer__chip" data-kind="image">
              <span>🖼 {basename(path)}</span>
              <button
                type="button"
                aria-label={`Remove ${basename(path)}`}
                onClick={() => onImagesChange((items) => items.filter((item) => item !== path))}
              >
                ×
              </button>
            </li>
          ))}
          {attachments.map((path) => (
            <li key={path} className="composer__chip" data-kind="attachment">
              <span>📎 {basename(path)}</span>
              <button
                type="button"
                aria-label={`Remove ${basename(path)}`}
                onClick={() =>
                  onAttachmentsChange((items) => items.filter((item) => item !== path))
                }
              >
                ×
              </button>
            </li>
          ))}
          {[...imageUploads, ...attachmentUploads].map((item) => (
            <li
              key={item.id}
              className="composer__chip"
              data-kind={item.kind}
              data-state={item.state}
            >
              <span>
                {item.kind === 'image' ? '🖼' : '📎'} {item.name}
              </span>
              {item.state === 'uploading' ? (
                <span className="composer__chip-state">Uploading…</span>
              ) : null}
              {item.state === 'failed' ? (
                <>
                  <span className="composer__chip-message" title={item.message}>
                    {item.message ?? 'Upload failed.'}
                  </span>
                  <button
                    type="button"
                    aria-label={`Retry ${item.name}`}
                    onClick={() => retryUpload(item)}
                  >
                    ↻
                  </button>
                </>
              ) : null}
              {isStagedOnOtherServer(item, serverKey) ? (
                <span className="composer__chip-badge">{STAGED_ON_OTHER_SERVER}</span>
              ) : null}
              <button
                type="button"
                aria-label={`Remove ${item.name}`}
                onClick={() => removeUploadItem(item)}
              >
                ×
              </button>
            </li>
          ))}
        </ol>
      ) : null}
      {repositoryFiles.length > 0 ? (
        <ol className="composer__chips" aria-label="Referenced repository files">
          {repositoryFiles.map((file) => (
            <li
              key={`${file.repoKey}:${file.path}`}
              className="composer__chip"
              data-kind="reference"
            >
              <span>
                @{file.repoKey}/{file.path}
              </span>
              <button
                type="button"
                aria-label={`Remove reference ${file.repoKey}/${file.path}`}
                onClick={() =>
                  onRepositoryFilesChange((files) =>
                    files.filter(
                      (item) => item.repoKey !== file.repoKey || item.path !== file.path,
                    ),
                  )
                }
              >
                ×
              </button>
            </li>
          ))}
        </ol>
      ) : null}
    </div>
  );
}
