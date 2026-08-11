/**
 * Sandboxed preload. Exposes exactly one narrow, task-specific API
 * (`window.agentico`) via the context bridge. There is no generic invoke,
 * no channel parameter accepted from the renderer, and no token, URL, node,
 * or process material in scope.
 */
import { contextBridge, ipcRenderer, webUtils } from 'electron';
import {
  AppEventSchema,
  AppRouteEventSchema,
  SessionOutputEventSchema,
  ConnectionStateSchema,
  CREATION_ATTACHMENT_LIMIT,
  CREATION_IMAGE_FORMATS,
  CREATION_IMAGE_LIMIT,
  IPC_CHANNELS,
  IPC_EVENTS,
  IpcEnvelopeSchema,
  type AgenticoApi,
  type AppEvent,
  type AppRouteEvent,
  type ConnectionState,
  type ChatStartRequest,
  type CreateFeatureInput,
  type CreationFileKind,
  type CreationFileSearchRequest,
  type FeatureActionRequest,
  type InitRepositoryRequest,
  type SettingsOpenRequest,
  type SettingsPatch,
  type ThemePreference,
  windowPurposeFromArgv,
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
  type FeatureConfigUpdateRequest,
  type WorkspaceDefaults,
  type RunListRequest,
  type RunGetRequest,
  type RunArtifactsListRequest,
  type RunArtifactContentRequest,
  type RunLogContentRequest,
  type RewindPreviewRequest,
  type RewindExecuteRequest,
  type LaunchRebaseChildRequest,
  type LaunchRefactorChildRequest,
  type DiscardRefactorChildRequest,
  type DeleteFeatureCascadeRequest,
  type FetchReviewFeedbackRequest,
  type LaunchReviewFeedbackChildRequest,
  type RecoveryExecuteRequest,
  type RecoveryLogReadRequest,
  type UpdateInstallNowRequest,
  type CompletionPreflightRequest,
  type PublishDescriptionRequest,
  type RepositoryDiffRequest,
  type OpenExternalRequest,
  type RevealPathRequest,
  type MainWindowUiState,
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
    const { code, message, remediation, details } = parsed.data.error;
    const error = new Error(`${code}: ${message}${remediation ? ` ${remediation}` : ''}`);
    Object.assign(error, { code, remediation, details });
    throw error;
  }
  return parsed.data.value as T;
}

const api: AgenticoApi = {
  // Preload retains a limited `process` even when sandboxed; reading it here
  // avoids an IPC round trip before first paint.
  platform: process.platform,
  // The sandboxed `process` shim carries `argv`, which is where
  // `webPreferences.additionalArguments` lands — so the window's purpose is
  // known synchronously and the entry point picks a root before first paint.
  windowPurpose: windowPurposeFromArgv(process.argv),
  getConnectionStatus: () => call(IPC_CHANNELS.connectionGetStatus),
  retryConnection: () => call(IPC_CHANNELS.connectionRetry),
  restartConnection: () => call(IPC_CHANNELS.connectionRestart),
  chooseConnectionServer: (request) => call(IPC_CHANNELS.connectionChooseServer, request),
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
  onRouteRequest: (listener: (event: AppRouteEvent) => void) => {
    const wrapped = (_event: unknown, payload: unknown): void => {
      try {
        assertNoPrototypePollution(payload);
      } catch {
        return;
      }
      const event = AppRouteEventSchema.safeParse(payload);
      if (event.success) {
        listener(event.data);
      }
    };
    ipcRenderer.on(IPC_EVENTS.routeRequested, wrapped);
    return () => {
      ipcRenderer.removeListener(IPC_EVENTS.routeRequested, wrapped);
    };
  },
  getSettings: () => call(IPC_CHANNELS.settingsGet),
  updateSettings: (patch: SettingsPatch) => call(IPC_CHANNELS.settingsUpdate, patch),
  openSettingsWindow: (request: SettingsOpenRequest) =>
    call(IPC_CHANNELS.windowOpenSettings, request),
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
  startChat: (request: ChatStartRequest) => call(IPC_CHANNELS.chatStart, request),
  endChat: () => call(IPC_CHANNELS.chatEnd),
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
  pickCreationFiles: (kind: CreationFileKind) => call(IPC_CHANNELS.creationPickFiles, kind),
  readClipboardImage: () => call(IPC_CHANNELS.clipboardReadImage),
  importDroppedCreationFiles: (kind: CreationFileKind, files: readonly File[]) => {
    const limit = kind === 'image' ? CREATION_IMAGE_LIMIT : CREATION_ATTACHMENT_LIMIT;
    const imageMimeTypes = new Set(CREATION_IMAGE_FORMATS.map((format) => format.mime));
    const paths = files.slice(0, limit).flatMap((file) => {
      if (
        kind === 'image' &&
        !imageMimeTypes.has(file.type as (typeof CREATION_IMAGE_FORMATS)[number]['mime'])
      )
        return [];
      const filePath = webUtils.getPathForFile(file);
      return filePath === '' ? [] : [filePath];
    });
    return { paths };
  },
  searchCreationFiles: (request: CreationFileSearchRequest) =>
    call(IPC_CHANNELS.creationSearchFiles, request),
  cancelCreationFileSearch: (requestId: string) =>
    call<{ cancelled: boolean }>(IPC_CHANNELS.creationCancelFileSearch, requestId).then(
      ({ cancelled }) => cancelled,
    ),
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
  getFeatureConfig: (featureId: string) => call(IPC_CHANNELS.configFeatureGet, featureId),
  updateFeatureConfig: (request: FeatureConfigUpdateRequest) =>
    call(IPC_CHANNELS.configFeatureUpdate, request),
  getWorkspaceDefaults: () => call(IPC_CHANNELS.configDefaultsGet),
  updateWorkspaceDefaults: (defaults: WorkspaceDefaults) =>
    call(IPC_CHANNELS.configDefaultsUpdate, defaults),
  getModelCatalogue: () => call(IPC_CHANNELS.configModelCatalogue),
  refreshProviderModels: (provider: string) =>
    call(IPC_CHANNELS.configProviderModelsRefresh, provider),
  listRuns: (request: RunListRequest) => call(IPC_CHANNELS.runsList, request),
  getRun: (request: RunGetRequest) => call(IPC_CHANNELS.runsGet, request),
  listRunSessions: (request: RunGetRequest) => call(IPC_CHANNELS.runSessionsList, request),
  getLivePreview: (featureId: string) => call(IPC_CHANNELS.livePreviewGet, featureId),
  listRunArtifacts: (request: RunArtifactsListRequest) =>
    call(IPC_CHANNELS.runArtifactsList, request),
  listRunLogs: (request: RunArtifactsListRequest) => call(IPC_CHANNELS.runLogsList, request),
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
  publishUiState: (state: MainWindowUiState) => call(IPC_CHANNELS.uiStatePublish, state),
  launchRebaseChild: (request: LaunchRebaseChildRequest) =>
    call(IPC_CHANNELS.featuresRebase, request),
  launchRefactorChild: (request: LaunchRefactorChildRequest) =>
    call(IPC_CHANNELS.featuresRefactor, request),
  discardRefactorChild: (request: DiscardRefactorChildRequest) =>
    call(IPC_CHANNELS.featuresRefactorDiscard, request),
  fetchReviewFeedback: (request: FetchReviewFeedbackRequest) =>
    call(IPC_CHANNELS.featuresReviewFeedbackFetch, request),
  launchReviewFeedbackChild: (request: LaunchReviewFeedbackChildRequest) =>
    call(IPC_CHANNELS.featuresReviewFeedbackLaunch, request),
  deleteFeatureCascade: (request: DeleteFeatureCascadeRequest) =>
    call(IPC_CHANNELS.featuresDeleteCascade, request),
  scanRecovery: () => call(IPC_CHANNELS.recoveryScan),
  executeRecovery: (request: RecoveryExecuteRequest) => call(IPC_CHANNELS.recoveryExecute, request),
  readRecoveryLog: (request: RecoveryLogReadRequest) => call(IPC_CHANNELS.recoveryLogRead, request),
  bulkPreview: () => call(IPC_CHANNELS.bulkPreview),
  getUpdates: () => call(IPC_CHANNELS.updatesGet),
  checkForUpdates: () => call(IPC_CHANNELS.updatesCheck),
  installUpdateWhenIdle: () => call(IPC_CHANNELS.updatesInstallWhenIdle),
  installUpdateNow: (request: UpdateInstallNowRequest) =>
    call(IPC_CHANNELS.updatesInstallNow, request),
  restartToUpdate: () => call(IPC_CHANNELS.updatesRestart),
  getDiagnostics: () => call(IPC_CHANNELS.diagnosticsGet),
  revealDiagnostics: () => call(IPC_CHANNELS.diagnosticsReveal),
  clearDiagnostics: () => call(IPC_CHANNELS.diagnosticsClear),
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
