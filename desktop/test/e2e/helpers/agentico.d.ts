/**
 * Minimal ambient typing of the preload surface for page.evaluate callbacks.
 * The real contract lives in src/shared/ipc.ts; journeys only assert against
 * the subset below and treat everything else as opaque JSON.
 */
export {};

interface ConnectionStatusLite {
  status: string;
  stage: string;
  detail: string;
  ownership: string;
  serverBuild?: { version: string; revision?: string };
  error?: { code: string; message: string; remediation?: string };
}

interface ReadinessLite {
  ready: boolean;
  providers: { name: string; installed: boolean; ready: boolean }[];
  models: { available: boolean; models?: string[] };
  workspaceRoots: { path: string; valid: boolean }[];
  repositories: { name: string; path: string; valid: boolean }[];
  issues: { code: string; message: string; remedy?: string }[];
}

interface FeatureSnapshotLite {
  id: string;
  name: string;
  status: string;
  currentPhase: string;
  repos: string[];
  setup?: {
    status: string;
    attempt: number;
    tasks: {
      key: string;
      status: string;
      repo?: string;
      branch?: string;
      attempt: number;
      error?: string;
    }[];
    lastError?: string;
  };
  actions: { id: string; enabled: boolean; disabledReasons: { code: string; message: string }[] }[];
}

interface SessionSummaryLite {
  id: string;
  featureId: string;
  phase: string;
  status: string;
}

interface SessionTranscriptLite {
  cursor: { start: number; end: number; total?: number };
  messages: { index: number; role: string; type: string; text?: string }[];
}

interface AttentionItemLite {
  kind: 'permission' | 'questions' | 'help' | 'gate' | 'review';
  id: string;
  featureId?: string;
  sessionId?: string;
  repoName?: string;
  cycleType?: string;
}

interface ReviewSessionLite {
  featureId: string;
  reviewId: string;
  draftRevision: string;
  text: string;
}

declare global {
  interface Window {
    agentico: {
      getConnectionStatus(): Promise<ConnectionStatusLite>;
      getReadiness(): Promise<ReadinessLite>;
      refreshReadiness(): Promise<ReadinessLite>;
      listFeatures(): Promise<{ id: string; name: string; status: string }[]>;
      getFeature(featureId: string): Promise<FeatureSnapshotLite>;
      dispatchFeatureAction(request: {
        featureId: string;
        action: 'start' | 'pause-stop';
      }): Promise<{ result: string }>;
      listSessions(): Promise<SessionSummaryLite[]>;
      getSessionTranscript(request: {
        sessionId: string;
        offset?: number;
        limit?: number;
      }): Promise<SessionTranscriptLite>;
      getAttention(): Promise<{ items: AttentionItemLite[] }>;
      answerPermission(input: {
        requestId: string;
        sessionId?: string;
        decision: 'allow_once' | 'allow_remember' | 'deny';
        rememberPattern?: string;
        rememberScope?: string;
      }): Promise<{ result: string; notice?: string; alreadyResolved?: boolean }>;
      createFeature(input: {
        name: string;
        description: string;
        repoKeys: string[];
        useCurrentBranch: boolean;
      }): Promise<{ featureId: string }>;
      getSettings(): Promise<{
        tabs: { open: { featureId: string; titleHint: string }[]; activeFeatureId: string | null };
      }>;
      readReview(input: { featureId: string }): Promise<ReviewSessionLite>;
      saveReview(input: {
        featureId: string;
        reviewId: string;
        baseRevision: string;
        text: string;
      }): Promise<{ type: 'saved' | 'conflict' }>;
      loadLocalReviewDraft(input: {
        runtimeId: string;
        featureId: string;
        reviewId: string;
        baseDraftRevision?: string;
      }): Promise<{ text: string } | null>;
    };
  }
}
