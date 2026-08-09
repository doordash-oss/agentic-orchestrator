import { useState, useCallback, useEffect, useRef } from 'react';
import { useMediaQuery } from '../../hooks';
import { parseIpcError } from '../../wizard/ipcError';
import {
  DiffViewer,
  PrLinkButton,
  STATUS_LABELS,
  FILE_OP_GLYPH,
  type DiffLayout,
} from './completionShared';
import type { CompletionPreflightResult, RepositoryDiffResult } from '../../../../shared/ipc';

export interface ChangesSurfaceProps {
  featureId: string;
  preflight: CompletionPreflightResult | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  getRepositoryDiff: (
    featureId: string,
    repo: string,
    filePath?: string,
  ) => Promise<RepositoryDiffResult>;
  openExternal: (url: string) => Promise<{ ok: boolean }>;
  revealPath: (featureId: string, repo: string) => Promise<{ ok: boolean }>;
}

export function ChangesSurface({
  featureId,
  preflight,
  loading,
  error,
  onRetry,
  getRepositoryDiff,
  openExternal,
  revealPath,
}: ChangesSurfaceProps): React.ReactElement {
  const [selectedRepo, setSelectedRepo] = useState<string | null>(null);
  const [diff, setDiff] = useState<RepositoryDiffResult | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [diffError, setDiffError] = useState<string | null>(null);
  const [diffLayoutOverride, setDiffLayoutOverride] = useState<DiffLayout | null>(null);
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [fileDiff, setFileDiff] = useState<string | null>(null);
  const [fileLoading, setFileLoading] = useState(false);
  const repoDiffRequestRef = useRef(0);
  const fileDiffRequestRef = useRef(0);
  const constrainedLayout = useMediaQuery('(max-width: 900px)');
  const diffLayout: DiffLayout =
    diffLayoutOverride ?? (constrainedLayout ? 'unified' : 'side-by-side');

  const loadRepoDiff = useCallback(
    async (repo: string) => {
      const request = ++repoDiffRequestRef.current;
      fileDiffRequestRef.current += 1;
      setDiffLoading(true);
      setDiff(null);
      setDiffError(null);
      setSelectedFile(null);
      setFileDiff(null);
      try {
        const result = await getRepositoryDiff(featureId, repo);
        if (request !== repoDiffRequestRef.current) return;
        setDiff(result);
      } catch (err) {
        if (request !== repoDiffRequestRef.current) return;
        setDiffError(parseIpcError(err).message);
      } finally {
        if (request === repoDiffRequestRef.current) {
          setDiffLoading(false);
        }
      }
    },
    [featureId, getRepositoryDiff],
  );

  const loadFileDiff = useCallback(
    async (repo: string, filePath: string) => {
      const request = ++fileDiffRequestRef.current;
      setFileLoading(true);
      setFileDiff(null);
      try {
        const result = await getRepositoryDiff(featureId, repo, filePath);
        if (request !== fileDiffRequestRef.current) return;
        if (result.fileDiff) {
          setFileDiff(result.fileDiff);
        } else if (result.fileBinary) {
          setFileDiff('Binary file — diff content unavailable');
        } else if (result.fileUnavailable) {
          setFileDiff('File content unavailable');
        }
      } catch (err) {
        if (request !== fileDiffRequestRef.current) return;
        setDiffError(parseIpcError(err).message);
      } finally {
        if (request === fileDiffRequestRef.current) {
          setFileLoading(false);
        }
      }
    },
    [featureId, getRepositoryDiff],
  );

  const handleRepoSelect = useCallback(
    (repo: string) => {
      setSelectedRepo(repo);
      void loadRepoDiff(repo);
    },
    [loadRepoDiff],
  );

  const handleFileSelect = useCallback(
    (filePath: string) => {
      setSelectedFile(filePath);
      if (selectedRepo) {
        void loadFileDiff(selectedRepo, filePath);
      }
    },
    [loadFileDiff, selectedRepo],
  );

  useEffect(() => {
    const firstRepo = preflight?.repos[0]?.repo;
    if (firstRepo === undefined || selectedRepo !== null) return;
    setSelectedRepo(firstRepo);
    void loadRepoDiff(firstRepo);
  }, [loadRepoDiff, preflight, selectedRepo]);

  useEffect(() => {
    const firstFile = diff?.files[0]?.path;
    if (firstFile === undefined || selectedFile !== null || selectedRepo === null) return;
    setSelectedFile(firstFile);
    void loadFileDiff(selectedRepo, firstFile);
  }, [diff, loadFileDiff, selectedFile, selectedRepo]);

  const activeRepo = preflight?.repos.find((repo) => repo.repo === selectedRepo);

  return (
    <section className="changes-manifest" aria-label="Changes">
      <header className="changes-manifest__intro">
        <div>
          <p className="cockpit__caption">Repository delta</p>
          <h3>Change manifest</h3>
          <p>
            Review the files produced by this feature before opening the worktree or pull request.
          </p>
        </div>
        <div className="changes-manifest__summary" aria-label="Change summary">
          <strong>{preflight?.repos.length ?? '—'}</strong>
          <span>{preflight?.repos.length === 1 ? 'repository' : 'repositories'}</span>
          <strong>{diff?.files.length ?? '—'}</strong>
          <span>{diff?.files.length === 1 ? 'changed file' : 'changed files'}</span>
        </div>
      </header>

      {error !== null ? (
        <div className="completion-workspace__error" role="alert">
          {error}
          <button type="button" onClick={onRetry}>
            Retry
          </button>
        </div>
      ) : null}
      {loading ? <ChangesManifestSkeleton /> : null}
      {diffError !== null ? (
        <div className="completion-workspace__error" role="alert">
          {diffError}
        </div>
      ) : null}
      {preflight !== null ? (
        <div className="changes-manifest__repositories" role="tablist" aria-label="Repositories">
          {preflight.repos.map((repo) => (
            <button
              key={repo.repo}
              type="button"
              role="tab"
              aria-selected={selectedRepo === repo.repo}
              className="changes-manifest__repository"
              onClick={() => handleRepoSelect(repo.repo)}
            >
              <span className="changes-manifest__repository-mark" aria-hidden="true" />
              <span className="completion-workspace__repo-name">{repo.repo}</span>
              <span className="completion-workspace__repo-status" data-status={repo.status}>
                {STATUS_LABELS[repo.status] ?? repo.status}
              </span>
            </button>
          ))}
        </div>
      ) : null}

      {selectedRepo !== null ? (
        <div className="changes-manifest__repository-bar">
          <div>
            <span>Inspecting</span>
            <strong>{selectedRepo}</strong>
          </div>
          <div className="changes-manifest__repository-actions">
            {activeRepo?.prUrl === undefined ? null : (
              <PrLinkButton url={activeRepo.prUrl} openExternal={openExternal} />
            )}
            <button
              type="button"
              className="completion-workspace__reveal"
              onClick={() => void revealPath(featureId, selectedRepo)}
            >
              Reveal in Finder
            </button>
          </div>
        </div>
      ) : null}

      {selectedRepo !== null && diffLoading ? <DiffSkeleton /> : null}

      {selectedRepo !== null && diff !== null ? (
        <div className="changes-manifest__workspace">
          <aside className="changes-manifest__files" aria-label="Changed files">
            <div className="changes-manifest__files-heading">
              <span>Files</span>
              <strong>{diff.files.length}</strong>
            </div>
            <div className="completion-workspace__files">
              {diff.files.map((file) => (
                <button
                  key={file.path + (file.oldPath ?? '')}
                  type="button"
                  className={`completion-workspace__file ${selectedFile === file.path ? 'is-selected' : ''}`}
                  onClick={() => handleFileSelect(file.path)}
                >
                  <span className="completion-workspace__file-op" data-op={file.operation}>
                    {FILE_OP_GLYPH[file.operation] ?? 'M'}
                  </span>
                  <span className="completion-workspace__file-path">{file.path}</span>
                  {file.addedLines !== undefined && file.removedLines !== undefined ? (
                    <span className="completion-workspace__file-lines">
                      +{file.addedLines} −{file.removedLines}
                    </span>
                  ) : null}
                  {file.binary ? (
                    <span className="completion-workspace__file-binary">binary</span>
                  ) : null}
                </button>
              ))}
              {diff.files.length === 0 && !diff.partialFailure ? (
                <p className="completion-workspace__no-changes">
                  No local changes in this repository.
                </p>
              ) : null}
            </div>
          </aside>

          <section className="changes-manifest__preview" aria-label="File difference">
            <header className="changes-manifest__preview-toolbar">
              <div>
                <span>Selected file</span>
                <strong>{selectedFile ?? 'Choose a file'}</strong>
              </div>
              <div className="changes-manifest__preview-controls">
                {diff.partialFailure ? (
                  <span className="completion-workspace__partial-failure">
                    {diff.partialFailure}
                  </span>
                ) : null}
                {diff.truncated ? (
                  <span className="completion-workspace__truncated">Truncated</span>
                ) : null}
                <div role="group" aria-label="Diff layout" className="changes-manifest__layout">
                  <button
                    type="button"
                    aria-pressed={diffLayout === 'unified'}
                    onClick={() => setDiffLayoutOverride('unified')}
                  >
                    Unified
                  </button>
                  <button
                    type="button"
                    aria-pressed={diffLayout === 'side-by-side'}
                    onClick={() => setDiffLayoutOverride('side-by-side')}
                  >
                    Split
                  </button>
                </div>
              </div>
            </header>

            {selectedFile !== null && fileLoading ? (
              <div className="changes-manifest__file-loading">
                <span />
                <span />
                <span />
              </div>
            ) : null}

            {selectedFile !== null && !fileLoading && fileDiff !== null ? (
              <div className="completion-workspace__file-diff">
                <DiffViewer diffText={fileDiff} renderSideBySide={diffLayout === 'side-by-side'} />
              </div>
            ) : null}

            {selectedFile !== null && !fileLoading && fileDiff === null ? (
              <div className="completion-workspace__file-placeholder">
                No diff content is available for this file.
              </div>
            ) : null}

            {selectedFile === null ? (
              <div className="changes-manifest__empty-preview">
                <span aria-hidden="true">↳</span>
                <p>Select a changed file to inspect its patch.</p>
              </div>
            ) : null}
          </section>
        </div>
      ) : null}
    </section>
  );
}

function ChangesManifestSkeleton(): React.ReactElement {
  return (
    <div className="changes-manifest__skeleton" aria-label="Loading change manifest" role="status">
      <span />
      <div>
        <span />
        <span />
      </div>
    </div>
  );
}

function DiffSkeleton(): React.ReactElement {
  return (
    <div className="changes-manifest__diff-skeleton" aria-label="Loading repository changes">
      <div>
        {Array.from({ length: 5 }, (_, index) => (
          <span key={index} />
        ))}
      </div>
      <div>
        {Array.from({ length: 8 }, (_, index) => (
          <span key={index} />
        ))}
      </div>
    </div>
  );
}
