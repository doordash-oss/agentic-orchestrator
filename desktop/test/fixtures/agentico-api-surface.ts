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

/**
 * Single source of truth for the exact `window.agentico` preload surface.
 *
 * Asserted byte-for-byte by both the fast security unit test
 * (test/security/preload.test.ts) and the packaged E2E security journey
 * (test/e2e/journeys/security.spec.ts). When you add or remove a preload
 * method, update this list once — both gates consume it, so the fast tier
 * fails immediately instead of the drift surfacing only in packaged CI.
 */
export const EXPECTED_API_SURFACE: readonly string[] = [
  'addRemoteServer',
  'addWorkspaceRoot',
  'answerPermission',
  'answerQuestions',
  'bulkPreview',
  'cancelCreationFileSearch',
  'cancelSessionOutput',
  'checkForUpdates',
  'chooseConnectionServer',
  'clearDiagnostics',
  'createFeature',
  'decideReview',
  'deleteFeatureCascade',
  'discardLocalReviewDraft',
  'discardRefactorChild',
  'dispatchFeatureAction',
  'dispatchFeatureSetup',
  'endChat',
  'executeRecovery',
  'executeRewind',
  'fetchReviewFeedback',
  'generatePublishDescription',
  'getAttention',
  'getConnectionStatus',
  'getCreationDefaults',
  'getDiagnostics',
  'getFeature',
  'getFeatureConfig',
  'getLivePreview',
  'getModelCatalogue',
  'getReadiness',
  'getRepositoryDiff',
  'getRewindPreview',
  'getRun',
  'getRunArtifactContent',
  'getRunLogContent',
  'getServerTokenStatus',
  'getSession',
  'getSessionTranscript',
  'getSettings',
  'getThemePreference',
  'getUpdates',
  'getWorkspaceDefaults',
  'importDroppedCreationFiles',
  'initRepository',
  'installUpdateNow',
  'installUpdateWhenIdle',
  'launchRebaseChild',
  'launchRefactorChild',
  'launchReviewFeedbackChild',
  'listFeatures',
  'listRepositories',
  'listRunArtifacts',
  'listRunLogs',
  'listRunSessions',
  'listRuns',
  'listServers',
  'listSessions',
  'loadLocalReviewDraft',
  'onAppEvent',
  'onConnectionChanged',
  'onRouteRequest',
  'onServersChanged',
  'onSessionOutput',
  'openExternal',
  'openReview',
  'openSessionOutput',
  'openSettingsWindow',
  'pickCreationFiles',
  'pickWorkspaceDirectory',
  'platform',
  'preflightCompletion',
  'probeServers',
  'publishUiState',
  'readClipboardImage',
  'readRecoveryLog',
  'readReview',
  'refreshProviderModels',
  'refreshReadiness',
  'removeServer',
  'removeWorkspaceRoot',
  'reorderWorkspaceRoots',
  'resolveGate',
  'restartConnection',
  'restartToUpdate',
  'retryConnection',
  'revealDiagnostics',
  'revealPath',
  'saveGateDraft',
  'saveLocalReviewDraft',
  'saveReview',
  'scanRecovery',
  'searchCreationFiles',
  'sendHelp',
  'setThemePreference',
  'startChat',
  'startLocalRuntime',
  'switchConnectionServer',
  'updateFeatureConfig',
  'updateReviewFeedbackSelection',
  'updateSettings',
  'updateWorkspaceDefaults',
  'uploadCreationFiles',
  'validateReview',
  'windowPurpose',
  'writeClipboardText',
];
