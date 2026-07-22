/**
 * Registers every IPC handler from the central registry. Each handler:
 *   1. validates the sender (main-window webContents + app origin),
 *   2. size-checks and pollution-scans the payload,
 *   3. validates arguments against the channel's request schema,
 *   4. invokes the service,
 *   5. validates the response against the channel's response schema,
 * and always resolves to a typed { ok } envelope — exceptions never cross
 * the boundary unredacted.
 */
import { toSafeError } from '../shared/errors';
import { validateWithSchema } from '../shared/api/parse';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import {
  IPC_CHANNELS,
  IPC_EVENTS,
  type ConnectionState,
  type CreateFeatureInput,
  type CreateFeatureResult,
  type CreationDefaults,
  type CreationFileKind,
  type CreationFileSearchRequest,
  type CreationFileSearchResult,
  type PickedCreationFiles,
  type FeatureSnapshot,
  type FeatureSummaryView,
  type FeatureActionRequest,
  type FeatureActionResult,
  type InitRepositoryRequest,
  type IpcChannel,
  type IpcEnvelope,
  type PickedDirectory,
  type ReadinessSnapshot,
  type RepositoryState,
  type Settings,
  type SettingsPatch,
  type SetupDispatchResult,
  type ThemeInfo,
  type ThemePreference,
  type SessionSummary,
  type SessionDetail,
  type SessionTranscriptRequest,
  type SessionTranscript,
  type SessionOutputOpenRequest,
  type SessionOutputEvent,
  type AttentionSnapshot,
  type PermissionDecisionRequest,
  type AskUserAnswerRequest,
  type HelpAnswerRequest,
  type GateDraftRequest,
  type GateResolutionRequest,
  type AttentionActionResult,
  type ChatActionResult,
  type ChatStartRequest,
  type LocalReviewDraft,
  type LocalReviewDraftSaveRequest,
  type LocalReviewDraftLookupRequest,
  type LocalReviewDraftDiscardRequest,
  type ReviewReadRequest,
  type ReviewSaveRequest,
  type ReviewValidateRequest,
  type ReviewDecisionRequest,
  type ReviewSession,
  type ReviewSaveResult,
  type ReviewValidation,
  type ReviewDecisionResult,
  type FeatureConfigSnapshot,
  type FeatureConfigUpdateRequest,
  type WorkspaceDefaults,
  type ModelCatalogue,
  type RunListRequest,
  type RunListResult,
  type RunGetRequest,
  type RunDetailView,
  type RunSessionsListResult,
  type LivePreviewView,
  type RunArtifactsListRequest,
  type RunArtifactsListResult,
  type RunLogsListResult,
  type RunArtifactContentRequest,
  type RunTextContent,
  type RunLogContentRequest,
  type RewindPreviewRequest,
  type RewindPreviewView,
  type RewindExecuteRequest,
  type RebaseRequest,
  type RebaseResult,
  type RebasePreflightRequest,
  type RebasePreflightResult,
  type ReviewCommentsFetchRequest,
  type ReviewCommentsFetchResult,
  type ReviewCommentsStartRequest,
  type ReviewCommentsStartResult,
  type RefactorRequest,
  type RefactorResult,
  type RefactorPreflightRequest,
  type RefactorPreflightResult,
  type RecoverySnapshot,
  type RecoveryExecuteRequest,
  type RecoveryExecuteResult,
  type RecoveryLogReadRequest,
  type RecoveryLogReadResult,
  type BulkPreview,
  type CompletionPreflightRequest,
  type CompletionPreflightResult,
  type PublishDescriptionRequest,
  type PublishDescriptionResult,
  type RepositoryDiffRequest,
  type RepositoryDiffResult,
  type OpenExternalRequest,
  type RevealPathRequest,
  type UpdateInstallNowRequest,
  type UpdateState,
  type DiagnosticsSnapshot,
  ipcContracts,
} from '../shared/ipc';
import { isTrustedSender, type SenderLikeEvent, type TrustedSender } from './security';

export interface IpcServices {
  getConnectionStatus(): ConnectionState;
  retryConnection(): Promise<ConnectionState> | ConnectionState;
  restartConnection(): Promise<ConnectionState> | ConnectionState;
  getSettings(): Settings;
  updateSettings(patch: SettingsPatch): Settings;
  getTheme(): ThemeInfo;
  setTheme(preference: ThemePreference): ThemeInfo;
  getReadiness(): Promise<ReadinessSnapshot>;
  refreshReadiness(): Promise<ReadinessSnapshot>;
  pickWorkspaceDirectory(): Promise<PickedDirectory>;
  addWorkspaceRoot(path: string): Promise<ReadinessSnapshot>;
  removeWorkspaceRoot(path: string): Promise<ReadinessSnapshot>;
  reorderWorkspaceRoots(paths: string[]): Promise<ReadinessSnapshot>;
  initRepository(request: InitRepositoryRequest): Promise<ReadinessSnapshot>;
  listRepositories(): Promise<RepositoryState[]>;
  listFeatures(): Promise<FeatureSummaryView[]>;
  getFeature(featureId: string): Promise<FeatureSnapshot>;
  createFeature(input: CreateFeatureInput): Promise<CreateFeatureResult>;
  dispatchFeatureSetup(featureId: string): Promise<SetupDispatchResult>;
  dispatchFeatureAction(request: FeatureActionRequest): Promise<FeatureActionResult>;
  listSessions(): Promise<SessionSummary[]>;
  getSession(sessionId: string): Promise<SessionDetail>;
  getSessionTranscript(request: SessionTranscriptRequest): Promise<SessionTranscript>;
  openSessionOutput(
    request: SessionOutputOpenRequest,
    emit: (event: SessionOutputEvent) => void,
  ): string;
  cancelSessionOutput(subscriptionId: string): boolean;
  getCreationDefaults(): Promise<CreationDefaults>;
  pickCreationFiles(kind: CreationFileKind): Promise<PickedCreationFiles>;
  readClipboardImage(): Promise<PickedCreationFiles>;
  searchCreationFiles(request: CreationFileSearchRequest): Promise<CreationFileSearchResult>;
  cancelCreationFileSearch(requestId: string): Promise<boolean> | boolean;
  getAttention(): Promise<AttentionSnapshot>;
  answerPermission(request: PermissionDecisionRequest): Promise<AttentionActionResult>;
  answerQuestions(request: AskUserAnswerRequest): Promise<AttentionActionResult>;
  sendHelp(request: HelpAnswerRequest): Promise<AttentionActionResult>;
  saveGateDraft(request: GateDraftRequest): Promise<AttentionActionResult>;
  resolveGate(request: GateResolutionRequest): Promise<AttentionActionResult>;
  startChat(request: ChatStartRequest): Promise<ChatActionResult>;
  endChat(): Promise<ChatActionResult>;
  loadLocalReviewDraft(request: LocalReviewDraftLookupRequest): LocalReviewDraft | null;
  saveLocalReviewDraft(request: LocalReviewDraftSaveRequest): LocalReviewDraft;
  discardLocalReviewDraft(request: LocalReviewDraftDiscardRequest): boolean;
  readReview(request: ReviewReadRequest): Promise<ReviewSession>;
  openReview(request: ReviewReadRequest): Promise<ReviewSession>;
  saveReview(request: ReviewSaveRequest): Promise<ReviewSaveResult>;
  validateReview(request: ReviewValidateRequest): Promise<ReviewValidation>;
  decideReview(request: ReviewDecisionRequest): Promise<ReviewDecisionResult>;
  getFeatureConfig(featureId: string): Promise<FeatureConfigSnapshot>;
  updateFeatureConfig(request: FeatureConfigUpdateRequest): Promise<FeatureConfigSnapshot>;
  getWorkspaceDefaults(): Promise<WorkspaceDefaults>;
  updateWorkspaceDefaults(defaults: WorkspaceDefaults): Promise<WorkspaceDefaults>;
  getModelCatalogue(): Promise<ModelCatalogue>;
  listRuns(request: RunListRequest): Promise<RunListResult>;
  getRun(request: RunGetRequest): Promise<RunDetailView>;
  listRunSessions(request: RunGetRequest): Promise<RunSessionsListResult>;
  getLivePreview(featureId: string): Promise<LivePreviewView>;
  listRunArtifacts(request: RunArtifactsListRequest): Promise<RunArtifactsListResult>;
  listRunLogs(request: RunArtifactsListRequest): Promise<RunLogsListResult>;
  getRunArtifactContent(request: RunArtifactContentRequest): Promise<RunTextContent>;
  getRunLogContent(request: RunLogContentRequest): Promise<RunTextContent>;
  getRewindPreview(request: RewindPreviewRequest): Promise<RewindPreviewView>;
  executeRewind(request: RewindExecuteRequest): Promise<FeatureActionResult>;
  startRebase(request: RebaseRequest): Promise<RebaseResult>;
  preflightRebase(request: RebasePreflightRequest): Promise<RebasePreflightResult>;
  fetchReviewComments(request: ReviewCommentsFetchRequest): Promise<ReviewCommentsFetchResult>;
  startReviewComments(request: ReviewCommentsStartRequest): Promise<ReviewCommentsStartResult>;
  startRefactor(request: RefactorRequest): Promise<RefactorResult>;
  preflightRefactor(request: RefactorPreflightRequest): Promise<RefactorPreflightResult>;
  scanRecovery(): Promise<RecoverySnapshot>;
  executeRecovery(request: RecoveryExecuteRequest): Promise<RecoveryExecuteResult>;
  readRecoveryLog(request: RecoveryLogReadRequest): Promise<RecoveryLogReadResult>;
  bulkPreview(): Promise<BulkPreview>;
  preflightCompletion(request: CompletionPreflightRequest): Promise<CompletionPreflightResult>;
  getRepositoryDiff(request: RepositoryDiffRequest): Promise<RepositoryDiffResult>;
  generatePublishDescription(request: PublishDescriptionRequest): Promise<PublishDescriptionResult>;
  openExternal(request: OpenExternalRequest): Promise<{ ok: boolean }>;
  revealPath(request: RevealPathRequest): Promise<{ ok: boolean }>;
  getUpdates(): Promise<UpdateState> | UpdateState;
  checkForUpdates(): Promise<UpdateState>;
  installUpdateWhenIdle(): Promise<UpdateState>;
  installUpdateNow(request: UpdateInstallNowRequest): Promise<UpdateState>;
  restartToUpdate(): Promise<UpdateState> | UpdateState;
  getDiagnostics(): Promise<DiagnosticsSnapshot> | DiagnosticsSnapshot;
  revealDiagnostics(): Promise<{ ok: boolean }>;
  clearDiagnostics(): Promise<DiagnosticsSnapshot> | DiagnosticsSnapshot;
}

export interface IpcMainLike {
  handle(
    channel: string,
    listener: (event: SenderLikeEvent, ...args: unknown[]) => Promise<unknown>,
  ): void;
}

const UNTRUSTED: IpcEnvelope = {
  ok: false,
  error: {
    code: 'E_UNTRUSTED_SENDER',
    message: 'The request did not originate from the application window.',
  },
};

function makeHandler(
  channel: IpcChannel,
  trusted: TrustedSender,
  invoke: (event: SenderLikeEvent, ...args: never[]) => unknown,
): (event: SenderLikeEvent, ...args: unknown[]) => Promise<IpcEnvelope> {
  const contract = ipcContracts[channel];
  return async (event, ...args) => {
    if (!isTrustedSender(event, trusted)) {
      return UNTRUSTED;
    }
    try {
      assertWithinByteSize(JSON.stringify(args) ?? '');
      assertNoPrototypePollution(args);
      const parsedArgs = validateWithSchema(args, contract.request);
      const value = await (invoke as (e: SenderLikeEvent, ...a: unknown[]) => unknown)(
        event,
        ...parsedArgs,
      );
      return { ok: true, value: validateWithSchema(value, contract.response) };
    } catch (err) {
      return { ok: false, error: toSafeError(err, 'E_INTERNAL') };
    }
  };
}

export function registerIpcHandlers(
  ipcMain: IpcMainLike,
  trusted: TrustedSender,
  services: IpcServices,
): void {
  const bindings: Record<IpcChannel, (event: SenderLikeEvent, ...args: never[]) => unknown> = {
    [IPC_CHANNELS.connectionGetStatus]: () => services.getConnectionStatus(),
    [IPC_CHANNELS.connectionRetry]: () => services.retryConnection(),
    [IPC_CHANNELS.connectionRestart]: () => services.restartConnection(),
    [IPC_CHANNELS.settingsGet]: () => services.getSettings(),
    [IPC_CHANNELS.settingsUpdate]: (_event, patch: SettingsPatch) => services.updateSettings(patch),
    [IPC_CHANNELS.themeGet]: () => services.getTheme(),
    [IPC_CHANNELS.themeSet]: (_event, preference: ThemePreference) => services.setTheme(preference),
    [IPC_CHANNELS.readinessGet]: () => services.getReadiness(),
    [IPC_CHANNELS.readinessRefresh]: () => services.refreshReadiness(),
    [IPC_CHANNELS.workspacePickDirectory]: () => services.pickWorkspaceDirectory(),
    [IPC_CHANNELS.workspaceAddRoot]: (_event, path: string) => services.addWorkspaceRoot(path),
    [IPC_CHANNELS.workspaceRemoveRoot]: (_event, path: string) =>
      services.removeWorkspaceRoot(path),
    [IPC_CHANNELS.workspaceReorderRoots]: (_event, paths: string[]) =>
      services.reorderWorkspaceRoots(paths),
    [IPC_CHANNELS.workspaceInitRepository]: (_event, request: InitRepositoryRequest) =>
      services.initRepository(request),
    [IPC_CHANNELS.repositoriesList]: () => services.listRepositories(),
    [IPC_CHANNELS.featuresList]: () => services.listFeatures(),
    [IPC_CHANNELS.featuresGet]: (_event, featureId: string) => services.getFeature(featureId),
    [IPC_CHANNELS.featuresCreate]: (_event, input: CreateFeatureInput) =>
      services.createFeature(input),
    [IPC_CHANNELS.featuresSetup]: (_event, featureId: string) =>
      services.dispatchFeatureSetup(featureId),
    [IPC_CHANNELS.featuresDispatchAction]: (_event, request: FeatureActionRequest) =>
      services.dispatchFeatureAction(request),
    [IPC_CHANNELS.attentionGet]: () => services.getAttention(),
    [IPC_CHANNELS.attentionAnswerPermission]: (_event, request: PermissionDecisionRequest) =>
      services.answerPermission(request),
    [IPC_CHANNELS.attentionAnswerQuestions]: (_event, request: AskUserAnswerRequest) =>
      services.answerQuestions(request),
    [IPC_CHANNELS.attentionSendHelp]: (_event, request: HelpAnswerRequest) =>
      services.sendHelp(request),
    [IPC_CHANNELS.attentionSaveGateDraft]: (_event, request: GateDraftRequest) =>
      services.saveGateDraft(request),
    [IPC_CHANNELS.attentionResolveGate]: (_event, request: GateResolutionRequest) =>
      services.resolveGate(request),
    [IPC_CHANNELS.chatStart]: (_event, request: ChatStartRequest) => services.startChat(request),
    [IPC_CHANNELS.chatEnd]: () => services.endChat(),
    [IPC_CHANNELS.sessionsList]: () => services.listSessions(),
    [IPC_CHANNELS.sessionsGet]: (_event, sessionId: string) => services.getSession(sessionId),
    [IPC_CHANNELS.sessionsTranscript]: (_event, request: SessionTranscriptRequest) =>
      services.getSessionTranscript(request),
    [IPC_CHANNELS.sessionsOutputOpen]: (event, request: SessionOutputOpenRequest) => ({
      subscriptionId: services.openSessionOutput(request, (payload) => {
        const sender = event.sender as typeof event.sender & { isDestroyed?(): boolean };
        if (sender.isDestroyed?.() !== true) {
          sender.send?.(IPC_EVENTS.sessionOutput, payload);
        }
      }),
    }),
    [IPC_CHANNELS.sessionsOutputCancel]: (_event, request: { subscriptionId: string }) => ({
      cancelled: services.cancelSessionOutput(request.subscriptionId),
    }),
    [IPC_CHANNELS.creationDefaults]: () => services.getCreationDefaults(),
    [IPC_CHANNELS.creationPickFiles]: (_event, kind: CreationFileKind) =>
      services.pickCreationFiles(kind),
    [IPC_CHANNELS.clipboardReadImage]: () => services.readClipboardImage(),
    [IPC_CHANNELS.creationSearchFiles]: (_event, request: CreationFileSearchRequest) =>
      services.searchCreationFiles(request),
    [IPC_CHANNELS.creationCancelFileSearch]: (_event, requestId: string) =>
      Promise.resolve(services.cancelCreationFileSearch(requestId)).then((cancelled) => ({
        cancelled,
      })),
    [IPC_CHANNELS.reviewDraftsLoad]: (_event, request: LocalReviewDraftLookupRequest) =>
      services.loadLocalReviewDraft(request),
    [IPC_CHANNELS.reviewDraftsSave]: (_event, request: LocalReviewDraftSaveRequest) =>
      services.saveLocalReviewDraft(request),
    [IPC_CHANNELS.reviewDraftsDiscard]: (_event, request: LocalReviewDraftDiscardRequest) => ({
      discarded: services.discardLocalReviewDraft(request),
    }),
    [IPC_CHANNELS.reviewsRead]: (_event, request: ReviewReadRequest) =>
      services.readReview(request),
    [IPC_CHANNELS.reviewsOpen]: (_event, request: ReviewReadRequest) =>
      services.openReview(request),
    [IPC_CHANNELS.reviewsSave]: (_event, request: ReviewSaveRequest) =>
      services.saveReview(request),
    [IPC_CHANNELS.reviewsValidate]: (_event, request: ReviewValidateRequest) =>
      services.validateReview(request),
    [IPC_CHANNELS.reviewsDecide]: (_event, request: ReviewDecisionRequest) =>
      services.decideReview(request),
    [IPC_CHANNELS.configFeatureGet]: (_event, featureId: string) =>
      services.getFeatureConfig(featureId),
    [IPC_CHANNELS.configFeatureUpdate]: (_event, request: FeatureConfigUpdateRequest) =>
      services.updateFeatureConfig(request),
    [IPC_CHANNELS.configDefaultsGet]: () => services.getWorkspaceDefaults(),
    [IPC_CHANNELS.configDefaultsUpdate]: (_event, defaults: WorkspaceDefaults) =>
      services.updateWorkspaceDefaults(defaults),
    [IPC_CHANNELS.configModelCatalogue]: () => services.getModelCatalogue(),
    [IPC_CHANNELS.runsList]: (_event, request: RunListRequest) => services.listRuns(request),
    [IPC_CHANNELS.runsGet]: (_event, request: RunGetRequest) => services.getRun(request),
    [IPC_CHANNELS.runSessionsList]: (_event, request: RunGetRequest) =>
      services.listRunSessions(request),
    [IPC_CHANNELS.livePreviewGet]: (_event, featureId: string) =>
      services.getLivePreview(featureId),
    [IPC_CHANNELS.runArtifactsList]: (_event, request: RunArtifactsListRequest) =>
      services.listRunArtifacts(request),
    [IPC_CHANNELS.runLogsList]: (_event, request: RunArtifactsListRequest) =>
      services.listRunLogs(request),
    [IPC_CHANNELS.runArtifactContent]: (_event, request: RunArtifactContentRequest) =>
      services.getRunArtifactContent(request),
    [IPC_CHANNELS.runLogContent]: (_event, request: RunLogContentRequest) =>
      services.getRunLogContent(request),
    [IPC_CHANNELS.rewindPreview]: (_event, request: RewindPreviewRequest) =>
      services.getRewindPreview(request),
    [IPC_CHANNELS.rewindExecute]: (_event, request: RewindExecuteRequest) =>
      services.executeRewind(request),
    [IPC_CHANNELS.featuresRebase]: (_event, request: RebaseRequest) =>
      services.startRebase(request),
    [IPC_CHANNELS.featuresRebasePreflight]: (_event, request: RebasePreflightRequest) =>
      services.preflightRebase(request),
    [IPC_CHANNELS.featuresReviewCommentsFetch]: (_event, request: ReviewCommentsFetchRequest) =>
      services.fetchReviewComments(request),
    [IPC_CHANNELS.featuresReviewCommentsStart]: (_event, request: ReviewCommentsStartRequest) =>
      services.startReviewComments(request),
    [IPC_CHANNELS.featuresRefactor]: (_event, request: RefactorRequest) =>
      services.startRefactor(request),
    [IPC_CHANNELS.featuresRefactorPreflight]: (_event, request: RefactorPreflightRequest) =>
      services.preflightRefactor(request),
    [IPC_CHANNELS.recoveryScan]: () => services.scanRecovery(),
    [IPC_CHANNELS.recoveryExecute]: (_event, request: RecoveryExecuteRequest) =>
      services.executeRecovery(request),
    [IPC_CHANNELS.recoveryLogRead]: (_event, request: RecoveryLogReadRequest) =>
      services.readRecoveryLog(request),
    [IPC_CHANNELS.bulkPreview]: () => services.bulkPreview(),
    [IPC_CHANNELS.updatesGet]: () => services.getUpdates(),
    [IPC_CHANNELS.updatesCheck]: () => services.checkForUpdates(),
    [IPC_CHANNELS.updatesInstallWhenIdle]: () => services.installUpdateWhenIdle(),
    [IPC_CHANNELS.updatesInstallNow]: (_event, request: UpdateInstallNowRequest) =>
      services.installUpdateNow(request),
    [IPC_CHANNELS.updatesRestart]: () => services.restartToUpdate(),
    [IPC_CHANNELS.diagnosticsGet]: () => services.getDiagnostics(),
    [IPC_CHANNELS.diagnosticsReveal]: () => services.revealDiagnostics(),
    [IPC_CHANNELS.diagnosticsClear]: () => services.clearDiagnostics(),
    [IPC_CHANNELS.completionPreflight]: (_event, request: CompletionPreflightRequest) =>
      services.preflightCompletion(request),
    [IPC_CHANNELS.repositoryDiff]: (_event, request: RepositoryDiffRequest) =>
      services.getRepositoryDiff(request),
    [IPC_CHANNELS.publishDescription]: (_event, request: PublishDescriptionRequest) =>
      services.generatePublishDescription(request),
    [IPC_CHANNELS.openExternal]: (_event, request: OpenExternalRequest) =>
      services.openExternal(request),
    [IPC_CHANNELS.revealPath]: (_event, request: RevealPathRequest) => services.revealPath(request),
  };
  for (const channel of Object.values(IPC_CHANNELS)) {
    ipcMain.handle(channel, makeHandler(channel, trusted, bindings[channel]));
  }
}
