/**
 * Renderer bridge to the server-owned review-feedback pending draft. This
 * module anchors the feature's wire surface to `window.agentico` in one
 * place: the fetch returns the revisioned draft view (`revision`, grouped
 * comments with `stableRef`/`selected`/`createdAt`), selection writes carry
 * only references plus the expected revision, and launch is constant size.
 */
import type { ReviewFeedbackCommentView } from '../../../../shared/ipc';

/** One comment inside the revisioned pending-draft view. */
export interface ReviewFeedbackDraftCommentView extends ReviewFeedbackCommentView {
  /** Stable identity: repo + supported comment type + GitHub database ID. */
  stableRef: string;
  /** The committed draft selection. */
  selected: boolean;
  /** GitHub creation timestamp; humanized at render time. */
  createdAt?: string;
}

export interface ReviewFeedbackDraftRepoGroup {
  repo: string;
  prUrl: string;
  comments: ReviewFeedbackDraftCommentView[];
}

/** The durable pending draft, as fetched or acknowledged by the server. */
export interface ReviewFeedbackDraftView {
  revision: number;
  snapshotId?: string;
  repos: ReviewFeedbackDraftRepoGroup[];
}

export interface ReviewFeedbackSelectionUpdate {
  stableRef: string;
  selected: boolean;
}

export interface ReviewFeedbackLaunchResult {
  /** Identity of the created review-feedback child (`child_id ?? feature_id`). */
  childId: string;
  parentId: string;
  changed?: number;
  omitted?: number;
  deferred?: number;
}

/** The preload shape this feature binds against. */
interface ReviewFeedbackPreloadBridge {
  fetchReviewFeedback(request: { featureId: string }): Promise<ReviewFeedbackDraftView>;
  updateReviewFeedbackSelection(request: {
    featureId: string;
    expectedRevision: number;
    updates: ReviewFeedbackSelectionUpdate[];
  }): Promise<Pick<ReviewFeedbackDraftView, 'revision' | 'repos'>>;
  launchReviewFeedbackChild(request: {
    parentId: string;
    expectedRevision: number;
    gate?: boolean;
  }): Promise<{
    featureId?: string;
    childId?: string;
    parentId: string;
    changed?: number;
    omitted?: number;
    deferred?: number;
  }>;
}

function bridge(): ReviewFeedbackPreloadBridge {
  return window.agentico as unknown as ReviewFeedbackPreloadBridge;
}

export function fetchReviewFeedbackDraft(featureId: string): Promise<ReviewFeedbackDraftView> {
  return bridge().fetchReviewFeedback({ featureId });
}

export function saveReviewFeedbackSelection(request: {
  featureId: string;
  expectedRevision: number;
  updates: ReviewFeedbackSelectionUpdate[];
}): Promise<Pick<ReviewFeedbackDraftView, 'revision' | 'repos'>> {
  return bridge().updateReviewFeedbackSelection(request);
}

export function launchReviewFeedbackDraft(request: {
  parentId: string;
  expectedRevision: number;
  gate?: boolean;
}): Promise<ReviewFeedbackLaunchResult> {
  return bridge()
    .launchReviewFeedbackChild(request)
    .then((result) => ({
      childId: result.childId ?? result.featureId ?? '',
      parentId: result.parentId,
      ...(result.changed === undefined ? {} : { changed: result.changed }),
      ...(result.omitted === undefined ? {} : { omitted: result.omitted }),
      ...(result.deferred === undefined ? {} : { deferred: result.deferred }),
    }));
}

/**
 * The launch receipt counted at reconcile time: selected references whose
 * content changed, selected references deleted before launch, and fresh
 * comments deferred to a later pass. Non-blocking information only; undefined
 * when every count is zero or absent.
 */
export function launchReceiptText(result: {
  changed?: number;
  omitted?: number;
  deferred?: number;
}): string | undefined {
  const clauses: string[] = [];
  if (result.changed !== undefined && result.changed > 0) clauses.push(`${result.changed} changed`);
  if (result.omitted !== undefined && result.omitted > 0) clauses.push(`${result.omitted} omitted`);
  if (result.deferred !== undefined && result.deferred > 0)
    clauses.push(`${result.deferred} deferred`);
  if (clauses.length === 0) return undefined;
  return `${clauses.join(', ')} since review`;
}
