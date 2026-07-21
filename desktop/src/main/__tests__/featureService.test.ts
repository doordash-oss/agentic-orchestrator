import { describe, expect, it, vi } from 'vitest';
import { FeatureSnapshotSchema, type ReadinessSnapshot } from '../../shared/ipc';
import { FeatureService } from '../features';
import type { ApiRequestInit, HttpResult } from '../gateway/runtimeGateway';

interface Call {
  path: string;
  init?: ApiRequestInit;
}

function readiness(): ReadinessSnapshot {
  return {
    ready: true,
    providers: [{ name: 'claude', installed: true, ready: true }],
    models: { available: true },
    configuration: { valid: true },
    workspaceRoots: [{ path: '/work/space', valid: true }],
    repositories: [
      { name: 'repo-a', path: '/work/space/repo-a', valid: true },
      {
        name: 'repo-b',
        path: '/work/space/repo-b',
        valid: false,
        issue: { code: 'invalid_repository', message: 'Not a git repository.' },
      },
    ],
    issues: [],
  };
}

function runtimeConfigBody(): Record<string, unknown> {
  return {
    api_version: 'v1',
    feature_defaults: {
      models: { planning: 'model-plan', implementation: 'model-impl', utilities: '' },
      inquireness: 'medium',
      pipeline: 'medium',
      checkpoints: {},
    },
  };
}

function detailBody(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    api_version: 'v1',
    feature: {
      id: 'abcd1234ef567890',
      name: 'Search revamp',
      slug: 'search-revamp',
      status: 'SettingUpWorktrees',
      current_phase: 'Plan',
      pipeline: 'medium',
      description: 'Improve search.',
      cycle: {},
      active_run: 1,
      run_count: 1,
      repos: ['repo-a'],
      created_at: '2026-07-14T10:00:00Z',
      checkpoints: {},
      progress: {},
      models: {},
      historical_runs: [],
      repo_status: [],
      timing: { total_seconds: 0, by_phase: {} },
      cost: { total_usd: 0, by_phase: {} },
      review_gate: { reviewing_gate: false, review_fixing: false, validating_plan: false },
      actions: [
        {
          id: 'setup',
          enabled: false,
          scope: { type: 'feature' },
          required_inputs: [],
          disabled_reasons: [{ code: 'no_pending_setup', message: 'nothing to retry' }],
        },
        { id: 'start', enabled: true, scope: { type: 'feature' }, required_inputs: [] },
      ],
      active_run_detail: {
        run_number: 1,
        artifact_count: 0,
        setup: {
          status: 'running',
          attempt: 1,
          task_order: ['worktree:repo-a', 'kb:repo-a'],
          tasks: {
            // Deliberately declared in non-execution order: the server map
            // has no order; task_order is authoritative.
            'kb:repo-a': { key: 'kb:repo-a', kind: 'kb', status: 'queued' },
            'worktree:repo-a': {
              key: 'worktree:repo-a',
              kind: 'worktree',
              label: 'Create worktree',
              repo: 'repo-a',
              status: 'running',
              branch: 'feature/search-revamp',
              attempt: 2,
            },
          },
        },
      },
      revision: 'r1',
      cache_revalidate: 'x',
      ...overrides,
    },
  };
}

function makeService(
  respond: (path: string, init?: ApiRequestInit) => HttpResult | Promise<HttpResult>,
) {
  const calls: Call[] = [];
  const service = new FeatureService({
    transport: {
      apiRequest: (path, init) => {
        calls.push(init === undefined ? { path } : { path, init });
        return Promise.resolve(respond(path, init));
      },
    },
    readReadiness: () => Promise.resolve(readiness()),
    resolveRepositoryFiles: () => Promise.resolve([]),
  });
  return { service, calls };
}

describe('FeatureService.creationDefaults', () => {
  it('composes fresh repository eligibility and server defaults in one call', async () => {
    const { service } = makeService(() => ({ status: 200, body: runtimeConfigBody() }));
    const defaults = await service.creationDefaults();
    expect(defaults.repositories).toHaveLength(2);
    expect(defaults.repositories[1]?.valid).toBe(false);
    expect(defaults.defaults.pipeline).toBe('medium');
    expect(defaults.defaults.inquireness).toBe('medium');
    expect(defaults.defaults.useCurrentBranch).toBe(false);
    expect(defaults.defaults.models).toEqual([
      { phase: 'Planning', model: 'model-plan' },
      { phase: 'Implementation', model: 'model-impl' },
    ]);
  });
});

describe('FeatureService.createFeature', () => {
  const input = {
    name: '  Search revamp  ',
    description: '',
    repoKeys: ['repo-a'],
    useCurrentBranch: false,
  };

  it('posts the narrow creation contract and returns the created feature id', async () => {
    const { service, calls } = makeService(() => ({
      status: 201,
      body: { api_version: 'v1', result: 'created', feature_id: 'abcd1234ef567890' },
    }));
    const result = await service.createFeature(input);
    expect(result).toEqual({ featureId: 'abcd1234ef567890' });
    expect(calls[0]?.path).toBe('/api/v1/features');
    expect(calls[0]?.init).toEqual(
      expect.objectContaining({
        method: 'POST',
        body: expect.objectContaining({ name: 'Search revamp', repos: ['repo-a'] }),
      }),
    );
    expect(calls[0]?.init?.body).toEqual(
      expect.objectContaining({
        pipeline: 'medium',
        risk_level: 'medium',
        inquireness: 'medium',
        idempotency_key: expect.stringMatching(/^[0-9a-f-]{36}$/),
      }),
    );
  });

  it('carries the current-branch choice and description when provided', async () => {
    const { service, calls } = makeService(() => ({
      status: 201,
      body: { api_version: 'v1', result: 'created', feature_id: 'abcd1234ef567890' },
    }));
    await service.createFeature({
      ...input,
      description: 'Improve search.',
      useCurrentBranch: true,
    });
    expect(calls[0]?.init?.body).toEqual(
      expect.objectContaining({
        name: 'Search revamp',
        description: 'Improve search.',
        repos: ['repo-a'],
        use_current_branch: true,
      }),
    );
  });

  it('maps the structured 409 not_ready rejection with its issues, redacted', async () => {
    const { service } = makeService(() => ({
      status: 409,
      body: {
        api_version: 'v1',
        error: {
          code: 'not_ready',
          message: 'runtime is not ready to create features',
          status: 409,
          target: {
            issues: [
              { code: 'unauthenticated', message: 'claude is not signed in at /Users/x/secret' },
            ],
          },
        },
      },
    }));
    const err = await service.createFeature(input).catch((e: unknown) => e);
    expect(err).toMatchObject({ safe: { code: 'not_ready' } });
    const safe = (err as { safe: { message: string; remediation?: string } }).safe;
    expect(safe.remediation).toContain('claude is not signed in');
    expect(safe.remediation).not.toContain('/Users/x');
  });

  it('maps plain 400 validation errors to their server code', async () => {
    const { service } = makeService(() => ({
      status: 400,
      body: { api_version: 'v1', error: { code: 'bad_request', message: 'name is required' } },
    }));
    await expect(service.createFeature({ ...input, name: 'x' })).rejects.toMatchObject({
      safe: { code: 'bad_request' },
    });
  });

  it('fails closed on an unstructured error body without echoing it', async () => {
    const { service } = makeService(() => ({ status: 500, body: 'Bearer tok-leak-1 exploded' }));
    const err = await service.createFeature(input).catch((e: unknown) => e);
    expect(err).toMatchObject({ safe: { code: 'E_HTTP_500' } });
    expect(JSON.stringify((err as { safe: unknown }).safe)).not.toContain('tok-leak-1');
  });

  it('rejects malformed input before any request reaches the transport', async () => {
    const { service, calls } = makeService(() => ({ status: 201, body: {} }));
    await expect(
      service.createFeature({
        name: '   ',
        description: '',
        repoKeys: ['r'],
        useCurrentBranch: false,
      }),
    ).rejects.toMatchObject({ safe: { code: 'E_SCHEMA_MISMATCH' } });
    await expect(
      service.createFeature({ name: 'ok', description: '', repoKeys: [], useCurrentBranch: false }),
    ).rejects.toMatchObject({ safe: { code: 'E_SCHEMA_MISMATCH' } });
    expect(calls).toHaveLength(0);
  });
});

describe('FeatureService.dispatchSetup', () => {
  it('dispatches the durable setup action for the same feature', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: { api_version: 'v1', result: 'setup_started', feature_id: 'abcd1234ef567890' },
    }));
    const result = await service.dispatchSetup('abcd1234ef567890');
    expect(result).toEqual({ result: 'setup_started' });
    expect(calls[0]?.path).toBe('/api/v1/features/abcd1234ef567890/actions/setup');
    expect(calls[0]?.init?.method).toBe('POST');
  });

  it('rejects id shapes that could smuggle path segments, without a request', async () => {
    const { service, calls } = makeService(() => ({ status: 200, body: {} }));
    for (const bad of ['../other', 'id/with/slash', 'id?x=1', '']) {
      await expect(service.dispatchSetup(bad)).rejects.toMatchObject({
        safe: { code: 'E_SCHEMA_MISMATCH' },
      });
    }
    expect(calls).toHaveLength(0);
  });
});

describe('FeatureService.dispatchAction', () => {
  it('dispatches the exact start action once while concurrent callers share the flight', async () => {
    let resolve!: (value: { status: number; body: unknown }) => void;
    const request = vi.fn(
      () =>
        new Promise<{ status: number; body: unknown }>((done) => {
          resolve = done;
        }),
    );
    const { service } = makeService(request);
    const input = { featureId: 'abcd1234ef567890', action: 'start' as const };
    const first = service.dispatchAction(input);
    const second = service.dispatchAction(input);
    expect(request).toHaveBeenCalledOnce();
    resolve({
      status: 200,
      body: {
        api_version: 'v1',
        feature_id: input.featureId,
        result: 'started',
        phase: 'implement',
        session_ids: ['session-1'],
      },
    });
    await expect(first).resolves.toStrictEqual({
      featureId: input.featureId,
      action: 'start',
      result: 'started',
      phase: 'implement',
      sessionIds: ['session-1'],
    });
    await expect(second).resolves.toStrictEqual(await first);
    expect(request).toHaveBeenCalledWith(`/api/v1/features/${input.featureId}/actions/start`, {
      method: 'POST',
      body: {},
    });
  });

  it('forwards completion action bodies to the server action endpoint', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        feature_id: 'abcd1234ef567890',
        result: 'published',
      },
    }));
    await expect(
      service.dispatchAction({
        featureId: 'abcd1234ef567890',
        action: 'publish',
        body: {
          source_revision: 'rev-1',
          repos: ['repo-a'],
          title: 'Ship reviewed changes',
        },
      }),
    ).resolves.toMatchObject({
      featureId: 'abcd1234ef567890',
      action: 'publish',
      result: 'published',
      sessionIds: [],
    });
    expect(calls[0]?.path).toBe('/api/v1/features/abcd1234ef567890/actions/publish');
    expect(calls[0]?.init).toStrictEqual({
      method: 'POST',
      body: {
        source_revision: 'rev-1',
        repos: ['repo-a'],
        title: 'Ship reviewed changes',
      },
    });
  });

  it('requests a server-authored publish narrative for selected repositories only', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        feature_id: 'abcd1234ef567890',
        title: 'Ship reviewed changes',
        body: 'Generated from server-owned feature and repository context.',
        result: 'generated',
      },
    }));

    await expect(
      service.generatePublishDescription('abcd1234ef567890', ['repo-a']),
    ).resolves.toStrictEqual({
      featureId: 'abcd1234ef567890',
      title: 'Ship reviewed changes',
      body: 'Generated from server-owned feature and repository context.',
    });
    expect(calls[0]?.path).toBe('/api/v1/features/abcd1234ef567890/actions/publish/description');
    expect(calls[0]?.init).toStrictEqual({
      method: 'POST',
      body: { repos: ['repo-a'] },
    });
  });

  it('rejects every action outside the audited allowlist before transport', async () => {
    const { service, calls } = makeService(() => ({ status: 200, body: {} }));
    await expect(
      service.dispatchAction({ featureId: 'abcd1234ef567890', action: '../start' as never }),
    ).rejects.toMatchObject({ safe: { code: 'E_SCHEMA_MISMATCH' } });
    expect(calls).toHaveLength(0);
  });
});

describe('FeatureService.getFeature', () => {
  it('maps the authoritative detail into the strict renderer snapshot', async () => {
    const { service } = makeService(() => ({ status: 200, body: detailBody() }));
    const snapshot = await service.getFeature('abcd1234ef567890');
    expect(FeatureSnapshotSchema.parse(snapshot)).toEqual(snapshot);
    expect(snapshot.name).toBe('Search revamp');
    expect(snapshot.status).toBe('SettingUpWorktrees');
    expect(snapshot.actions).toEqual([
      {
        id: 'setup',
        enabled: false,
        disabledReasons: [{ code: 'no_pending_setup', message: 'nothing to retry' }],
        inputs: [],
      },
      { id: 'start', enabled: true, disabledReasons: [], inputs: [] },
    ]);
  });

  it('orders setup tasks by the server-owned task_order', async () => {
    const { service } = makeService(() => ({ status: 200, body: detailBody() }));
    const snapshot = await service.getFeature('abcd1234ef567890');
    expect(snapshot.setup?.status).toBe('running');
    expect(snapshot.setup?.tasks.map((task) => task.key)).toEqual(['worktree:repo-a', 'kb:repo-a']);
    expect(snapshot.setup?.tasks[0]).toMatchObject({
      label: 'Create worktree',
      repo: 'repo-a',
      status: 'running',
      branch: 'feature/search-revamp',
      attempt: 2,
    });
    expect(snapshot.setup?.tasks[1]).toMatchObject({ label: 'kb:repo-a', attempt: 0 });
  });

  it('redacts failure and task error text before it crosses the boundary', async () => {
    const body = detailBody({
      failure: { type: 'worktree_setup', message: 'Bearer tok-x failed' },
    });
    const feature = (body['feature'] ?? {}) as Record<string, unknown>;
    const run = (feature['active_run_detail'] ?? {}) as Record<string, unknown>;
    const setup = (run['setup'] ?? {}) as Record<string, unknown>;
    setup['status'] = 'failed';
    setup['last_error'] = 'clone failed at /Users/someone/repo';
    const { service } = makeService(() => ({ status: 200, body }));
    const snapshot = await service.getFeature('abcd1234ef567890');
    expect(snapshot.failure?.message).not.toContain('tok-x');
    expect(snapshot.setup?.lastError).not.toContain('/Users/someone');
  });

  it('omits the setup section rather than mislabel an unknown lifecycle value', async () => {
    const body = detailBody();
    const feature = (body['feature'] ?? {}) as Record<string, unknown>;
    const run = (feature['active_run_detail'] ?? {}) as Record<string, unknown>;
    (run['setup'] as Record<string, unknown>)['status'] = 'exploded';
    const { service } = makeService(() => ({ status: 200, body }));
    const snapshot = await service.getFeature('abcd1234ef567890');
    expect(snapshot.setup).toBeUndefined();
  });

  it('maps a 404 to the stable not_found code', async () => {
    const { service } = makeService(() => ({
      status: 404,
      body: { api_version: 'v1', error: { code: 'not_found', message: 'feature not found' } },
    }));
    await expect(service.getFeature('abcd1234ef567890')).rejects.toMatchObject({
      safe: { code: 'not_found' },
    });
  });
});

describe('FeatureService.listFeatures', () => {
  it('maps summaries into the strict renderer view', async () => {
    const { service } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        features: [
          {
            id: 'abcd1234ef567890',
            name: 'Search revamp',
            slug: 'search-revamp',
            status: 'Created',
            current_phase: 'Plan',
            cycle: {},
            active_run: 1,
            run_count: 1,
            repos: ['repo-a'],
            created_at: '2026-07-14T10:00:00Z',
            checkpoints: {},
            progress: {},
          },
        ],
      },
    }));
    const features = await service.listFeatures();
    expect(features).toEqual([
      {
        id: 'abcd1234ef567890',
        name: 'Search revamp',
        status: 'Created',
        currentPhase: 'Plan',
        repos: ['repo-a'],
        createdAt: '2026-07-14T10:00:00Z',
        activeRun: 1,
        runCount: 1,
        warnings: [],
      },
    ]);
  });
});
