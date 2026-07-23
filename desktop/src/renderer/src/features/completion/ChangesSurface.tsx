import { useState, useCallback, useRef } from 'react';
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

  return (
    <section className="completion-workspace__inspect" aria-label="Changes">
      {error !== null ? (
        <div className="completion-workspace__error" role="alert">
          {error}
          <button type="button" onClick={onRetry}>
            Retry
          </button>
        </div>
      ) : null}
      {loading ? <div className="completion-workspace__loading">Loading changes…</div> : null}
      {diffError !== null ? (
        <div className="completion-workspace__error" role="alert">
          {diffError}
        </div>
      ) : null}
      {preflight !== null ? (
        <div className="completion-workspace__repos">
          <h3>Repositories</h3>
          {preflight.repos.map((repo) => (
            <div
              key={repo.repo}
              className={`completion-workspace__repo ${selectedRepo === repo.repo ? 'is-selected' : ''}`}
            >
              <button
                type="button"
                className="completion-workspace__repo-select"
                onClick={() => handleRepoSelect(repo.repo)}
              >
                <span className="completion-workspace__repo-name">{repo.repo}</span>
                <span className="completion-workspace__repo-status" data-status={repo.status}>
                  {STATUS_LABELS[repo.status] ?? repo.status}
                </span>
              </button>
              {repo.prUrl !== undefined ? (
                <PrLinkButton url={repo.prUrl} openExternal={openExternal} />
              ) : null}
              <button
                type="button"
                className="completion-workspace__reveal"
                onClick={() => void revealPath(featureId, repo.repo)}
              >
                Reveal
              </button>
            </div>
          ))}
        </div>
      ) : null}

      {selectedRepo && diffLoading && (
        <div className="completion-workspace__diff-loading">Loading diff…</div>
      )}

      {selectedRepo && diff && (
        <div className="completion-workspace__diff">
          <div className="completion-workspace__diff-toolbar">
            <label className="completion-workspace__layout-toggle">
              <input
                type="checkbox"
                checked={diffLayout === 'side-by-side'}
                onChange={(e) =>
                  setDiffLayoutOverride(e.target.checked ? 'side-by-side' : 'unified')
                }
              />
              Side-by-side
            </label>
            {diff.partialFailure && (
              <span className="completion-workspace__partial-failure">{diff.partialFailure}</span>
            )}
            {diff.truncated && <span className="completion-workspace__truncated">Truncated</span>}
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
                {file.addedLines !== undefined && file.removedLines !== undefined && (
                  <span className="completion-workspace__file-lines">
                    +{file.addedLines} −{file.removedLines}
                  </span>
                )}
                {file.binary && <span className="completion-workspace__file-binary">binary</span>}
              </button>
            ))}
            {diff.files.length === 0 && !diff.partialFailure && (
              <p className="completion-workspace__no-changes">No changes</p>
            )}
          </div>

          {selectedFile && fileLoading && (
            <div className="completion-workspace__file-loading">Loading file diff…</div>
          )}

          {selectedFile && !fileLoading && fileDiff && (
            <div className="completion-workspace__file-diff">
              <DiffViewer diffText={fileDiff} renderSideBySide={diffLayout === 'side-by-side'} />
            </div>
          )}

          {selectedFile && !fileLoading && !fileDiff && (
            <div className="completion-workspace__file-placeholder">
              No diff content available for this file.
            </div>
          )}
        </div>
      )}
    </section>
  );
}
