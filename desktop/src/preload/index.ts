/**
 * Sandboxed preload. Exposes exactly one narrow, task-specific API
 * (`window.agentico`) via the context bridge. There is no generic invoke,
 * no channel parameter accepted from the renderer, and no token, URL, node,
 * or process material in scope.
 */
import { contextBridge, ipcRenderer } from 'electron';
import {
  AppEventSchema,
  SessionOutputEventSchema,
  ConnectionStateSchema,
  IPC_CHANNELS,
  IPC_EVENTS,
  IpcEnvelopeSchema,
  type AgenticoApi,
  type AppEvent,
  type ConnectionState,
  type CreateFeatureInput,
  type FeatureActionRequest,
  type InitRepositoryRequest,
  type SettingsPatch,
  type ThemePreference,
  type SessionOutputOpenRequest,
  type SessionOutputOpenResult,
  type SessionOutputEvent,
  type LocalReviewDraftSaveRequest,
  type LocalReviewDraftLookupRequest,
  type LocalReviewDraftDiscardRequest,
  type ReviewReadRequest,
  type ReviewSaveRequest,
  type ReviewValidateRequest,
  type ReviewDecisionRequest,
  type ResourceValidateRequest,
  type ResourceWriteRequest,
  type LocalResourceDraftSaveRequest,
  type LocalResourceDraftLookupRequest,
  type LocalResourceDraftDiscardRequest,
  type RunListRequest,
  type RunGetRequest,
  type RunArtifactsListRequest,
  type RunArtifactContentRequest,
  type RunLogContentRequest,
  type RewindPreviewRequest,
  type RewindExecuteRequest,
  type RebaseRequest,
  type RebasePreflightRequest,
  type ReviewCommentsFetchRequest,
  type ReviewCommentsStartRequest,
  type RefactorRequest,
  type RefactorPreflightRequest,
  type RecoveryExecuteRequest,
  type RecoveryLogReadRequest,
  type CompletionPreflightRequest,
  type PublishDescriptionRequest,
  type RepositoryDiffRequest,
  type OpenExternalRequest,
  type RevealPathRequest,
} from '../shared/ipc';
import { assertNoPrototypePollution } from '../shared/sanitize';

/** Invokes a fixed channel and unwraps the validated envelope, failing closed. */
async function call<T>(channel: string, ...args: unknown[]): Promise<T> {
  const raw: unknown = await ipcRenderer.invoke(channel, ...args);
  const parsed = IpcEnvelopeSchema.safeParse(raw);
  if (!parsed.success) {
    throw new Error('E_IPC_PROTOCOL: The main process returned an unrecognized response.');
  }
  if (!parsed.data.ok) {
    const { code, message, remediation } = parsed.data.error;
    throw new Error(`${code}: ${message}${remediation ? ` ${remediation}` : ''}`);
  }
  return parsed.data.value as T;
}

const api: AgenticoApi = {
  getConnectionStatus: () => call(IPC_CHANNELS.connectionGetStatus),
  retryConnection: () => call(IPC_CHANNELS.connectionRetry),
  restartConnection: () => call(IPC_CHANNELS.connectionRestart),
  onConnectionChanged: (listener: (state: ConnectionState) => void) => {
    const wrapped = (_event: unknown, payload: unknown): void => {
      try {
        assertNoPrototypePollution(payload);
      } catch {
        return; // drop unsafe events silently — fail closed
      }
      const state = ConnectionStateSchema.safeParse(payload);
      if (state.success) {
        listener(state.data);
      }
    };
    ipcRenderer.on(IPC_EVENTS.connectionChanged, wrapped);
    return () => {
      ipcRenderer.removeListener(IPC_EVENTS.connectionChanged, wrapped);
    };
  },
  getSettings: () => call(IPC_CHANNELS.settingsGet),
  updateSettings: (patch: SettingsPatch) => call(IPC_CHANNELS.settingsUpdate, patch),
  getThemePreference: () => call(IPC_CHANNELS.themeGet),
  setThemePreference: (preference: ThemePreference) => call(IPC_CHANNELS.themeSet, preference),
  getReadiness: () => call(IPC_CHANNELS.readinessGet),
  refreshReadiness: () => call(IPC_CHANNELS.readinessRefresh),
  pickWorkspaceDirectory: () => call(IPC_CHANNELS.workspacePickDirectory),
  addWorkspaceRoot: (path: string) => call(IPC_CHANNELS.workspaceAddRoot, path),
  removeWorkspaceRoot: (path: string) => call(IPC_CHANNELS.workspaceRemoveRoot, path),
  reorderWorkspaceRoots: (paths: string[]) => call(IPC_CHANNELS.workspaceReorderRoots, paths),
  initRepository: (request: InitRepositoryRequest) =>
    call(IPC_CHANNELS.workspaceInitRepository, request),
  listRepositories: () => call(IPC_CHANNELS.repositoriesList),
  listFeatures: () => call(IPC_CHANNELS.featuresList),
  getFeature: (featureId: string) => call(IPC_CHANNELS.featuresGet, featureId),
  createFeature: (input: CreateFeatureInput) => call(IPC_CHANNELS.featuresCreate, input),
  dispatchFeatureSetup: (featureId: string) => call(IPC_CHANNELS.featuresSetup, featureId),
  dispatchFeatureAction: (request: FeatureActionRequest) =>
    call(IPC_CHANNELS.featuresDispatchAction, request),
  getAttention: () => call(IPC_CHANNELS.attentionGet),
  answerPermission: (request) => call(IPC_CHANNELS.attentionAnswerPermission, request),
  answerQuestions: (request) => call(IPC_CHANNELS.attentionAnswerQuestions, request),
  sendHelp: (request) => call(IPC_CHANNELS.attentionSendHelp, request),
  saveGateDraft: (request) => call(IPC_CHANNELS.attentionSaveGateDraft, request),
  resolveGate: (request) => call(IPC_CHANNELS.attentionResolveGate, request),
  listSessions: () => call(IPC_CHANNELS.sessionsList),
  getSession: (sessionId: string) => call(IPC_CHANNELS.sessionsGet, sessionId),
  getSessionTranscript: (request) => call(IPC_CHANNELS.sessionsTranscript, request),
  openSessionOutput: (request: SessionOutputOpenRequest): Promise<SessionOutputOpenResult> =>
    call(IPC_CHANNELS.sessionsOutputOpen, request),
  cancelSessionOutput: (subscriptionId: string) =>
    call<{ cancelled: boolean }>(IPC_CHANNELS.sessionsOutputCancel, { subscriptionId }).then(
      ({ cancelled }) => cancelled,
    ),
  onSessionOutput: (listener: (event: SessionOutputEvent) => void) => {
    const wrapped = (_event: unknown, payload: unknown): void => {
      try {
        assertNoPrototypePollution(payload);
      } catch {
        return;
      }
      const event = SessionOutputEventSchema.safeParse(payload);
      if (event.success) listener(event.data);
    };
    ipcRenderer.on(IPC_EVENTS.sessionOutput, wrapped);
    return () => {
      ipcRenderer.removeListener(IPC_EVENTS.sessionOutput, wrapped);
    };
  },
  getCreationDefaults: () => call(IPC_CHANNELS.creationDefaults),
  loadLocalReviewDraft: (request: LocalReviewDraftLookupRequest) =>
    call(IPC_CHANNELS.reviewDraftsLoad, request),
  saveLocalReviewDraft: (request: LocalReviewDraftSaveRequest) =>
    call(IPC_CHANNELS.reviewDraftsSave, request),
  discardLocalReviewDraft: (request: LocalReviewDraftDiscardRequest) =>
    call<{ discarded: boolean }>(IPC_CHANNELS.reviewDraftsDiscard, request).then(
      ({ discarded }) => discarded,
    ),
  readReview: (request: ReviewReadRequest) => call(IPC_CHANNELS.reviewsRead, request),
  openReview: (request: ReviewReadRequest) => call(IPC_CHANNELS.reviewsOpen, request),
  saveReview: (request: ReviewSaveRequest) => call(IPC_CHANNELS.reviewsSave, request),
  validateReview: (request: ReviewValidateRequest) => call(IPC_CHANNELS.reviewsValidate, request),
  decideReview: (request: ReviewDecisionRequest) => call(IPC_CHANNELS.reviewsDecide, request),
  listResources: (kind?: string) => call(IPC_CHANNELS.resourcesCatalogue, kind),
  readResource: (resourceId: string) => call(IPC_CHANNELS.resourcesRead, resourceId),
  validateResource: (request: ResourceValidateRequest) =>
    call(IPC_CHANNELS.resourcesValidate, request),
  writeResource: (request: ResourceWriteRequest) => call(IPC_CHANNELS.resourcesWrite, request),
  loadLocalResourceDraft: (request: LocalResourceDraftLookupRequest) =>
    call(IPC_CHANNELS.resourceDraftsLoad, request),
  saveLocalResourceDraft: (request: LocalResourceDraftSaveRequest) =>
    call(IPC_CHANNELS.resourceDraftsSave, request),
  discardLocalResourceDraft: (request: LocalResourceDraftDiscardRequest) =>
    call<{ discarded: boolean }>(IPC_CHANNELS.resourceDraftsDiscard, request).then(
      ({ discarded }) => discarded,
    ),
  listRuns: (request: RunListRequest) => call(IPC_CHANNELS.runsList, request),
  getRun: (request: RunGetRequest) => call(IPC_CHANNELS.runsGet, request),
  listRunSessions: (request: RunGetRequest) => call(IPC_CHANNELS.runSessionsList, request),
  listRunArtifacts: (request: RunArtifactsListRequest) =>
    call(IPC_CHANNELS.runArtifactsList, request),
  getRunArtifactContent: (request: RunArtifactContentRequest) =>
    call(IPC_CHANNELS.runArtifactContent, request),
  getRunLogContent: (request: RunLogContentRequest) => call(IPC_CHANNELS.runLogContent, request),
  getRewindPreview: (request: RewindPreviewRequest) => call(IPC_CHANNELS.rewindPreview, request),
  executeRewind: (request: RewindExecuteRequest) => call(IPC_CHANNELS.rewindExecute, request),
  preflightCompletion: (request: CompletionPreflightRequest) =>
    call(IPC_CHANNELS.completionPreflight, request),
  getRepositoryDiff: (request: RepositoryDiffRequest) => call(IPC_CHANNELS.repositoryDiff, request),
  generatePublishDescription: (request: PublishDescriptionRequest) =>
    call(IPC_CHANNELS.publishDescription, request),
  openExternal: (request: OpenExternalRequest) => call(IPC_CHANNELS.openExternal, request),
  revealPath: (request: RevealPathRequest) => call(IPC_CHANNELS.revealPath, request),
  startRebase: (request: RebaseRequest) => call(IPC_CHANNELS.featuresRebase, request),
  preflightRebase: (request: RebasePreflightRequest) =>
    call(IPC_CHANNELS.featuresRebasePreflight, request),
  fetchReviewComments: (request: ReviewCommentsFetchRequest) =>
    call(IPC_CHANNELS.featuresReviewCommentsFetch, request),
  startReviewComments: (request: ReviewCommentsStartRequest) =>
    call(IPC_CHANNELS.featuresReviewCommentsStart, request),
  startRefactor: (request: RefactorRequest) => call(IPC_CHANNELS.featuresRefactor, request),
  preflightRefactor: (request: RefactorPreflightRequest) =>
    call(IPC_CHANNELS.featuresRefactorPreflight, request),
  scanRecovery: () => call(IPC_CHANNELS.recoveryScan),
  executeRecovery: (request: RecoveryExecuteRequest) => call(IPC_CHANNELS.recoveryExecute, request),
  readRecoveryLog: (request: RecoveryLogReadRequest) => call(IPC_CHANNELS.recoveryLogRead, request),
  bulkPreview: () => call(IPC_CHANNELS.bulkPreview),
  onAppEvent: (listener: (event: AppEvent) => void) => {
    const wrapped = (_event: unknown, payload: unknown): void => {
      try {
        assertNoPrototypePollution(payload);
      } catch {
        return; // drop unsafe events silently — fail closed
      }
      const event = AppEventSchema.safeParse(payload);
      if (event.success) {
        listener(event.data);
      }
    };
    ipcRenderer.on(IPC_EVENTS.appEvent, wrapped);
    return () => {
      ipcRenderer.removeListener(IPC_EVENTS.appEvent, wrapped);
    };
  },
};

contextBridge.exposeInMainWorld('agentico', api);
