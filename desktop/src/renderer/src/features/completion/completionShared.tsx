import { useState, useCallback, useMemo } from 'react';
import { parseIpcError } from '../../wizard/ipcError';

export type DiffLayout = 'side-by-side' | 'unified';
export type CompletionAction = 'publish' | 'merge' | 'mark-done' | 'cleanup' | 'delete';

export type ActionResult = { ok: true; result: string } | { ok: false; message: string };

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
        setResult({ ok: false, message: parseIpcError(err).message });
        return false;
      } finally {
        setBusy(false);
      }
    },
    [],
  );
  return { busy, result, run };
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
