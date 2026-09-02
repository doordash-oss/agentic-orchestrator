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

import { describe, expect, it } from 'vitest';
import {
  CompletionPreflightRepoSchema,
  ConnectionStateSchema,
  FeatureSetupViewSchema,
  IPC_CHANNELS,
  IPC_EVENTS,
  InitRepositoryRequestSchema,
  IpcEnvelopeSchema,
  AbsolutePathSchema,
  ReadinessSnapshotSchema,
  RelationshipChildViewSchema,
  RelationshipTransactionViewSchema,
  FeatureActionResultSchema,
  RepositoryDiffResultSchema,
  RecoveryItemViewSchema,
  SettingsPatchSchema,
  SettingsSchema,
  FeatureActionRequestSchema,
  FeatureSnapshotSchema,
  GateResumeRequestSchema,
  ChatStartRequestSchema,
  SessionIdSchema,
  SessionTranscriptRequestSchema,
  SessionOutputEventSchema,
  SetupTaskViewSchema,
  isActiveChatSession,
  isTerminalChatStatus,
  defaultAmaGeometry,
  defaultAmaPrefs,
  defaultServersPrefs,
  defaultSettings,
  defaultSettingsWindowPrefs,
  defaultShellPrefs,
  defaultWizardPrefs,
  ipcContracts,
  AppEventSchema,
  KnownServerSchema,
  MAX_KNOWN_SERVERS,
  ServersPatchSchema,
  SETTINGS_PANES,
  SettingsOpenRequestSchema,
  WINDOW_PURPOSE_ARGUMENT_PREFIX,
  windowPurposeFromArgv,
  LocalReviewDraftSaveRequestSchema,
  LocalReviewDraftStoreSchema,
  PublishDescriptionRequestSchema,
  RepoStatusViewSchema,
  AppRouteEventSchema,
  ServerRemoveRequestSchema,
  ServerTokenStatusRequestSchema,
  ServerTokenStatusResultSchema,
} from './ipc';
import { assertNoPrototypePollution } from './sanitize';

describe('FeatureSnapshot failure schema', () => {
  const failureSchema = FeatureSnapshotSchema.shape.failure;
  const canonicalFailure = {
    code: 'worktree_setup_failed',
    class: 'blocking' as const,
    title: 'Worktree setup failed',
    summary: 'Setting up the worktree for repository "repo-a" failed.',
    remediation: {
      hint: 'Resolve the reported problem in the repository, then retry setup.',
      actions: ['setup'],
    },
    context: { repositories: [{ name: 'repo-a', branch: 'feature/search-revamp' }] },
    diagnostics: 'git worktree add failed: no commits yet',
  };

  it('accepts the canonical failure crossing IPC', () => {
    const parsed = failureSchema?.safeParse(canonicalFailure);
    expect(parsed?.success).toBe(true);
  });

  it('rejects a failure missing any required canonical field', () => {
    for (const field of ['code', 'class', 'title', 'summary'] as const) {
      const incomplete = { ...canonicalFailure } as Record<string, unknown>;
      delete incomplete[field];
      expect(failureSchema?.safeParse(incomplete).success).toBe(false);
    }
  });
});

describe('Setup task and setup view schemas', () => {
  const canonicalError = {
    code: 'worktree_setup_failed',
    class: 'blocking' as const,
    title: 'Worktree setup failed',
    summary: 'Setting up the worktree for repository "repo-a" failed.',
    remediation: {
      hint: 'Resolve the reported problem in the repository or branch, then retry setup.',
      actions: ['setup'],
    },
    context: { repositories: [{ name: 'repo-a', branch: 'feature/search-revamp' }] },
    diagnostics: 'git worktree add failed: no commits yet',
  };
  const setupTask = {
    key: 'worktree:repo-a',
    kind: 'worktree',
    label: 'Worktree: repo-a',
    repo: 'repo-a',
    status: 'failed',
    branch: 'feature/search-revamp',
    attempt: 1,
    error: canonicalError,
  };

  it('accepts a setup task carrying its canonical error record', () => {
    expect(SetupTaskViewSchema.safeParse(setupTask).success).toBe(true);
  });

  it('rejects a task error missing any required canonical field', () => {
    for (const field of ['code', 'class', 'title', 'summary'] as const) {
      const incomplete = {
        ...setupTask,
        error: { ...canonicalError } as Record<string, unknown>,
      } as Record<string, unknown>;
      delete (incomplete.error as Record<string, unknown>)[field];
      expect(SetupTaskViewSchema.safeParse(incomplete).success).toBe(false);
    }
  });

  it('rejects the removed lastError keys on setup and relationship child views', () => {
    const setupView = {
      status: 'failed',
      attempt: 1,
      tasks: [setupTask],
    };
    expect(FeatureSetupViewSchema.safeParse(setupView).success).toBe(true);
    expect(FeatureSetupViewSchema.safeParse({ ...setupView, lastError: 'boom' }).success).toBe(
      false,
    );
    expect(SetupTaskViewSchema.safeParse({ ...setupTask, lastError: 'boom' }).success).toBe(false);

    const childView = {
      id: 'abcd1234ef567890',
      name: 'Refactor pass',
      kind: 'refactor',
      displayToken: 'R1',
      displayState: 'Completed',
      pipeline: 'medium',
      status: 'Done',
      startedAt: '2026-07-14T00:00:00Z',
      cost: { totalUsd: 0, byPhase: {} },
      integrationState: 'merged',
      warnings: [],
    };
    expect(RelationshipChildViewSchema.safeParse(childView).success).toBe(true);
    expect(RelationshipChildViewSchema.safeParse({ ...childView, lastError: 'boom' }).success).toBe(
      false,
    );
    expect(
      RelationshipChildViewSchema.safeParse({ ...childView, cleanupWarnings: [] }).success,
    ).toBe(false);
  });

  it('rejects removed warning shapes on renderer-facing result views', () => {
    const transactionEntry = {
      repo: 'repo-a',
      prepState: 'applied',
      pendingSync: true,
    };
    const transaction = RelationshipTransactionViewSchema.safeParse({
      phase: 'applied',
      entries: [transactionEntry],
    });
    expect(transaction.success).toBe(true);
    const staleTransaction = RelationshipTransactionViewSchema.safeParse({
      phase: 'applied',
      entries: [{ ...transactionEntry, cleanupWarning: 'stale string' }],
    });
    expect(staleTransaction.success).toBe(false);

    const actionResult = {
      featureId: 'abcd1234ef567890',
      action: 'rewind',
      result: 'rewound',
      sessionIds: [],
    };
    expect(FeatureActionResultSchema.safeParse(actionResult).success).toBe(true);
    const staleWarnings = FeatureActionResultSchema.safeParse({
      ...actionResult,
      warnings: ['stale string warning'],
    });
    expect(staleWarnings.success).toBe(false);

    const diffResult = {
      featureId: 'abcd1234ef567890',
      repo: 'repo-a',
      files: [],
    };
    expect(RepositoryDiffResultSchema.safeParse(diffResult).success).toBe(true);
    const staleDiff = RepositoryDiffResultSchema.safeParse({
      ...diffResult,
      partialFailure: 'stale string',
    });
    expect(staleDiff.success).toBe(false);
  });

  it('requires the canonical orphan error on every recovery item view', () => {
    const recoveryItem = {
      key: 'feature-alpha:repo-a',
      featureId: 'alpha1234ef567890',
      processAlive: true,
      allowedActions: ['resume', 'kill'],
      defaultAction: 'resume',
      error: {
        code: 'orphan_session_live',
        class: 'needs_action',
        title: 'Orphan session still running',
        summary: 'The session process is still alive after its run was interrupted.',
      },
    };
    expect(RecoveryItemViewSchema.safeParse(recoveryItem).success).toBe(true);
    const { error: _removed, ...withoutError } = recoveryItem;
    void _removed;
    expect(RecoveryItemViewSchema.safeParse(withoutError).success).toBe(false);
  });
});

describe('IPC channel registry', () => {
  it('defines a zod request/response contract for every invokable channel', () => {
    for (const channel of Object.values(IPC_CHANNELS)) {
      const contract = ipcContracts[channel];
      expect(contract, `missing contract for ${channel}`).toBeDefined();
      expect(contract.request).toBeDefined();
      expect(contract.response).toBeDefined();
    }
  });

  it('namespaces all channels to avoid collisions with generic channels', () => {
    for (const channel of [...Object.values(IPC_CHANNELS), ...Object.values(IPC_EVENTS)]) {
      expect(channel.startsWith('agentico:')).toBe(true);
    }
  });
});

describe('operational IPC schemas', () => {
  it('accepts only the gate target when resuming and rejects legacy decisions', () => {
    const target = {
      featureId: 'abcd1234',
      repoName: 'repo-a',
    };

    expect(GateResumeRequestSchema.parse(target)).toStrictEqual(target);
    for (const decision of ['resume', 'abort']) {
      expect(GateResumeRequestSchema.safeParse({ ...target, decision }).success).toBe(false);
    }
  });

  it('allows only audited feature action catalogue entries', () => {
    expect(
      FeatureActionRequestSchema.parse({ featureId: 'abcd1234', action: 'start' }),
    ).toStrictEqual({ featureId: 'abcd1234', action: 'start' });
    expect(
      FeatureActionRequestSchema.parse({ featureId: 'abcd1234', action: 'pause-stop' }),
    ).toStrictEqual({ featureId: 'abcd1234', action: 'pause-stop' });
    expect(
      FeatureActionRequestSchema.parse({ featureId: 'abcd1234', action: 'resume' }),
    ).toStrictEqual({ featureId: 'abcd1234', action: 'resume' });
    expect(
      FeatureActionRequestSchema.parse({ featureId: 'abcd1234', action: 'retry' }),
    ).toStrictEqual({ featureId: 'abcd1234', action: 'retry' });
    expect(
      FeatureActionRequestSchema.parse({ featureId: 'abcd1234', action: 'restart' }),
    ).toStrictEqual({ featureId: 'abcd1234', action: 'restart' });
    expect(
      FeatureActionRequestSchema.parse({
        featureId: 'abcd1234',
        action: 'restart',
        body: {
          max_iterations_delta: 10,
          max_plan_iterations_delta: 2,
        },
      }),
    ).toStrictEqual({
      featureId: 'abcd1234',
      action: 'restart',
      body: {
        max_iterations_delta: 10,
        max_plan_iterations_delta: 2,
      },
    });
    expect(
      FeatureActionRequestSchema.parse({
        featureId: 'abcd1234',
        action: 'publish',
        body: {
          source_revision: 'rev-1',
          repos: ['repo-a'],
          title: 'Ship reviewed changes',
        },
      }),
    ).toStrictEqual({
      featureId: 'abcd1234',
      action: 'publish',
      body: {
        source_revision: 'rev-1',
        repos: ['repo-a'],
        title: 'Ship reviewed changes',
      },
    });
    expect(
      FeatureActionRequestSchema.parse({
        featureId: 'abcd1234',
        action: 'publish',
        body: {
          source_revision: 'rev-1',
          repos: ['repo-a'],
        },
      }),
    ).toStrictEqual({
      featureId: 'abcd1234',
      action: 'publish',
      body: {
        source_revision: 'rev-1',
        repos: ['repo-a'],
      },
    });
    expect(
      FeatureActionRequestSchema.parse({
        featureId: 'abcd1234',
        action: 'delete',
        body: { source_revision: 'rev-1' },
      }),
    ).toStrictEqual({
      featureId: 'abcd1234',
      action: 'delete',
      body: { source_revision: 'rev-1' },
    });
    for (const action of ['../start', 'shell', 'publish/description']) {
      expect(FeatureActionRequestSchema.safeParse({ featureId: 'abcd1234', action }).success).toBe(
        false,
      );
    }
    expect(
      FeatureActionRequestSchema.safeParse({ featureId: 'abcd1234', action: 'publish' }).success,
    ).toBe(false);
    expect(
      PublishDescriptionRequestSchema.parse({ featureId: 'abcd1234', repos: ['repo-a'] }),
    ).toStrictEqual({ featureId: 'abcd1234', repos: ['repo-a'] });
  });

  it('bounds transcript windows and keeps row cursors distinct from global event cursors', () => {
    expect(
      SessionTranscriptRequestSchema.parse({ sessionId: 'session-1', offset: 4, limit: 100 }),
    ).toStrictEqual({ sessionId: 'session-1', offset: 4, limit: 100 });
    expect(
      SessionTranscriptRequestSchema.safeParse({
        sessionId: 'session-1',
        offset: 0,
        limit: 501,
      }).success,
    ).toBe(false);
    expect(
      SessionTranscriptRequestSchema.safeParse({
        sessionId: 'session-1',
        epoch: 'global-epoch',
        seq: 9,
      }).success,
    ).toBe(false);
  });

  it('accepts dotted session IDs as one safe path segment', () => {
    expect(SessionIdSchema.parse('session.a-1')).toBe('session.a-1');
    expect(SessionIdSchema.safeParse('session/a-1').success).toBe(false);
  });

  it('bounds singleton AMA chat start requests', () => {
    expect(ChatStartRequestSchema.parse({ message: 'What is running?' })).toStrictEqual({
      message: 'What is running?',
    });
    expect(ChatStartRequestSchema.safeParse({ message: '   ' }).success).toBe(false);
    expect(ChatStartRequestSchema.safeParse({ message: 'hello', sessionId: 'other' }).success).toBe(
      false,
    );
  });

  it('rejects foreign and prototype-polluting output records', () => {
    const valid = {
      subscriptionId: 'sub-1',
      type: 'record',
      sessionId: 'session-1',
      index: 2,
      message: { index: 2, role: 'assistant', type: 'text', text: 'hello' },
    };
    expect(SessionOutputEventSchema.parse(valid)).toStrictEqual(valid);
    expect(SessionOutputEventSchema.safeParse({ ...valid, token: 'secret' }).success).toBe(false);
    expect(() =>
      assertNoPrototypePollution(
        JSON.parse(
          '{"subscriptionId":"sub-1","type":"record","sessionId":"session-1","index":2,"message":{"index":2,"role":"assistant","type":"text","__proto__":{}}}',
        ),
      ),
    ).toThrow();
  });
});

describe('singleton AMA session helpers', () => {
  it('uses one terminal status vocabulary for active-chat decisions', () => {
    for (const status of [
      'complete',
      'completed',
      'done',
      'ended',
      'failed',
      'cancelled',
      'canceled',
      'stopped',
      'not_active',
    ]) {
      expect(isTerminalChatStatus(status), status).toBe(true);
      expect(isTerminalChatStatus(status.toLocaleUpperCase()), status).toBe(true);
      expect(
        isActiveChatSession({
          id: '__chat__',
          featureId: '__chat__',
          kind: 'chat',
          status,
        }),
        status,
      ).toBe(false);
    }

    expect(
      isActiveChatSession({
        id: '__chat__',
        featureId: '__chat__',
        kind: 'chat',
        status: 'running',
      }),
    ).toBe(true);
    expect(
      isActiveChatSession({
        id: 'feature-session',
        featureId: 'feature1',
        kind: 'agent',
        status: 'running',
      }),
    ).toBe(false);
  });
});

describe('ConnectionStateSchema', () => {
  it('accepts gateway lifecycle states with ownership and server build', () => {
    const state = {
      status: 'ready',
      stage: 'ready',
      detail: 'Connected to an externally managed Agentico runtime.',
      ownership: 'external',
      kind: 'local',
      serverBuild: { version: 'v1.2.3', revision: 'abc' },
    };
    expect(ConnectionStateSchema.parse(state)).toEqual(state);
  });

  it('requires locality on the ready state and bounds it to local|remote', () => {
    const base = {
      status: 'ready',
      stage: 'ready',
      detail: 'Connected.',
      ownership: 'external',
    };
    // Both kinds validate; the field is additive within the strict shape.
    expect(ConnectionStateSchema.safeParse({ ...base, kind: 'local' }).success).toBe(true);
    expect(ConnectionStateSchema.safeParse({ ...base, kind: 'remote' }).success).toBe(true);
    // A ready state without locality fails closed: consumers must never guess.
    expect(ConnectionStateSchema.safeParse(base).success).toBe(false);
    expect(ConnectionStateSchema.safeParse({ ...base, kind: null }).success).toBe(false);
    // Foreign kinds are not locality.
    expect(ConnectionStateSchema.safeParse({ ...base, kind: 'nearby' }).success).toBe(false);
    expect(ConnectionStateSchema.safeParse({ ...base, kind: 'LOCAL' }).success).toBe(false);
    // Transitional states carry no locality at all (strict shape rejects it).
    expect(
      ConnectionStateSchema.safeParse({
        status: 'connecting',
        stage: 'authenticate',
        detail: 'Authenticating.',
        ownership: 'external',
        kind: 'remote',
      }).success,
    ).toBe(false);
  });

  it('carries the optional server display name within its 64-char bound', () => {
    const base = {
      status: 'ready',
      stage: 'ready',
      detail: 'Connected to an externally managed Agentico runtime.',
      ownership: 'external',
      kind: 'local',
    };
    // Absent (older servers), null, at-bound, and in-bound names all pass.
    expect(ConnectionStateSchema.safeParse(base).success).toBe(true);
    expect(ConnectionStateSchema.safeParse({ ...base, serverName: null }).success).toBe(true);
    expect(ConnectionStateSchema.safeParse({ ...base, serverName: 'x'.repeat(64) }).success).toBe(
      true,
    );
    const named = { ...base, serverName: 'frothy-macchiato' };
    expect(ConnectionStateSchema.parse(named)).toEqual(named);
    // Oversized or wrong-typed names fail closed.
    expect(ConnectionStateSchema.safeParse({ ...base, serverName: 'x'.repeat(65) }).success).toBe(
      false,
    );
    expect(ConnectionStateSchema.safeParse({ ...base, serverName: 42 }).success).toBe(false);
  });

  it('accepts terminal error states with redacted diagnostics', () => {
    const state = {
      status: 'incompatible',
      stage: 'connect',
      detail: 'A running Agentico runtime is not compatible with this app.',
      ownership: 'external',
      error: { code: 'E_INCOMPATIBLE_SERVER', message: 'nope', remediation: 'update' },
    };
    expect(ConnectionStateSchema.parse(state)).toEqual(state);
  });

  it('bounds app-owned failure diagnostics at the IPC boundary', () => {
    const base = {
      status: 'crashed',
      stage: 'connect',
      detail: 'The app-owned runtime stopped.',
      ownership: 'none',
      error: { code: 'E_SERVER_CRASHED', message: 'stopped' },
      diagnostics: { commandContext: 'bundled agentico server', logTail: ['redacted line'] },
    };
    expect(ConnectionStateSchema.safeParse(base).success).toBe(true);
    expect(
      ConnectionStateSchema.safeParse({
        ...base,
        diagnostics: { ...base.diagnostics, logTail: Array.from({ length: 21 }, () => 'line') },
      }).success,
    ).toBe(false);
    expect(
      ConnectionStateSchema.safeParse({
        ...base,
        diagnostics: { ...base.diagnostics, logTail: ['x'.repeat(513)] },
      }).success,
    ).toBe(false);
  });

  it('rejects unknown statuses and extra fields fail-closed', () => {
    expect(
      ConnectionStateSchema.safeParse({
        status: 'nope',
        stage: 'discover',
        detail: '',
        ownership: 'none',
      }).success,
    ).toBe(false);
    expect(
      ConnectionStateSchema.safeParse({ status: 'idle', stage: 'discover', detail: '' }).success,
    ).toBe(false); // ownership is required
  });

  it('rejects a ready state that names no server ownership', () => {
    expect(
      ConnectionStateSchema.safeParse({
        status: 'ready',
        stage: 'ready',
        detail: 'Connected.',
        ownership: 'none',
        kind: 'local',
      }).success,
    ).toBe(false);
  });

  it('rejects a ready state carrying error detail', () => {
    expect(
      ConnectionStateSchema.safeParse({
        status: 'ready',
        stage: 'ready',
        detail: 'Connected.',
        ownership: 'app-owned',
        kind: 'local',
        error: { code: 'E_X', message: 'impossible' },
      }).success,
    ).toBe(false);
  });

  it('rejects every terminal failure status without error detail', () => {
    for (const status of [
      'incompatible',
      'resources-missing',
      'launch-failed',
      'crashed',
      'error',
    ]) {
      expect(
        ConnectionStateSchema.safeParse({
          status,
          stage: 'connect',
          detail: 'failed',
          ownership: 'none',
        }).success,
        `${status} must require error detail`,
      ).toBe(false);
    }
  });

  it('rejects in-flight startup states carrying an error', () => {
    for (const status of ['idle', 'resolving-runtime', 'discovering', 'attaching', 'launching']) {
      expect(
        ConnectionStateSchema.safeParse({
          status,
          stage: 'discover',
          detail: 'working',
          ownership: 'none',
          error: { code: 'E_X', message: 'impossible' },
        }).success,
        `${status} must not carry an error`,
      ).toBe(false);
    }
  });

  it('rejects supervision states with impossible ownership', () => {
    // waiting-health only exists while supervising the app-owned child.
    expect(
      ConnectionStateSchema.safeParse({
        status: 'waiting-health',
        stage: 'wait-health',
        detail: 'Waiting for the runtime to become healthy.',
        ownership: 'none',
      }).success,
    ).toBe(false);
    // connecting authenticates against a concrete server, never no server.
    expect(
      ConnectionStateSchema.safeParse({
        status: 'connecting',
        stage: 'authenticate',
        detail: 'Authenticating.',
        ownership: 'none',
      }).success,
    ).toBe(false);
  });

  it('rejects lifecycle statuses paired with another stage', () => {
    const validStageByStatus = {
      idle: { stage: 'resolve-runtime', ownership: 'none' },
      'resolving-runtime': { stage: 'resolve-runtime', ownership: 'none' },
      discovering: { stage: 'discover', ownership: 'none' },
      attaching: { stage: 'connect', ownership: 'none' },
      launching: { stage: 'connect', ownership: 'none' },
      'waiting-health': { stage: 'wait-health', ownership: 'app-owned' },
      connecting: { stage: 'authenticate', ownership: 'app-owned' },
      ready: { stage: 'ready', ownership: 'app-owned' },
      incompatible: { stage: 'connect', ownership: 'external' },
      'resources-missing': { stage: 'connect', ownership: 'none' },
      'launch-failed': { stage: 'connect', ownership: 'none' },
      crashed: { stage: 'connect', ownership: 'none' },
      error: { stage: 'resolve-runtime', ownership: 'none' },
    } as const;

    for (const [status, expected] of Object.entries(validStageByStatus)) {
      expect(
        ConnectionStateSchema.safeParse({
          status,
          stage: expected.stage === 'ready' ? 'discover' : 'ready',
          detail: 'invalid lifecycle pairing',
          ownership: expected.ownership,
          ...(status === 'ready' ? { kind: 'local' } : {}),
          ...(status === 'incompatible' ||
          status === 'resources-missing' ||
          status === 'launch-failed' ||
          status === 'crashed' ||
          status === 'error'
            ? { error: { code: 'E_X', message: 'failed' } }
            : {}),
        }).success,
        `${status} must only accept its lifecycle stage`,
      ).toBe(false);
    }
  });

  it('rejects token-shaped fields anywhere in the state', () => {
    const base = {
      status: 'ready',
      stage: 'ready',
      detail: '',
      ownership: 'app-owned',
      kind: 'local',
    };
    for (const extra of [
      { bearerToken: 'x' },
      { authToken: 'x' },
      { token: 'x' },
      { auth_token: 'x' },
      { baseUrl: 'http://127.0.0.1:1' },
      { serverBuild: { version: 'v1', token: 'x' } },
    ]) {
      expect(ConnectionStateSchema.safeParse({ ...base, ...extra }).success).toBe(false);
    }
  });
});

describe('SettingsSchema', () => {
  it('accepts the default settings', () => {
    expect(SettingsSchema.parse(defaultSettings())).toEqual(defaultSettings());
  });

  it('rejects credential- or server-domain-shaped fields fail-closed', () => {
    for (const extra of [
      { token: 'abc' },
      { serverUrl: 'http://x' },
      { features: [] },
      { runs: [] },
    ]) {
      const candidate = { ...defaultSettings(), ...extra };
      expect(SettingsSchema.safeParse(candidate).success).toBe(false);
    }
  });

  it('rejects unsupported schema versions', () => {
    expect(SettingsSchema.safeParse({ ...defaultSettings(), schemaVersion: 2 }).success).toBe(
      false,
    );
    expect(SettingsSchema.safeParse({ ...defaultSettings(), schemaVersion: 3 }).success).toBe(
      false,
    );
    expect(SettingsSchema.safeParse({ ...defaultSettings(), schemaVersion: 4 }).success).toBe(
      false,
    );
    expect(SettingsSchema.safeParse({ ...defaultSettings(), schemaVersion: 6 }).success).toBe(
      false,
    );
  });

  it('accepts a full settings document with window bounds and theme', () => {
    const doc = {
      schemaVersion: 5,
      runtime: { selection: 'claude' },
      window: { bounds: { x: 10, y: 20, width: 800, height: 600 } },
      theme: 'dark',
      wizard: { collapsedHelp: true },
      ama: { drawer: 'expanded', geometry: { right: 40, bottom: 60, width: 480, height: 620 } },
      notifications: { previewEnabled: true },
      shell: {
        featureByServer: { ['a'.repeat(64)]: 'abcd1234ef567890' },
        sidebarCollapsed: true,
      },
      settingsWindow: {
        bounds: { x: 40, y: 60, width: 900, height: 640 },
        pane: 'diagnostics',
      },
      servers: {
        known: [
          {
            serverKey: 'a'.repeat(64),
            kind: 'local',
            name: 'frothy-macchiato',
            baseUrl: 'http://127.0.0.1:9001',
            runtimeDir: '/home/user/.agentic-orchestrator',
            lastSeenAt: '2026-08-10T00:00:00.000Z',
          },
        ],
        lastUsed: 'a'.repeat(64),
      },
    };
    expect(SettingsSchema.parse(doc)).toEqual(doc);
  });

  it('fills wizard presentation prefs with defaults for pre-wizard documents', () => {
    const doc = {
      schemaVersion: 5,
      runtime: { selection: null },
      window: {},
      theme: 'system',
    };
    expect(SettingsSchema.parse(doc)).toEqual({
      ...doc,
      wizard: defaultWizardPrefs(),
      ama: defaultAmaPrefs(),
      notifications: { previewEnabled: false },
      shell: defaultShellPrefs(),
      settingsWindow: defaultSettingsWindowPrefs(),
      servers: defaultServersPrefs(),
    });
  });

  it('fills the Settings window prefs with defaults for pre-Settings-window documents', () => {
    const doc = {
      schemaVersion: 5,
      runtime: { selection: 'claude' },
      window: {},
      theme: 'dark',
      wizard: { collapsedHelp: true },
      ama: defaultAmaPrefs(),
      notifications: { previewEnabled: true },
      shell: defaultShellPrefs(),
    };
    expect(SettingsSchema.parse(doc)).toEqual({
      ...doc,
      settingsWindow: { pane: 'workspace-roots' },
      servers: defaultServersPrefs(),
    });
  });

  it('rejects a Settings window pane outside the pane catalogue', () => {
    for (const pane of ['nope', 'Updates', '']) {
      expect(
        SettingsSchema.safeParse({ ...defaultSettings(), settingsWindow: { pane } }).success,
        pane,
      ).toBe(false);
    }
    for (const pane of SETTINGS_PANES) {
      expect(
        SettingsSchema.safeParse({ ...defaultSettings(), settingsWindow: { pane } }).success,
        pane,
      ).toBe(true);
    }
  });
});

describe('SettingsPatchSchema', () => {
  it('accepts partial updates', () => {
    expect(SettingsPatchSchema.parse({ theme: 'light' })).toEqual({ theme: 'light' });
    expect(SettingsPatchSchema.parse({ runtime: { selection: null } })).toEqual({
      runtime: { selection: null },
    });
    expect(SettingsPatchSchema.parse({ ama: { drawer: 'expanded' } })).toEqual({
      ama: { drawer: 'expanded', geometry: defaultAmaGeometry() },
    });
    expect(SettingsPatchSchema.parse({ notifications: { previewEnabled: true } })).toEqual({
      notifications: { previewEnabled: true },
    });
  });

  it('accepts a Settings window patch and rejects an unknown pane', () => {
    expect(SettingsPatchSchema.parse({ settingsWindow: { pane: 'appearance' } })).toEqual({
      settingsWindow: { pane: 'appearance' },
    });
    expect(
      SettingsPatchSchema.parse({
        settingsWindow: { bounds: { x: 1, y: 2, width: 900, height: 640 }, pane: 'updates' },
      }),
    ).toEqual({
      settingsWindow: { bounds: { x: 1, y: 2, width: 900, height: 640 }, pane: 'updates' },
    });
    expect(SettingsPatchSchema.safeParse({ settingsWindow: { pane: 'nope' } }).success).toBe(false);
    expect(SettingsPatchSchema.safeParse({ settingsWindow: {} }).success).toBe(false);
  });

  it('rejects schemaVersion tampering and unknown keys', () => {
    expect(SettingsPatchSchema.safeParse({ schemaVersion: 9 }).success).toBe(false);
    expect(SettingsPatchSchema.safeParse({ apiToken: 'x' }).success).toBe(false);
  });

  it('accepts a servers patch that upserts an entry and/or sets last-used', () => {
    const entry = {
      serverKey: 'b'.repeat(64),
      kind: 'local',
      name: '',
      baseUrl: 'http://localhost:9001',
      runtimeDir: '/rt',
      lastSeenAt: '2026-08-10T00:00:00.000Z',
    };
    expect(SettingsPatchSchema.parse({ servers: { upsertKnown: entry } })).toEqual({
      servers: { upsertKnown: entry },
    });
    expect(SettingsPatchSchema.parse({ servers: { lastUsed: null } })).toEqual({
      servers: { lastUsed: null },
    });
    expect(SettingsPatchSchema.parse({ servers: { upsertKnown: entry, lastUsed: 'x' } })).toEqual({
      servers: { upsertKnown: entry, lastUsed: 'x' },
    });
  });

  it('rejects an empty servers patch section and wholesale known-list patching', () => {
    expect(SettingsPatchSchema.safeParse({ servers: {} }).success).toBe(false);
    expect(SettingsPatchSchema.safeParse({ servers: { known: [] } }).success).toBe(false);
    expect(SettingsPatchSchema.safeParse({ servers: { lastUsed: 'x', known: [] } }).success).toBe(
      false,
    );
  });
});

describe('Servers pane IPC contracts', () => {
  it('AppRouteEvent: settings focus rides the settings section, stays optional', () => {
    expect(
      AppRouteEventSchema.parse({
        target: 'settings',
        settingsSection: 'servers',
        settingsFocus: 'add-server',
      }),
    ).toEqual({
      target: 'settings',
      settingsSection: 'servers',
      settingsFocus: 'add-server',
    });
    expect(AppRouteEventSchema.parse({ target: 'settings' })).toEqual({ target: 'settings' });
    // Unknown focus intents and smuggled fields are rejected.
    expect(
      AppRouteEventSchema.safeParse({
        target: 'settings',
        settingsSection: 'servers',
        settingsFocus: 'nuke',
      }).success,
    ).toBe(false);
    expect(
      AppRouteEventSchema.safeParse({
        target: 'settings',
        settingsSection: 'servers',
        token: 'x',
      }).success,
    ).toBe(false);
  });

  it('ServerRemoveRequest: strict shape, non-empty key', () => {
    expect(ServerRemoveRequestSchema.parse({ serverKey: 'a'.repeat(32) })).toEqual({
      serverKey: 'a'.repeat(32),
    });
    for (const bad of [
      { serverKey: '' },
      { serverKey: 'a'.repeat(65) },
      { serverKey: 'a'.repeat(32), token: 'smuggled' },
      {},
    ]) {
      expect(ServerRemoveRequestSchema.safeParse(bad).success).toBe(false);
    }
  });

  it('ServerTokenStatus: request is strict; the result is one of four statuses, nothing else', () => {
    expect(ServerTokenStatusRequestSchema.safeParse({ serverKey: 'x' }).success).toBe(true);
    expect(ServerTokenStatusRequestSchema.safeParse({ serverKey: 'x', extra: 1 }).success).toBe(
      false,
    );
    for (const status of ['local', 'saved', 'session-only', 're-paste-required']) {
      expect(ServerTokenStatusResultSchema.parse({ status })).toEqual({ status });
    }
    expect(ServerTokenStatusResultSchema.safeParse({ status: 'ok' }).success).toBe(false);
    expect(
      ServerTokenStatusResultSchema.safeParse({ status: 'saved', token: 'leak' }).success,
    ).toBe(false);
  });
});

describe('KnownServerSchema', () => {
  const entry = {
    serverKey: 'c'.repeat(64),
    kind: 'local' as const,
    name: 'frothy-macchiato',
    baseUrl: 'http://127.0.0.1:9001',
    runtimeDir: '/home/user/.agentic-orchestrator',
    lastSeenAt: '2026-08-10T00:00:00.000Z',
  };

  it('accepts loopback and network plain-http base URLs', () => {
    for (const baseUrl of [
      'http://127.0.0.1:9001',
      'http://127.42.0.9',
      'http://localhost:9001',
      'http://[::1]:9001/ui',
      'http://10.1.2.3:8080',
      'http://example.com:9001',
    ]) {
      expect(KnownServerSchema.safeParse({ ...entry, baseUrl }).success, baseUrl).toBe(true);
    }
  });

  it('rejects non-http and unparseable base URLs', () => {
    for (const baseUrl of ['https://127.0.0.1:9001', 'ftp://localhost:9001', 'not a url', '']) {
      expect(KnownServerSchema.safeParse({ ...entry, baseUrl }).success, baseUrl).toBe(false);
    }
  });

  it('rejects token-shaped and other unknown fields, fail closed', () => {
    for (const rogue of [{ token: 'x' }, { authToken: 'x' }, { apiToken: 'x' }, { pid: 1234 }]) {
      expect(KnownServerSchema.safeParse({ ...entry, ...rogue }).success).toBe(false);
    }
  });

  it('rejects an empty serverKey, an unparseable lastSeenAt, and oversized fields', () => {
    expect(KnownServerSchema.safeParse({ ...entry, serverKey: '' }).success).toBe(false);
    expect(KnownServerSchema.safeParse({ ...entry, serverKey: 'd'.repeat(65) }).success).toBe(
      false,
    );
    expect(KnownServerSchema.safeParse({ ...entry, name: 'n'.repeat(65) }).success).toBe(false);
    expect(KnownServerSchema.safeParse({ ...entry, lastSeenAt: 'next tuesday-ish' }).success).toBe(
      false,
    );
  });

  it('splits local and remote entries: runtimeDir required locally, forbidden remotely', () => {
    // A local entry without runtimeDir is rejected.
    const localNoDir: Record<string, unknown> = { ...entry };
    delete localNoDir.runtimeDir;
    expect(KnownServerSchema.safeParse(localNoDir).success).toBe(false);
    // A remote entry must not carry runtimeDir.
    expect(
      KnownServerSchema.safeParse({ ...entry, kind: 'remote', runtimeDir: '/rt/no' }).success,
    ).toBe(false);
    // A remote entry without runtimeDir (with or without a nickname) is valid.
    const remote: Record<string, unknown> = { ...entry, kind: 'remote' };
    delete remote.runtimeDir;
    expect(KnownServerSchema.safeParse(remote).success).toBe(true);
    expect(KnownServerSchema.safeParse({ ...remote, nickname: 'the far box' }).success).toBe(true);
    // kind itself is constrained and required.
    expect(KnownServerSchema.safeParse({ ...entry, kind: 'tunnel' }).success).toBe(false);
    const noKind: Record<string, unknown> = { ...entry };
    delete noKind.kind;
    expect(KnownServerSchema.safeParse(noKind).success).toBe(false);
  });

  it('refuses a settings document whose known list exceeds the bound', () => {
    const doc = defaultSettings();
    for (let index = 0; index <= MAX_KNOWN_SERVERS; index += 1) {
      doc.servers.known.push({ ...entry, serverKey: index.toString(16).padStart(64, '0') });
    }
    expect(SettingsSchema.safeParse(doc).success).toBe(false);
  });

  it('accepts a servers patch with a last-used pointer only', () => {
    expect(ServersPatchSchema.safeParse({ lastUsed: 'e'.repeat(64) }).success).toBe(true);
    expect(ServersPatchSchema.safeParse({ lastUsed: 'f'.repeat(65) }).success).toBe(false);
  });
});

describe('window purposes', () => {
  it('reads the purpose out of a renderer argv, defaulting to the main window', () => {
    expect(windowPurposeFromArgv([`${WINDOW_PURPOSE_ARGUMENT_PREFIX}settings`])).toBe('settings');
    expect(windowPurposeFromArgv([`${WINDOW_PURPOSE_ARGUMENT_PREFIX}main`])).toBe('main');
    expect(windowPurposeFromArgv([])).toBe('main');
    expect(windowPurposeFromArgv(['--other-flag=settings'])).toBe('main');
    // An unrecognized purpose never escalates past the main window.
    expect(windowPurposeFromArgv([`${WINDOW_PURPOSE_ARGUMENT_PREFIX}devtools`])).toBe('main');
    expect(windowPurposeFromArgv([`${WINDOW_PURPOSE_ARGUMENT_PREFIX}`])).toBe('main');
  });

  it('bounds the renderer-origin Settings open request to deep-linkable sections', () => {
    expect(SettingsOpenRequestSchema.parse({})).toEqual({});
    expect(SettingsOpenRequestSchema.parse({ section: 'updates' })).toEqual({ section: 'updates' });
    expect(SettingsOpenRequestSchema.parse({ section: 'diagnostics' })).toEqual({
      section: 'diagnostics',
    });
    // Not every pane is deep-linkable, and nothing else rides along.
    expect(SettingsOpenRequestSchema.safeParse({ section: 'advanced' }).success).toBe(false);
    expect(SettingsOpenRequestSchema.safeParse({ section: 'updates', pane: 'x' }).success).toBe(
      false,
    );
    // A within-pane focus intent matches the deep-link vocabulary exactly.
    expect(SettingsOpenRequestSchema.parse({ section: 'servers', focus: 'add-server' })).toEqual({
      section: 'servers',
      focus: 'add-server',
    });
    expect(SettingsOpenRequestSchema.safeParse({ section: 'servers', focus: 'nuke' }).success).toBe(
      false,
    );
  });

  it('carries the cross-window theme fan-out as a strict push event', () => {
    expect(AppEventSchema.parse({ type: 'theme', preference: 'system', resolved: 'dark' })).toEqual(
      {
        type: 'theme',
        preference: 'system',
        resolved: 'dark',
      },
    );
    expect(
      AppEventSchema.safeParse({ type: 'theme', preference: 'dark', resolved: 'system' }).success,
    ).toBe(false);
    expect(AppEventSchema.safeParse({ type: 'theme', preference: 'dark' }).success).toBe(false);
  });
});

describe('ReadinessSnapshotSchema', () => {
  const snapshot = {
    ready: false,
    probedAt: '2026-07-14T10:00:00Z',
    providers: [
      {
        name: 'claude',
        installed: true,
        version: '2.1.0',
        ready: false,
        issue: { code: 'unauthenticated', message: 'not authenticated', remedy: 'claude login' },
      },
    ],
    models: { available: false, issue: { code: 'models_unavailable', message: 'no models' } },
    configuration: { valid: true },
    workspaceRoots: [{ path: '/w', valid: true }],
    repositories: [{ name: 'r', path: '/w/r', valid: true }],
    issues: [{ code: 'unauthenticated', message: 'not authenticated', remedy: 'claude login' }],
  };

  it('accepts a complete snapshot', () => {
    expect(ReadinessSnapshotSchema.parse(snapshot)).toEqual(snapshot);
  });

  it('rejects unknown issue codes and token-shaped extras fail-closed', () => {
    expect(
      ReadinessSnapshotSchema.safeParse({
        ...snapshot,
        issues: [{ code: 'mystery', message: 'x' }],
      }).success,
    ).toBe(false);
    for (const extra of [{ authToken: 'x' }, { token: 'x' }, { baseUrl: 'http://127.0.0.1:1' }]) {
      expect(ReadinessSnapshotSchema.safeParse({ ...snapshot, ...extra }).success).toBe(false);
    }
  });
});

describe('AbsolutePathSchema', () => {
  it('accepts absolute POSIX paths', () => {
    expect(AbsolutePathSchema.parse('/work/space')).toBe('/work/space');
  });

  it('rejects relative paths, NUL bytes, and empty strings', () => {
    for (const bad of ['relative/path', '', '/bad\0path', '/bad\npath', 'C:\\windows']) {
      expect(AbsolutePathSchema.safeParse(bad).success).toBe(false);
    }
  });
});

describe('InitRepositoryRequestSchema', () => {
  it('requires consent to be literally true at the schema layer', () => {
    expect(InitRepositoryRequestSchema.safeParse({ path: '/w/repo', consent: true }).success).toBe(
      true,
    );
    for (const consent of [false, 'true', 1, undefined]) {
      expect(InitRepositoryRequestSchema.safeParse({ path: '/w/repo', consent }).success).toBe(
        false,
      );
    }
  });
});

describe('IpcEnvelopeSchema', () => {
  it('accepts ok and error envelopes', () => {
    expect(IpcEnvelopeSchema.safeParse({ ok: true, value: { any: 1 } }).success).toBe(true);
    expect(
      IpcEnvelopeSchema.safeParse({ ok: false, error: { code: 'E_X', message: 'm' } }).success,
    ).toBe(true);
  });

  it('rejects malformed envelopes', () => {
    expect(IpcEnvelopeSchema.safeParse({ ok: false }).success).toBe(false);
    expect(IpcEnvelopeSchema.safeParse({ value: 1 }).success).toBe(false);
  });
});

describe('recoverable local review draft schemas', () => {
  const key = {
    runtimeId: 'runtime-a',
    featureId: 'feature-a',
    reviewId: 'review-a',
    baseDraftRevision: 'revision-a',
  };

  it('accepts only bounded, strictly-keyed local editor text', () => {
    expect(LocalReviewDraftSaveRequestSchema.safeParse({ ...key, text: '# Draft' }).success).toBe(
      true,
    );
    expect(
      LocalReviewDraftSaveRequestSchema.safeParse({ ...key, text: 'draft', snapshot: {} }).success,
    ).toBe(false);
  });

  it('requires the exact versioned store envelope', () => {
    expect(
      LocalReviewDraftStoreSchema.safeParse({
        schemaVersion: 1,
        drafts: [{ ...key, text: 'draft', savedAt: '2026-07-16T00:00:00.000Z' }],
      }).success,
    ).toBe(true);
    expect(LocalReviewDraftStoreSchema.safeParse({ schemaVersion: 2, drafts: [] }).success).toBe(
      false,
    );
  });
});

describe('repository publish-failure error views', () => {
  const repoError = {
    code: 'publish_pull_request_failed',
    class: 'needs_action',
    title: 'Pull-request creation failed',
    summary: 'Creating the pull request for repository "repo-a" failed.',
    remediation: { hint: 'Check GitHub access, then retry.', actions: ['publish'] },
    context: { repositories: [{ name: 'repo-a', branch: 'feature/f', remote_only_commits: 3 }] },
    diagnostics: 'POST /repos/org/repo-a/pulls: 502 Bad Gateway',
  };
  const repoStatusView = {
    name: 'repo-a',
    publishable: true,
    touched: true,
    error: repoError,
  };
  const preflightRepoView = {
    repo: 'repo-a',
    publishable: true,
    touched: true,
    status: 'unpublished_changes',
    error: repoError,
  };

  it('accepts both views carrying the canonical error', () => {
    expect(RepoStatusViewSchema.safeParse(repoStatusView).success).toBe(true);
    expect(CompletionPreflightRepoSchema.safeParse(preflightRepoView).success).toBe(true);
  });

  it('rejects an error lacking code, class, title, or summary on both views', () => {
    for (const dropped of ['code', 'class', 'title', 'summary'] as const) {
      const degraded = { ...repoError };
      delete degraded[dropped];
      expect(RepoStatusViewSchema.safeParse({ ...repoStatusView, error: degraded }).success).toBe(
        false,
      );
      expect(
        CompletionPreflightRepoSchema.safeParse({ ...preflightRepoView, error: degraded }).success,
      ).toBe(false);
    }
  });

  it('rejects stale lastError keys on both views', () => {
    expect(RepoStatusViewSchema.safeParse({ ...repoStatusView, lastError: 'boom' }).success).toBe(
      false,
    );
    expect(
      CompletionPreflightRepoSchema.safeParse({ ...preflightRepoView, lastError: 'boom' }).success,
    ).toBe(false);
  });
});
