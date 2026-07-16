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
  ipcContracts,
} from '../shared/ipc';
import { isTrustedSender, type SenderLikeEvent, type TrustedSender } from './security';

export interface IpcServices {
  getConnectionStatus(): ConnectionState;
  retryConnection(): Promise<ConnectionState> | ConnectionState;
  getSettings(): Settings;
  updateSettings(patch: SettingsPatch): Settings;
  getTheme(): ThemeInfo;
  setTheme(preference: ThemePreference): ThemeInfo;
  getReadiness(): Promise<ReadinessSnapshot>;
  refreshReadiness(): Promise<ReadinessSnapshot>;
  pickWorkspaceDirectory(): Promise<PickedDirectory>;
  addWorkspaceRoot(path: string): Promise<ReadinessSnapshot>;
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
  getAttention(): Promise<AttentionSnapshot>;
  answerPermission(request: PermissionDecisionRequest): Promise<AttentionActionResult>;
  answerQuestions(request: AskUserAnswerRequest): Promise<AttentionActionResult>;
  sendHelp(request: HelpAnswerRequest): Promise<AttentionActionResult>;
  saveGateDraft(request: GateDraftRequest): Promise<AttentionActionResult>;
  resolveGate(request: GateResolutionRequest): Promise<AttentionActionResult>;
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
    [IPC_CHANNELS.settingsGet]: () => services.getSettings(),
    [IPC_CHANNELS.settingsUpdate]: (_event, patch: SettingsPatch) => services.updateSettings(patch),
    [IPC_CHANNELS.themeGet]: () => services.getTheme(),
    [IPC_CHANNELS.themeSet]: (_event, preference: ThemePreference) => services.setTheme(preference),
    [IPC_CHANNELS.readinessGet]: () => services.getReadiness(),
    [IPC_CHANNELS.readinessRefresh]: () => services.refreshReadiness(),
    [IPC_CHANNELS.workspacePickDirectory]: () => services.pickWorkspaceDirectory(),
    [IPC_CHANNELS.workspaceAddRoot]: (_event, path: string) => services.addWorkspaceRoot(path),
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
  };
  for (const channel of Object.values(IPC_CHANNELS)) {
    ipcMain.handle(channel, makeHandler(channel, trusted, bindings[channel]));
  }
}
