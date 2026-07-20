import { describe, expect, it } from 'vitest';
import {
  ConnectionStateSchema,
  IPC_CHANNELS,
  IPC_EVENTS,
  InitRepositoryRequestSchema,
  IpcEnvelopeSchema,
  AbsolutePathSchema,
  ReadinessSnapshotSchema,
  SettingsPatchSchema,
  SettingsSchema,
  FeatureActionRequestSchema,
  ChatStartRequestSchema,
  SessionIdSchema,
  SessionTranscriptRequestSchema,
  SessionOutputEventSchema,
  isActiveChatSession,
  isTerminalChatStatus,
  defaultSettings,
  defaultTabsPrefs,
  defaultWizardPrefs,
  ipcContracts,
  LocalReviewDraftSaveRequestSchema,
  LocalReviewDraftStoreSchema,
  PublishDescriptionRequestSchema,
} from './ipc';
import { assertNoPrototypePollution } from './sanitize';

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
      serverBuild: { version: 'v1.2.3', revision: 'abc' },
    };
    expect(ConnectionStateSchema.parse(state)).toEqual(state);
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
    const base = { status: 'ready', stage: 'ready', detail: '', ownership: 'app-owned' };
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
  });

  it('accepts a full settings document with window bounds and theme', () => {
    const doc = {
      schemaVersion: 1,
      runtime: { selection: 'claude' },
      window: { bounds: { x: 10, y: 20, width: 800, height: 600 } },
      theme: 'dark',
      wizard: { collapsedHelp: true, lastRepositoryPathHint: '/work/repo' },
      ama: { drawer: 'expanded' },
      notifications: { previewEnabled: true },
      tabs: { open: [{ featureId: 'abcd1234', titleHint: 'Search' }], activeFeatureId: null },
    };
    expect(SettingsSchema.parse(doc)).toEqual(doc);
  });

  it('fills wizard presentation prefs with defaults for pre-wizard documents', () => {
    const doc = {
      schemaVersion: 1,
      runtime: { selection: null },
      window: {},
      theme: 'system',
    };
    expect(SettingsSchema.parse(doc)).toEqual({
      ...doc,
      wizard: defaultWizardPrefs(),
      ama: { drawer: 'compact' },
      notifications: { previewEnabled: false },
      tabs: defaultTabsPrefs(),
    });
  });
});

describe('SettingsPatchSchema', () => {
  it('accepts partial updates', () => {
    expect(SettingsPatchSchema.parse({ theme: 'light' })).toEqual({ theme: 'light' });
    expect(SettingsPatchSchema.parse({ runtime: { selection: null } })).toEqual({
      runtime: { selection: null },
    });
    expect(SettingsPatchSchema.parse({ ama: { drawer: 'expanded' } })).toEqual({
      ama: { drawer: 'expanded' },
    });
    expect(SettingsPatchSchema.parse({ notifications: { previewEnabled: true } })).toEqual({
      notifications: { previewEnabled: true },
    });
  });

  it('rejects schemaVersion tampering and unknown keys', () => {
    expect(SettingsPatchSchema.safeParse({ schemaVersion: 9 }).success).toBe(false);
    expect(SettingsPatchSchema.safeParse({ apiToken: 'x' }).success).toBe(false);
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
