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

import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from 'react';
import type { TranscriptMessage } from '../../../../shared/ipc';
import { CopyIcon } from '../../components/icons';
import { renderSanitizedMarkdown } from '../sanitizedMarkdown';
import {
  friendlyToolName,
  type ConversationItem,
  type ConversationNoticeTone,
  type SubagentActivity,
} from './conversation';

const NEAR_BOTTOM_PX = 40;
/** Scrolling within this distance of the top asks for earlier rows. */
const NEAR_TOP_PX = 160;

type VerdictOutcome = Extract<ConversationItem, { kind: 'verdict' }>['outcome'];

const VERDICT_MARKS: Readonly<Record<VerdictOutcome, string>> = {
  allowed: '✓',
  answered: '✓',
  denied: '✕',
  interrupted: '‖',
};

const NOTICE_MARKS: Readonly<Record<ConversationNoticeTone, string>> = {
  interrupted: '‖',
  failed: '✕',
  caveat: '!',
  neutral: '•',
};

function Notice({ item }: { item: Extract<ConversationItem, { kind: 'notice' }> }) {
  const [expanded, setExpanded] = useState(false);
  const summaryId = `${item.key}-summary`;
  return (
    <div className="conversation__notice-container">
      <p className="conversation__notice" data-tone={item.tone}>
        <span className="conversation__notice-mark" aria-hidden="true">
          {NOTICE_MARKS[item.tone]}
        </span>
        <span className="conversation__notice-text">{item.text}</span>
        {item.summary !== undefined ? (
          <button
            type="button"
            className="conversation__notice-toggle"
            aria-expanded={expanded}
            aria-controls={summaryId}
            onClick={() => setExpanded(!expanded)}
          >
            {expanded ? 'Hide summary' : 'Show summary'}
          </button>
        ) : null}
      </p>
      {item.summary !== undefined && expanded ? (
        <div id={summaryId} className="conversation__notice-summary">
          <div dangerouslySetInnerHTML={{ __html: renderSanitizedMarkdown(item.summary) }} />
          {item.summaryTruncated ? (
            <p className="conversation__notice-truncated">Summary truncated</p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** A quiet, hover-revealed control under each reply to copy its source text. */
function CopyMessageButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="conversation__message-copy"
      aria-label={copied ? 'Copied' : 'Copy message'}
      title="Copy message"
      onClick={() => {
        const clipboard = (navigator as Partial<Navigator>).clipboard;
        if (clipboard === undefined) return;
        void clipboard.writeText(text).then(
          () => {
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1600);
          },
          () => undefined,
        );
      }}
    >
      <CopyIcon />
      <span>{copied ? 'Copied' : 'Copy'}</span>
    </button>
  );
}

export function ActivityIndicator({
  labels,
  active,
  idleLabel,
}: {
  labels: string[];
  active: boolean;
  idleLabel: string;
}) {
  const shownLabels = labels.slice(-3);
  return (
    <div
      className="conversation__activity"
      data-active={active}
      role={active ? 'status' : undefined}
    >
      {active ? (
        <span className="conversation__thinking" aria-hidden="true">
          {Array.from({ length: 8 }, (_, index) => (
            <span key={index} />
          ))}
        </span>
      ) : (
        <span className="conversation__activity-mark" aria-hidden="true">
          ✓
        </span>
      )}
      <div className="conversation__activity-copy">
        <strong>{active ? 'Working' : 'Worked'}</strong>
        <span>{shownLabels.length > 0 ? shownLabels.join(' · ') : idleLabel}</span>
      </div>
    </div>
  );
}

function subagentDetail(agent: SubagentActivity): string {
  if (agent.state === 'running') {
    return agent.lastTool !== undefined && agent.lastTool !== ''
      ? `using ${friendlyToolName(agent.lastTool)}`
      : 'starting up';
  }
  if (agent.summary?.trim()) return agent.summary.trim();
  if (agent.state === 'failed') return 'Failed';
  return agent.state === 'cancelled' ? 'Cancelled' : 'Finished';
}

function subagentTally(agents: SubagentActivity[]): string {
  const running = agents.filter((agent) => agent.state === 'running').length;
  const failed = agents.filter((agent) => agent.state === 'failed').length;
  const cancelled = agents.filter((agent) => agent.state === 'cancelled').length;
  if (running > 0) return `${running} of ${agents.length} running`;
  if (failed > 0) return `${failed} of ${agents.length} failed`;
  return cancelled > 0 ? `${cancelled} of ${agents.length} cancelled` : 'all finished';
}

export function SubagentGroupCard({ agents }: { agents: SubagentActivity[] }) {
  const live = agents.some((agent) => agent.state === 'running');
  return (
    <article
      className="conversation__subagents"
      aria-label="Sub-agent activity"
      role={live ? 'status' : undefined}
    >
      <header className="conversation__subagents-header">
        <span className="conversation__subagents-title">Sub-agents</span>
        <span className="conversation__subagents-tally" data-live={live}>
          {subagentTally(agents)}
        </span>
      </header>
      <ul className="conversation__subagents-list">
        {agents.map((agent) => (
          <li key={agent.id} className="conversation__subagent" data-state={agent.state}>
            <span className="conversation__subagent-lamp" aria-hidden="true" />
            <span className="conversation__subagent-desc">
              {agent.description ?? agent.taskType ?? 'Delegated task'}
            </span>
            <span className="conversation__subagent-detail">{subagentDetail(agent)}</span>
          </li>
        ))}
      </ul>
    </article>
  );
}

export interface ConversationTranscriptProps {
  items: ConversationItem[];
  /** True while the active agent is still working (drives the live spinner). */
  waiting: boolean;
  idleLabel: string;
  ariaLabel: string;
  assistantName?: string;
  className?: string;
  emptyState?: ReactNode;
  /** Status/loading/error rows rendered above the conversation. */
  status?: ReactNode;
  /** Interactive rows rendered after the newest turn (e.g. a pending question). */
  trailing?: ReactNode;
  /** Bump to force a scroll to the newest row (e.g. after sending a message). */
  pinToBottomToken?: number;
  /** Rows rendered directly above the first conversation row (e.g. a load-earlier status). */
  top?: ReactNode;
  /**
   * Called when the viewport scrolls within a threshold of the top, or when
   * the rows do not fill the viewport; the caller decides whether more exists.
   */
  onNearTop?(): void;
  /** Holds the first visible row in place when rows are prepended above it. */
  anchorPrepend?: boolean;
}

interface PrependAnchor {
  row: Element;
  offset: number;
}

/**
 * Prepend anchoring: after every commit it remembers the first row visible
 * in the viewport and that row's offset from the viewport top (refreshed on
 * scroll as well). When a commit changes the first item's key and the
 * remembered row is still mounted, rows were added above it, so the scroll
 * position moves by exactly the distance the row was pushed down. Rows are
 * the siblings after the `rowsStart` marker, so status and top-slot rows
 * that come and go are never chosen as the anchor. Returns the capture
 * function for the scroll handler.
 */
export function usePrependAnchor(
  scrollRef: RefObject<HTMLElement | null>,
  rowsStartRef: RefObject<HTMLElement | null>,
  firstKey: string | undefined,
  enabled: boolean,
): () => void {
  const anchor = useRef<PrependAnchor | null>(null);
  const previousFirstKey = useRef(firstKey);

  const capture = useCallback(() => {
    anchor.current = null;
    const container = scrollRef.current;
    const start = rowsStartRef.current;
    if (!enabled || container === null || start === null) return;
    const viewportTop = container.getBoundingClientRect().top;
    for (let row = start.nextElementSibling; row !== null; row = row.nextElementSibling) {
      const bounds = row.getBoundingClientRect();
      if (bounds.bottom > viewportTop) {
        anchor.current = { row, offset: bounds.top - viewportTop };
        return;
      }
    }
  }, [enabled, rowsStartRef, scrollRef]);

  useLayoutEffect(() => {
    const container = scrollRef.current;
    const held = anchor.current;
    if (
      enabled &&
      container !== null &&
      held !== null &&
      firstKey !== previousFirstKey.current &&
      held.row.isConnected
    ) {
      const offset = held.row.getBoundingClientRect().top - container.getBoundingClientRect().top;
      container.scrollTop += offset - held.offset;
    }
    previousFirstKey.current = firstKey;
    capture();
  });

  return capture;
}

type FileChange = NonNullable<TranscriptMessage['fileChange']>;
type DiffLineKind = 'added' | 'removed' | 'context' | 'meta';

/** Bounded cap on rendered diff lines; the rest collapses into a meta row. */
const MAX_DIFF_LINES = 24;

function fileChangeLabel(operation: string | undefined): string {
  switch (operation?.trim().toLocaleLowerCase()) {
    case 'add':
    case 'create':
    case 'write':
      return 'Created';
    case 'delete':
    case 'remove':
      return 'Deleted';
    case 'move':
    case 'rename':
      return 'Renamed';
    default:
      return 'Updated';
  }
}

function diffLineKind(line: string): DiffLineKind {
  if (
    line.startsWith('diff --git ') ||
    line.startsWith('index ') ||
    line.startsWith('@@') ||
    line.startsWith('--- ') ||
    line.startsWith('+++ ') ||
    line.startsWith('new file mode ') ||
    line.startsWith('deleted file mode ') ||
    line.startsWith('…') ||
    line === '...'
  ) {
    return 'meta';
  }
  if (line.startsWith('+')) return 'added';
  if (line.startsWith('-')) return 'removed';
  return 'context';
}

function isCreateOperation(operation: string | undefined): boolean {
  switch (operation?.trim().toLocaleLowerCase()) {
    case 'add':
    case 'create':
    case 'write':
      return true;
    default:
      return false;
  }
}

function isSyntheticReplacement(lines: string[]): boolean {
  return (
    lines.some((line) => line.trim() !== '') &&
    lines.every((line) => line.startsWith('+') || line.startsWith('-'))
  );
}

function visibleDiff(change: FileChange): string[] {
  const detail = change.detail?.trim();
  if (detail === undefined || detail === '') return [];
  if (/^Captured from (?:tool usage|tool activity|provider file change)\.$/.test(detail)) return [];
  const lines = detail.split('\n');
  if (
    change.hasDiffPatch !== true &&
    isCreateOperation(change.operation) &&
    !isSyntheticReplacement(lines)
  ) {
    return lines.map((line) => `+ ${line}`);
  }
  const looksLikeDiff =
    change.hasDiffPatch === true ||
    lines.some((line) => line.startsWith('+') || line.startsWith('-') || line.startsWith('@@'));
  return looksLikeDiff ? lines : [];
}

export function FileChangeCard({ change }: { change: FileChange }): React.ReactElement {
  const allLines = visibleDiff(change);
  const inferredAdded = allLines.filter((line) => diffLineKind(line) === 'added').length;
  const inferredRemoved = allLines.filter((line) => diffLineKind(line) === 'removed').length;
  const added = change.addedLines ?? inferredAdded;
  const removed = change.removedLines ?? inferredRemoved;
  const label = fileChangeLabel(change.operation);
  const lines =
    allLines.length > MAX_DIFF_LINES
      ? [...allLines.slice(0, MAX_DIFF_LINES), `… ${allLines.length - MAX_DIFF_LINES} more lines`]
      : allLines;

  return (
    <article className="conversation__file-change" aria-label={`${label} ${change.path}`}>
      <header className="conversation__file-change-header">
        <span className="conversation__file-change-path" title={change.path}>
          {change.oldPath ? `${change.oldPath} → ` : null}
          {change.path}
        </span>
        <span className="conversation__file-change-status">{label.toLocaleLowerCase()}</span>
        {added > 0 || removed > 0 ? (
          <span
            className="conversation__file-change-stats"
            aria-label={`${added} lines added, ${removed} lines removed`}
          >
            {added > 0 ? <span data-kind="added">+{added}</span> : null}
            {removed > 0 ? <span data-kind="removed">−{removed}</span> : null}
          </span>
        ) : null}
      </header>
      {lines.length > 0 ? (
        <div className="conversation__diff" role="region" aria-label={`Diff for ${change.path}`}>
          {lines.map((line, index) => {
            const kind = diffLineKind(line);
            return (
              <div key={`${index}-${line}`} className="conversation__diff-line" data-kind={kind}>
                <span className="conversation__diff-marker" aria-hidden="true">
                  {kind === 'added' ? '+' : kind === 'removed' ? '−' : ' '}
                </span>
                <code>{kind === 'added' || kind === 'removed' ? line.slice(1) : line}</code>
              </div>
            );
          })}
        </div>
      ) : null}
    </article>
  );
}

/** Shared conversational renderer for the supervisor and the current-run live preview. */
export function ConversationTranscript({
  items,
  waiting,
  idleLabel,
  ariaLabel,
  assistantName = 'Agentico',
  className,
  emptyState,
  status,
  trailing,
  pinToBottomToken,
  top,
  onNearTop,
  anchorPrepend = false,
}: ConversationTranscriptProps) {
  const scrollRef = useRef<HTMLElement>(null);
  const rowsStartRef = useRef<HTMLSpanElement>(null);
  const stickToBottom = useRef(true);
  const lastItem = items.at(-1);
  const hasTrailing = trailing !== undefined && trailing !== null;
  const captureAnchor = usePrependAnchor(scrollRef, rowsStartRef, items[0]?.key, anchorPrepend);

  useEffect(() => {
    const element = scrollRef.current;
    if (element !== null && stickToBottom.current) element.scrollTop = element.scrollHeight;
  }, [items, waiting, hasTrailing]);

  useEffect(() => {
    if (pinToBottomToken === undefined) return;
    stickToBottom.current = true;
    const element = scrollRef.current;
    if (element !== null) element.scrollTop = element.scrollHeight;
  }, [pinToBottomToken]);

  // Rows too few to scroll can never be scrolled near the top: ask at once.
  useEffect(() => {
    const element = scrollRef.current;
    if (
      onNearTop !== undefined &&
      element !== null &&
      element.scrollHeight <= element.clientHeight
    ) {
      onNearTop();
    }
  });

  return (
    <section
      ref={scrollRef}
      className={
        className === undefined ? 'conversation__scroll' : `conversation__scroll ${className}`
      }
      aria-label={ariaLabel}
      aria-live="polite"
      tabIndex={0}
      onScroll={(event) => {
        const element = event.currentTarget;
        stickToBottom.current =
          element.scrollHeight - element.scrollTop - element.clientHeight < NEAR_BOTTOM_PX;
        captureAnchor();
        if (onNearTop !== undefined && element.scrollTop <= NEAR_TOP_PX) onNearTop();
      }}
    >
      {status}
      {items.length === 0 && !waiting ? (emptyState ?? null) : null}
      {top}
      {anchorPrepend ? <span ref={rowsStartRef} hidden /> : null}
      {items.map((item, index) =>
        item.kind === 'message' ? (
          <article
            key={item.key}
            className="conversation__message"
            data-role={item.role}
            aria-label={item.role === 'user' ? 'You' : assistantName}
          >
            {item.role === 'assistant' ? (
              <>
                <div
                  className="conversation__markdown"
                  dangerouslySetInnerHTML={{ __html: renderSanitizedMarkdown(item.text) }}
                />
                <CopyMessageButton text={item.text} />
              </>
            ) : item.text !== '' ? (
              <p>{item.text}</p>
            ) : null}
            {item.attachments !== undefined && item.attachments.length > 0 ? (
              <ol className="composer__chips conversation__attachments" aria-label="Attachments">
                {item.attachments.map((attachment, index) => (
                  <li
                    key={`${String(index)}:${attachment.name}`}
                    className="composer__chip"
                    data-kind={attachment.kind === 'image' ? 'image' : 'attachment'}
                  >
                    <span>
                      {attachment.kind === 'image' ? '🖼' : '📎'} {attachment.name}
                    </span>
                  </li>
                ))}
              </ol>
            ) : null}
            {item.footer !== undefined ? (
              <span className="conversation__message-footer">{item.footer}</span>
            ) : null}
          </article>
        ) : item.kind === 'auto-pick' ? (
          <article
            key={item.key}
            className="conversation__auto-pick"
            aria-label="Auto-picked response"
          >
            <p>{item.text}</p>
          </article>
        ) : item.kind === 'status' ? (
          <article key={item.key} className="conversation__status" role="status">
            <span aria-hidden="true">✓</span>
            <p>{item.text}</p>
          </article>
        ) : item.kind === 'file-change' ? (
          <FileChangeCard key={item.key} change={item.change} />
        ) : item.kind === 'subagents' ? (
          <SubagentGroupCard key={item.key} agents={item.agents} />
        ) : item.kind === 'verification-tick' ? (
          <p key={item.key} className="conversation__verification-tick" data-tone={item.tone}>
            <span aria-hidden="true">{item.symbol}</span> {item.name}
          </p>
        ) : item.kind === 'verdict' ? (
          <p key={item.key} className="conversation__verdict" data-outcome={item.outcome}>
            <span className="conversation__verdict-mark" aria-hidden="true">
              {VERDICT_MARKS[item.outcome]}
            </span>
            <span className="conversation__verdict-text">{item.text}</span>
          </p>
        ) : item.kind === 'notice' ? (
          <Notice key={item.key} item={item} />
        ) : (
          <ActivityIndicator
            key={item.key}
            labels={item.labels}
            idleLabel={idleLabel}
            active={waiting && index === items.length - 1}
          />
        ),
      )}
      {waiting && lastItem?.kind !== 'activity' ? (
        <ActivityIndicator
          labels={(() => {
            const running =
              lastItem?.kind === 'subagents'
                ? lastItem.agents.filter((agent) => agent.state === 'running').length
                : 0;
            return running > 0
              ? [`waiting on ${running} sub-agent${running === 1 ? '' : 's'}`]
              : [];
          })()}
          idleLabel={idleLabel}
          active
        />
      ) : null}
      {trailing}
    </section>
  );
}
