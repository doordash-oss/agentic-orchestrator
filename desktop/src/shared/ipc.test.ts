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
  CreationFileUploadResultSchema,
  FeatureSetupViewSchema,
  FeatureSummaryViewSchema,
  IPC_CHANNELS,
  IPC_EVENTS,
  InitRepositoryRequestSchema,
  IpcEnvelopeSchema,
  AbsolutePathSchema,
  OwnedErrorSchema,
  ReadinessSnapshotSchema,
  RelationshipChildViewSchema,
  RelationshipTransactionViewSchema,
  FeatureActionResultSchema,
  RepositoryDiffResultSchema,
  RecoveryItemViewSchema,
  SettingsPatchSchema,
  SettingsSchema,
  FeatureActionRequestSchema,
  AttentionItemSchema,
  UpdateStateSchema,
  actionableAttentionCount,
  type AttentionItem,
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
import * as ipcModule from './ipc';
import { assertNoPrototypePollution } from './sanitize';

const canonicalErrorFixture = {
  code: 'E_INTERNAL',
  class: 'blocking' as const,
  title: 'Request failed',
  summary: 'The connection attempt failed unexpectedly: boom.',
  remediation: { hint: 'Retry.' },
};

describe('module surface', () => {
  it('exports no safe-error schema: the canonical error is the one error shape', () => {
    // Matched structurally (not by name) so this file never spells the
    // deleted identifier the static check guards against.
    expect(Object.keys(ipcModule).some((key) => /safe.?error/i.test(key))).toBe(false);
  });
});

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

  it('accepts a run chat context reference and rejects undisciplined ones', () => {
    const request = {
      message: 'Explain this error',
      context: { scope: 'run', code: 'iteration_budget_exhausted', featureId: 'abcd1234' },
    };
    expect(ChatStartRequestSchema.parse(request)).toStrictEqual(request);
    // Unknown scopes, extra keys, and keys missing for or foreign to the
    // scope never reach the wire.
    for (const context of [
      { scope: 'session', code: 'x', featureId: 'abcd1234' },
      { scope: 'run', code: 'x', featureId: 'abcd1234', extra: 'x' },
      { scope: 'run', code: 'x', featureId: 'abcd1234', taskKey: 'setup-worktrees' },
      { scope: 'run', code: 'x', repository: 'main' },
      { scope: 'run', code: 'x' },
      { scope: 'setup', code: 'x', featureId: 'abcd1234' },
      { scope: 'recovery', code: 'x' },
    ]) {
      expect(
        ChatStartRequestSchema.safeParse({ message: 'Explain this error', context }).success,
        JSON.stringify(context),
      ).toBe(false);
    }
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

  it('carries the session-output error event as one canonical error', () => {
    const event = {
      subscriptionId: 'sub-1',
      type: 'error',
      sessionId: 'session-1',
      error: {
        code: 'E_SESSION_STREAM',
        class: 'blocking',
        title: 'The session stream failed',
        summary: 'The session output stream ended unexpectedly.',
      },
    };
    expect(SessionOutputEventSchema.parse(event)).toStrictEqual(event);
    expect(
      SessionOutputEventSchema.safeParse({
        ...event,
        error: { code: 'E_SESSION_STREAM', message: 'stream broke' },
      }).success,
    ).toBe(false);
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

  it('accepts terminal error states carrying a canonical error with folded diagnostics', () => {
    const state = {
      status: 'incompatible',
      stage: 'connect',
      detail: 'A running Agentico runtime is not compatible with this app.',
      ownership: 'external',
      error: {
        code: 'E_INCOMPATIBLE_SERVER',
        class: 'blocking',
        title: 'The server is not compatible with this app',
        summary: 'The server build is too old for this app.',
        remediation: { hint: 'Update the app and the runtime to matching releases.' },
        diagnostics: 'bundled agentico server\nlast redacted log line',
      },
    };
    expect(ConnectionStateSchema.parse(state)).toEqual(state);
  });

  it('rejects a terminal error carrying a message, a structured diagnostics object, or a state-level diagnostics sibling', () => {
    const base = {
      status: 'crashed',
      stage: 'connect',
      detail: 'The app-owned runtime stopped.',
      ownership: 'none',
      error: {
        code: 'E_SERVER_CRASHED',
        class: 'blocking',
        title: 'The app-managed runtime crashed',
        summary: 'The app-managed Agentico runtime exited with code 1.',
        diagnostics: 'bundled agentico server\nredacted line',
      },
    };
    expect(ConnectionStateSchema.safeParse(base).success).toBe(true);
    expect(
      ConnectionStateSchema.safeParse({
        ...base,
        error: { ...base.error, message: 'stopped' },
      }).success,
    ).toBe(false);
    expect(
      ConnectionStateSchema.safeParse({
        ...base,
        error: {
          ...base.error,
          diagnostics: { commandContext: 'bundled agentico server', logTail: ['redacted line'] },
        },
      }).success,
    ).toBe(false);
    // The pre-canonical state-level structured diagnostics sibling is gone.
    expect(
      ConnectionStateSchema.safeParse({
        ...base,
        diagnostics: { commandContext: 'bundled agentico server', logTail: ['redacted line'] },
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
        error: canonicalErrorFixture,
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
          error: canonicalErrorFixture,
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
            ? { error: canonicalErrorFixture }
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

  it('AppRouteEvent: draft, autoSubmit, and chatContext ride the ama target only', () => {
    const amaRoute = {
      target: 'ama',
      draft: 'Explain the "Run failed" error (run_failed) on add-login.',
      autoSubmit: true,
      chatContext: { scope: 'run', code: 'run_failed', featureId: 'abcd1234' },
    };
    expect(AppRouteEventSchema.parse(amaRoute)).toEqual(amaRoute);
    expect(AppRouteEventSchema.parse({ target: 'ama' })).toEqual({ target: 'ama' });
    // A non-ama route carrying any chat field — or an undisciplined
    // reference — fails closed.
    for (const bad of [
      { target: 'home', draft: 'hello' },
      { target: 'home', autoSubmit: true },
      { target: 'settings', chatContext: { scope: 'run', code: 'x', featureId: 'abcd1234' } },
      {
        target: 'ama',
        draft: 'hello',
        chatContext: { scope: 'run', code: 'x', featureId: 'abcd1234', taskKey: 't' },
      },
    ]) {
      expect(AppRouteEventSchema.safeParse(bad).success, JSON.stringify(bad)).toBe(false);
    }
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
  const issue = {
    code: 'unauthenticated',
    class: 'blocking',
    title: 'Unauthenticated',
    summary: 'A provider CLI is installed but its authentication flow has not been completed.',
    remediation: { hint: 'claude login' },
  };
  const snapshot = {
    ready: false,
    probedAt: '2026-07-14T10:00:00Z',
    providers: [
      {
        name: 'claude',
        installed: true,
        version: '2.1.0',
        ready: false,
        issue,
      },
    ],
    models: {
      available: false,
      issue: {
        code: 'models_unavailable',
        class: 'blocking',
        title: 'Models unavailable',
        summary: 'No usable provider exposes any model.',
      },
    },
    configuration: { valid: true },
    workspaceRoots: [{ path: '/w', valid: true }],
    repositories: [{ name: 'r', path: '/w/r', valid: true }],
    issues: [issue],
  };

  it('accepts a complete snapshot whose issues are canonical errors', () => {
    expect(ReadinessSnapshotSchema.parse(snapshot)).toEqual(snapshot);
  });

  it('rejects the pre-canonical message/remedy issue shape fail-closed', () => {
    expect(
      ReadinessSnapshotSchema.safeParse({
        ...snapshot,
        issues: [{ code: 'unauthenticated', message: 'not authenticated' }],
      }).success,
    ).toBe(false);
    expect(
      ReadinessSnapshotSchema.safeParse({
        ...snapshot,
        providers: [
          {
            name: 'claude',
            installed: true,
            version: '2.1.0',
            ready: false,
            issue: {
              code: 'unauthenticated',
              message: 'not authenticated',
              remedy: 'claude login',
            },
          },
        ],
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
  it('accepts ok and error envelopes, the error member being one canonical error', () => {
    expect(IpcEnvelopeSchema.safeParse({ ok: true, value: { any: 1 } }).success).toBe(true);
    expect(IpcEnvelopeSchema.safeParse({ ok: false, error: canonicalErrorFixture }).success).toBe(
      true,
    );
  });

  it('rejects the pre-canonical {code,message} error shape', () => {
    expect(
      IpcEnvelopeSchema.safeParse({ ok: false, error: { code: 'E_X', message: 'm' } }).success,
    ).toBe(false);
  });

  it('rejects malformed envelopes', () => {
    expect(IpcEnvelopeSchema.safeParse({ ok: false }).success).toBe(false);
    expect(IpcEnvelopeSchema.safeParse({ value: 1 }).success).toBe(false);
  });
});

describe('creation file upload results', () => {
  const failed = {
    ok: false as const,
    name: 'shot.png',
    error: {
      code: 'E_UPLOAD_TOO_LARGE',
      class: 'needs_action',
      title: 'The file is too large',
      summary: 'The file is larger than the 10 MiB upload limit.',
      remediation: {
        hint: 'Choose a smaller file: images are limited to 10 MiB and attachments to 25 MiB.',
      },
    },
  };

  it('accepts a failed-upload entry carrying one canonical error', () => {
    expect(CreationFileUploadResultSchema.safeParse(failed).success).toBe(true);
    expect(CreationFileUploadResultSchema.parse(failed)).toEqual(failed);
  });

  it('rejects the pre-canonical {code,message} failure entry', () => {
    expect(
      CreationFileUploadResultSchema.safeParse({
        ...failed,
        error: { code: 'E_UPLOAD_TOO_LARGE', message: 'too large' },
      }).success,
    ).toBe(false);
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

describe('owned errors on the feature summary and snapshot views', () => {
  const ownedRunError = {
    ref: { scope: 'run', code: 'iteration_budget_exhausted', featureId: 'abcd1234ef567890' },
    error: {
      code: 'iteration_budget_exhausted',
      class: 'blocking',
      title: 'Iteration budget exhausted',
      summary: 'The Implement phase exhausted its iteration budget.',
    },
  };
  const ownedRepoError = {
    ref: {
      scope: 'repository',
      code: 'publish_rebase_conflict',
      featureId: 'abcd1234ef567890',
      repository: 'repo-a',
    },
    error: {
      code: 'publish_rebase_conflict',
      class: 'needs_action',
      title: 'Pull-rebase conflict',
      summary: 'The pull rebase for repository "repo-a" conflicted with its target branch.',
    },
  };
  const summaryView = {
    id: 'abcd1234ef567890',
    name: 'Search revamp',
    status: 'Failed',
    currentPhase: 'Implement',
    repos: ['repo-a'],
    createdAt: '2026-07-14T10:00:00Z',
    activeRun: 1,
    runCount: 1,
    warnings: [],
    errors: [ownedRunError, ownedRepoError],
  };

  it('accepts a summary carrying two owned-error entries', () => {
    const parsed = FeatureSummaryViewSchema.parse(summaryView);
    expect(parsed.errors).toHaveLength(2);
    expect(parsed.errors?.[0]?.ref.scope).toBe('run');
    expect(parsed.errors?.[1]?.ref.repository).toBe('repo-a');
  });

  it('rejects an entry whose error carries the warning class', () => {
    const warningEntry = {
      ref: { scope: 'run', code: 'rewind_worktree_reset', featureId: 'abcd1234ef567890' },
      error: {
        code: 'rewind_worktree_reset',
        class: 'warning',
        title: 'Worktree reset to anchor',
        summary: 'The worktree was reset.',
      },
    };
    expect(OwnedErrorSchema.safeParse(warningEntry).success).toBe(false);
    expect(
      FeatureSummaryViewSchema.safeParse({ ...summaryView, errors: [warningEntry] }).success,
    ).toBe(false);
  });

  it('rejects an entry whose setup reference lacks the task key', () => {
    const undisciplined = {
      ...ownedRunError,
      ref: { scope: 'setup', code: 'worktree_setup_failed', featureId: 'abcd1234ef567890' },
    };
    expect(OwnedErrorSchema.safeParse(undisciplined).success).toBe(false);
    expect(
      FeatureSummaryViewSchema.safeParse({ ...summaryView, errors: [undisciplined] }).success,
    ).toBe(false);
  });

  it('rejects an entry carrying unknown keys', () => {
    expect(OwnedErrorSchema.safeParse({ ...ownedRunError, diagnostics: 'raw' }).success).toBe(
      false,
    );
    expect(OwnedErrorSchema.safeParse({ ...ownedRunError, owner: 'feature' }).success).toBe(false);
  });

  it('carries the same list on the feature snapshot view', () => {
    const { runCount: _summaryOnly, ...snapshotSummary } = summaryView;
    void _summaryOnly;
    const snapshotView = {
      ...snapshotSummary,
      slug: 'search-revamp',
      actions: [],
      reviewGate: {
        reviewingGate: false,
        reviewFixing: false,
        validatingPlan: false,
        validatorStatuses: {},
      },
      automaticReview: { mode: 'default', enabled: false, source: 'global' },
    };
    const parsed = FeatureSnapshotSchema.parse(snapshotView);
    expect(parsed.errors).toHaveLength(2);
    expect(parsed.errors?.[0]?.error.title).toBe('Iteration budget exhausted');
  });
});

describe('error attention items', () => {
  const errorItem: AttentionItem = {
    kind: 'error',
    id: 'error:feature-1:run::iteration_budget_exhausted',
    featureId: 'abcd1234ef567890',
    waitingSince: '2026-08-05T12:00:00Z',
    ref: { scope: 'run', code: 'iteration_budget_exhausted', featureId: 'abcd1234ef567890' },
    class: 'blocking',
    code: 'iteration_budget_exhausted',
    title: 'Iteration budget exhausted',
  };

  it('counts error items as actionable attention', () => {
    expect(actionableAttentionCount([errorItem])).toBe(1);
  });

  it('rejects an error item whose class is warning', () => {
    expect(AttentionItemSchema.safeParse({ ...errorItem, class: 'warning' }).success).toBe(false);
    expect(AttentionItemSchema.safeParse(errorItem).success).toBe(true);
  });
});

describe('UpdateStateSchema canonical error presence', () => {
  const base = {
    currentVersion: '0.1.0',
    packageFormat: 'macos',
    signatureStatus: 'unknown',
    message: 'Agentico is up to date.',
  } as const;
  const canonicalError = {
    code: 'E_UPDATE_CHECK_FAILED',
    class: 'blocking',
    title: 'Update check failed',
    summary: 'GitHub Releases returned HTTP 503.',
  } as const;

  it("rejects a 'failed' state that carries no canonical error", () => {
    expect(UpdateStateSchema.safeParse({ ...base, status: 'failed' }).success).toBe(false);
  });

  it('rejects a non-failed state that carries a canonical error', () => {
    for (const status of [
      'idle',
      'checking',
      'current',
      'available',
      'downloading',
      'ready',
      'scheduled',
      'installing',
    ] as const) {
      expect(
        UpdateStateSchema.safeParse({ ...base, status, error: canonicalError }).success,
        status,
      ).toBe(false);
    }
  });

  it("accepts a 'failed' state carrying a canonical E_ error", () => {
    expect(
      UpdateStateSchema.safeParse({ ...base, status: 'failed', error: canonicalError }).success,
    ).toBe(true);
  });
});
