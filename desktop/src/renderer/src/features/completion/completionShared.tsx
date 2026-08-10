import { useState, useCallback, useMemo } from 'react';
import { E_REQUEST_TIMEOUT } from '../../../../shared/errors';
import { parseIpcError } from '../../wizard/ipcError';

export type DiffLayout = 'side-by-side' | 'unified';
export type CompletionAction = 'publish' | 'merge' | 'mark-done' | 'cleanup' | 'delete';

/**
 * `reconciling` is not a failure: the mutation outran its request bound and is
 * still running server-side, so its outcome arrives through the feature refresh.
 */
export type ActionResult =
  | { ok: true; result: string }
  | {
      ok: false;
      code: string;
      message: string;
      remediation?: string;
      reconciling?: boolean;
    };

export const STATUS_LABELS: Record<string, string> = {
  eligible: 'Eligible',
  already_published: 'Already published',
  completed: 'Completed',
  ineligible: 'Local only',
  untouched: 'No changes',
  blocked: 'Blocked',
  unpublished_changes: 'Unpublished changes',
  unmerged_changes: 'Unmerged changes',
};

export function isEligibleForPublish(repo: {
  publishable: boolean;
  status: string;
  touched: boolean;
}): boolean {
  return repo.publishable && repo.status === 'eligible' && repo.touched;
}

export const FILE_OP_GLYPH: Record<string, string> = {
  add: '+',
  delete: '−',
  rename: '→',
  modify: 'M',
};

interface DiffLine {
  type: 'add' | 'delete' | 'context' | 'hunk';
  text: string;
}

function parseDiffLines(diffText: string): DiffLine[] {
  return diffText.split('\n').map((line) => {
    if (line.startsWith('@@')) return { type: 'hunk', text: line };
    if (line.startsWith('+') && !line.startsWith('+++'))
      return { type: 'add', text: line.slice(1) };
    if (line.startsWith('-') && !line.startsWith('---'))
      return { type: 'delete', text: line.slice(1) };
    if (line.startsWith(' ')) return { type: 'context', text: line.slice(1) };
    return { type: 'context', text: line };
  });
}

export function DiffViewer({
  diffText,
  renderSideBySide,
}: {
  diffText: string;
  renderSideBySide: boolean;
}) {
  const lines = useMemo(() => parseDiffLines(diffText), [diffText]);
  if (renderSideBySide) {
    const leftLines: DiffLine[] = [];
    const rightLines: DiffLine[] = [];
    for (const line of lines) {
      if (line.type === 'delete') {
        leftLines.push(line);
        rightLines.push({ type: 'context', text: '' });
      } else if (line.type === 'add') {
        leftLines.push({ type: 'context', text: '' });
        rightLines.push(line);
      } else {
        leftLines.push(line);
        rightLines.push(line);
      }
    }
    return (
      <div className="completion-workspace__diff-pane-container">
        <div className="completion-workspace__diff-pane">
          <span className="completion-workspace__diff-pane-label">Original</span>
          <pre className="completion-workspace__diff-content">
            {leftLines.map((line, i) => (
              <span key={i} className={`completion-workspace__diff-line--${line.type}`}>
                {line.text || '\n'}
              </span>
            ))}
          </pre>
        </div>
        <div className="completion-workspace__diff-pane">
          <span className="completion-workspace__diff-pane-label">Modified</span>
          <pre className="completion-workspace__diff-content">
            {rightLines.map((line, i) => (
              <span key={i} className={`completion-workspace__diff-line--${line.type}`}>
                {line.text || '\n'}
              </span>
            ))}
          </pre>
        </div>
      </div>
    );
  }
  return (
    <pre className="completion-workspace__diff-content">
      {lines.map((line, i) => (
        <span key={i} className={`completion-workspace__diff-line--${line.type}`}>
          {line.type === 'add' ? '+ ' : line.type === 'delete' ? '- ' : ''}
          {line.text || '\n'}
        </span>
      ))}
    </pre>
  );
}

export function useCompletionAction() {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ActionResult | null>(null);
  const run = useCallback(
    async (thunk: () => Promise<string>, refresh?: () => Promise<void>): Promise<boolean> => {
      setBusy(true);
      setResult(null);
      try {
        const resultStr = await thunk();
        if (refresh) await refresh();
        setResult({ ok: true, result: resultStr });
        return true;
      } catch (err) {
        const parsed = parseIpcError(err);
        if (parsed.code === E_REQUEST_TIMEOUT) {
          // The server keeps working; repoll so the true outcome surfaces.
          try {
            if (refresh) await refresh();
          } catch {
            // The cockpit converges through its own invalidation path.
          }
          setResult({
            ok: false,
            code: parsed.code,
            message: parsed.message,
            ...(parsed.remediation === undefined ? {} : { remediation: parsed.remediation }),
            reconciling: true,
          });
          return false;
        }
        setResult({
          ok: false,
          code: parsed.code,
          message: parsed.message,
          ...(parsed.remediation === undefined ? {} : { remediation: parsed.remediation }),
        });
        return false;
      } finally {
        setBusy(false);
      }
    },
    [],
  );
  // A reconciling action must not re-arm its trigger: the mutation is still in
  // flight on the server, and dispatching again would repeat it.
  const reconciling = result !== null && !result.ok && result.reconciling === true;
  return { busy, reconciling, result, run };
}

export function PrLinkButton({
  url,
  openExternal,
}: {
  url: string;
  openExternal: (url: string) => Promise<{ ok: boolean }>;
}) {
  return (
    <button
      type="button"
      className="completion-workspace__pr-link"
      onClick={() => void openExternal(url)}
    >
      PR ↗
    </button>
  );
}

export function ResultBox({ result }: { result: ActionResult | null }) {
  if (result === null) return null;
  if (result.ok) {
    return (
      <div
        className="completion-workspace__result completion-workspace__result--success"
        role="status"
      >
        <span className="completion-workspace__result-icon" aria-hidden="true">
          {'✓'}
        </span>
        <span className="completion-workspace__result-text">{result.result}</span>
      </div>
    );
  }
  if (result.reconciling === true) {
    return (
      <div
        className="completion-workspace__result completion-workspace__result--reconciling"
        role="status"
      >
        <span className="completion-workspace__result-icon" aria-hidden="true">
          {'⟳'}
        </span>
        <span className="completion-workspace__result-text">
          still running on the server — reconciling: {result.message}
        </span>
      </div>
    );
  }
  return (
    <div
      className="completion-workspace__result completion-workspace__result--failure"
      role="alert"
    >
      <span className="completion-workspace__result-icon" aria-hidden="true">
        {'⚠'}
      </span>
      <span className="completion-workspace__result-text">failed: {result.message}</span>
    </div>
  );
}
