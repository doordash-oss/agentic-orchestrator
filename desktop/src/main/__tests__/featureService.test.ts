import { describe, expect, it, vi } from 'vitest';
import { SafeErrorException, requestTimeoutError } from '../../shared/errors';
import { FeatureSnapshotSchema, type ReadinessSnapshot } from '../../shared/ipc';
import { FeatureService, type FeatureServiceDeps } from '../features';
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
      effort: { planning: 'high', implementation: 'max' },
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
      active_run: 1,
      run_count: 1,
      repos: ['repo-a'],
      created_at: '2026-07-14T10:00:00Z',
      checkpoints: {},
      progress: {},
      models: {},
      automatic_review: { mode: 'default', enabled: true, source: 'global' },
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
  resolveRepositoryFiles: FeatureServiceDeps['resolveRepositoryFiles'] = () => Promise.resolve([]),
  extras: Partial<FeatureServiceDeps> = {},
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
    resolveRepositoryFiles,
    ...extras,
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
    expect(defaults.defaults.effort).toEqual([
      { phase: 'Planning', effort: 'high' },
      { phase: 'Implementation', effort: 'max' },
    ]);
  });
});

describe('FeatureService remote-connection path stripping', () => {
  const created = () => ({
    status: 201,
    body: { api_version: 'v1', result: 'created', feature_id: 'abcd1234ef567890' },
  });

  it('feature create drops local image/attachment/repository-file paths remotely and logs the strip', async () => {
    const resolveRepositoryFiles = vi.fn(() => Promise.resolve(['/resolved/repo-a/query.ts']));
    const log = vi.fn();
    const { service, calls } = makeService(created, resolveRepositoryFiles, {
      locality: () => 'remote',
      log,
    });
    await service.createFeature({
      name: 'Staged before the switch',
      description: '',
      repoKeys: ['repo-a'],
      useCurrentBranch: false,
      images: ['/staged/shot.png'],
      attachments: ['/staged/spec.pdf'],
      repositoryFiles: [{ repoKey: 'repo-a', path: 'src/query.ts' }],
    });
    expect(resolveRepositoryFiles).not.toHaveBeenCalled();
    expect(calls[0]?.init?.body).toEqual(expect.objectContaining({ images: [], attachments: [] }));
    expect(JSON.stringify(calls)).not.toContain('/staged/');
    expect(log).toHaveBeenCalledTimes(1);
    expect(String(log.mock.calls[0]?.[0])).toContain('stripped 3');
  });

  it('feature create sends staged paths unchanged when the connection is local', async () => {
    const resolveRepositoryFiles = vi.fn((refs: readonly { repoKey: string; path: string }[]) =>
      Promise.resolve(refs.map((ref) => `/resolved/${ref.repoKey}/${ref.path}`)),
    );
    const { service, calls } = makeService(created, resolveRepositoryFiles, {
      locality: () => 'local',
    });
    await service.createFeature({
      name: 'Staged locally',
      description: '',
      repoKeys: ['repo-a'],
      useCurrentBranch: false,
      images: ['/staged/shot.png'],
      attachments: ['/staged/spec.pdf'],
      repositoryFiles: [{ repoKey: 'repo-a', path: 'src/query.ts' }],
    });
    expect(calls[0]?.init?.body).toEqual(
      expect.objectContaining({
        images: ['/staged/shot.png'],
        attachments: ['/staged/spec.pdf', '/resolved/repo-a/src/query.ts'],
      }),
    );
  });

  it('refactor launch drops staged paths remotely and skips the repository-file walk', async () => {
    const resolveRepositoryFiles = vi.fn(() => Promise.resolve(['/resolved/repo-a/query.ts']));
    const log = vi.fn();
    const { service, calls } = makeService(
      () => ({
        status: 202,
        body: {
          api_version: 'v1',
          feature_id: 'child1234ef567890',
          parent_id: 'abcd1234ef567890',
          result: 'created',
        },
      }),
      resolveRepositoryFiles,
      { locality: () => 'remote', log },
    );
    await service.launchRefactorChild({
      parentId: 'abcd1234ef567890',
      name: 'Extract search core',
      images: ['/staged/shot.png'],
      attachments: ['/staged/spec.pdf'],
      repositoryFiles: [{ repoKey: 'repo-a', path: 'src/query.ts' }],
    });
    expect(resolveRepositoryFiles).not.toHaveBeenCalled();
    const body = calls[0]?.init?.body as Record<string, unknown>;
    expect('images' in body).toBe(false);
    expect('attachments' in body).toBe(false);
    expect(JSON.stringify(calls)).not.toContain('/staged/');
    expect(log).toHaveBeenCalledTimes(1);
  });

  it('rejects a creation payload that tries to smuggle locality past the boundary', async () => {
    const resolveRepositoryFiles = vi.fn(() => Promise.resolve([]));
    const { service, calls } = makeService(created, resolveRepositoryFiles, {
      locality: () => 'remote',
      log: vi.fn(),
    });
    await expect(
      service.createFeature({
        name: 'Spoof attempt',
        description: '',
        repoKeys: ['repo-a'],
        useCurrentBranch: false,
        images: ['/staged/shot.png'],
        kind: 'local',
      } as never),
    ).rejects.toMatchObject({ safe: { code: 'E_SCHEMA_MISMATCH' } });
    expect(calls).toHaveLength(0);
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

  it('posts explicit per-phase effort without materializing untouched defaults', async () => {
    const { service, calls } = makeService(() => ({
      status: 201,
      body: { api_version: 'v1', result: 'created', feature_id: 'abcd1234ef567890' },
    }));
    await service.createFeature({
      ...input,
      models: { planning: 'claude:opus' },
      effort: { planning: 'max' },
    });
    expect(calls[0]?.init?.body).toEqual(
      expect.objectContaining({
        models: { planning: 'claude:opus' },
        effort: { planning: 'max' },
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
      // Publish commits, pushes, and opens or updates a pull request per
      // repository; the ordinary 30-second bound would abort mid-flight.
      timeoutMs: 600000,
    });
  });

  it('gives merge the same long request bound publish gets', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: { api_version: 'v1', feature_id: 'abcd1234ef567890', result: 'merged' },
    }));

    await service.dispatchAction({
      featureId: 'abcd1234ef567890',
      action: 'merge',
      body: { source_revision: 'rev-1' },
    });

    expect(calls[0]?.init?.timeoutMs).toBe(600000);
  });

  it('keeps the ordinary bound for short lifecycle actions', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: { api_version: 'v1', feature_id: 'abcd1234ef567890', result: 'started' },
    }));

    await service.dispatchAction({ featureId: 'abcd1234ef567890', action: 'start' });

    expect(calls[0]?.init?.timeoutMs).toBeUndefined();
  });

  it('retains the single-flight entry after a request timeout so no duplicate publish is issued', async () => {
    const request = vi.fn((path: string) => {
      if (path.endsWith('/actions/publish')) {
        return Promise.reject(new SafeErrorException(requestTimeoutError()));
      }
      return Promise.resolve({ status: 200, body: detailBody() });
    });
    const { service } = makeService(request as never);
    const input = {
      featureId: 'abcd1234ef567890',
      action: 'publish' as const,
      body: { source_revision: 'rev-1', repos: ['repo-a'], title: 'Ship it' },
    };

    await expect(service.dispatchAction(input)).rejects.toMatchObject({
      safe: { code: 'E_REQUEST_TIMEOUT' },
    });
    // A second click must not reach the server: the first publish is still
    // running there.
    await expect(service.dispatchAction(input)).rejects.toMatchObject({
      safe: { code: 'E_REQUEST_TIMEOUT' },
    });
    expect(request.mock.calls.filter(([path]) => path.endsWith('/actions/publish'))).toHaveLength(
      1,
    );
  });

  it('drops the single-flight entry after an ordinary rejection so a retry is possible', async () => {
    const request = vi.fn((path: string) =>
      path.endsWith('/actions/publish')
        ? Promise.resolve({ status: 409, body: { error: { code: 'conflict', message: 'no' } } })
        : Promise.resolve({ status: 200, body: detailBody() }),
    );
    const { service } = makeService(request as never);
    const input = {
      featureId: 'abcd1234ef567890',
      action: 'publish' as const,
      body: { source_revision: 'rev-1', repos: ['repo-a'], title: 'Ship it' },
    };

    await expect(service.dispatchAction(input)).rejects.toThrow();
    await expect(service.dispatchAction(input)).rejects.toThrow();
    expect(request.mock.calls.filter(([path]) => path.endsWith('/actions/publish'))).toHaveLength(
      2,
    );
  });

  it('forwards max-iteration restart deltas to the server action endpoint', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        feature_id: 'abcd1234ef567890',
        result: 'restarted',
        phase: 'implement',
      },
    }));

    await expect(
      service.dispatchAction({
        featureId: 'abcd1234ef567890',
        action: 'restart',
        body: {
          max_iterations_delta: 10,
          max_plan_iterations_delta: 2,
        },
      }),
    ).resolves.toMatchObject({
      featureId: 'abcd1234ef567890',
      action: 'restart',
      result: 'restarted',
      phase: 'implement',
      sessionIds: [],
    });
    expect(calls[0]?.path).toBe('/api/v1/features/abcd1234ef567890/actions/restart');
    expect(calls[0]?.init).toStrictEqual({
      method: 'POST',
      body: {
        max_iterations_delta: 10,
        max_plan_iterations_delta: 2,
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
      timeoutMs: 6 * 60_000,
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
    expect(snapshot.reviewGate).toEqual({
      reviewingGate: false,
      reviewFixing: false,
      validatingPlan: false,
      validatorStatuses: {},
    });
    expect(Reflect.get(snapshot, 'automaticReview')).toEqual({
      mode: 'default',
      enabled: true,
      source: 'global',
    });
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

  it('maps roadmap phase, total, iteration, and phase status from the active run detail', async () => {
    const body = detailBody({
      status: 'Implementing',
      current_phase: 'Implement',
      active_run_detail: {
        run_number: 1,
        artifact_count: 0,
        roadmap_phase: 2,
        roadmap_total: 5,
        iteration: 3,
        phase_status: 'implementing',
      },
    });
    const { service } = makeService(() => ({ status: 200, body }));

    await expect(service.getFeature('abcd1234ef567890')).resolves.toMatchObject({
      currentRoadmapPhase: 2,
      totalRoadmapPhases: 5,
      currentIteration: 3,
      phaseStatus: 'implementing',
    });
  });

  it('falls back to feature progress when the active run detail omits roadmap fields', async () => {
    const body = detailBody({
      status: 'Implementing',
      current_phase: 'Implement',
      active_run_detail: { run_number: 1, artifact_count: 0 },
      progress: {
        current_iteration: 4,
        current_roadmap_phase: 1,
        total_roadmap_phases: 3,
        current_phase_status: 'reviewing',
      },
    });
    const { service } = makeService(() => ({ status: 200, body }));

    await expect(service.getFeature('abcd1234ef567890')).resolves.toMatchObject({
      currentRoadmapPhase: 1,
      totalRoadmapPhases: 3,
      currentIteration: 4,
      phaseStatus: 'reviewing',
    });
  });

  it('preserves implementation review gate and axis status', async () => {
    const body = detailBody({
      review_gate: {
        reviewing_gate: true,
        review_fixing: true,
        validating_plan: false,
        validator_statuses: {
          Craft: 'APPROVED',
          'Functionality/Evidence': 'running',
        },
      },
    });
    const { service } = makeService(() => ({ status: 200, body }));

    await expect(service.getFeature('abcd1234ef567890')).resolves.toMatchObject({
      reviewGate: {
        reviewingGate: true,
        reviewFixing: true,
        validatorStatuses: {
          Craft: 'APPROVED',
          'Functionality/Evidence': 'running',
        },
      },
    });
  });

  it('carries ordered harness verification items into the snapshot', async () => {
    const body = detailBody({
      verification_items: [
        { name: 'go test ./...', state: 'passed' },
        { name: 'npm run build', state: 'running' },
      ],
    });
    const { service } = makeService(() => ({ status: 200, body }));

    await expect(service.getFeature('abcd1234ef567890')).resolves.toMatchObject({
      verificationItems: [
        { name: 'go test ./...', state: 'passed' },
        { name: 'npm run build', state: 'running' },
      ],
    });
  });

  it('omits verificationItems when the server sends none', async () => {
    const { service } = makeService(() => ({ status: 200, body: detailBody() }));
    const snapshot = await service.getFeature('abcd1234ef567890');
    expect(snapshot.verificationItems).toBeUndefined();
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

  it('carries the bounded history counters and the body-less diff flag', async () => {
    const { service } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        features: [
          {
            id: 'abcd1234ef567890',
            name: 'Search revamp',
            slug: 'search-revamp',
            status: 'Published',
            current_phase: 'Done',
            active_run: 1,
            run_count: 1,
            repos: ['repo-a'],
            created_at: '2026-07-14T10:00:00Z',
            checkpoints: {},
            progress: {},
            child_history: [
              {
                id: 'child0000ef567890',
                name: 'Extract search core',
                kind: 'refactor',
                display_token: 'R1',
                display_state: 'Closed — Completed',
                pipeline: 'medium',
                status: 'Done',
                outcome: 'completed',
                started_at: '2026-07-30T10:00:00Z',
                closed_at: '2026-07-31T10:00:00Z',
                cost: { total_usd: 1, by_phase: {} },
                integration_state: 'merged',
                attention: [],
                cleanup_warnings: [],
                has_diff_summary: true,
              },
            ],
            child_history_total: 12,
            child_history_truncated: true,
          },
        ],
      },
    }));
    const [summary] = await service.listFeatures();
    expect(summary?.childHistoryTotal).toBe(12);
    expect(summary?.childHistoryTruncated).toBe(true);
    expect(summary?.childHistory?.[0]).toMatchObject({ hasDiffSummary: true });
    expect(summary?.childHistory?.[0]?.diffSummary).toBeUndefined();
  });
});

describe('FeatureService relationship operations', () => {
  it('maps a child launch to the authoritative parent refactor action', async () => {
    const { service, calls } = makeService(() => ({
      status: 202,
      body: {
        api_version: 'v1',
        feature_id: 'child1234ef567890',
        parent_id: 'abcd1234ef567890',
        result: 'created',
      },
    }));
    await expect(
      service.launchRefactorChild({
        parentId: 'abcd1234ef567890',
        name: 'Extract search core',
        description: 'Move shared query behavior.',
        pipeline: 'large',
        riskLevel: 'high',
        inquireness: 'medium',
      }),
    ).resolves.toEqual({
      childId: 'child1234ef567890',
      parentId: 'abcd1234ef567890',
      result: 'created',
    });
    expect(calls).toEqual([
      {
        path: '/api/v1/features/abcd1234ef567890/actions/refactor',
        init: {
          method: 'POST',
          body: {
            name: 'Extract search core',
            description: 'Move shared query behavior.',
            pipeline: 'large',
            risk_level: 'high',
            inquireness: 'medium',
          },
        },
      },
    ]);
  });

  it('maps the full child run contract to the wire format, folding file references into attachments', async () => {
    const { service, calls } = makeService(
      () => ({
        status: 202,
        body: {
          api_version: 'v1',
          feature_id: 'child1234ef567890',
          parent_id: 'abcd1234ef567890',
          result: 'created',
        },
      }),
      (refs) => Promise.resolve(refs.map((ref) => `/resolved/${ref.repoKey}/${ref.path}`)),
    );
    await service.launchRefactorChild({
      parentId: 'abcd1234ef567890',
      name: 'Extract search core',
      images: ['/safe/sketch.png'],
      attachments: ['/safe/spec.pdf'],
      repositoryFiles: [{ repoKey: 'repo-a', path: 'src/query.ts' }],
      pipeline: 'moonshot',
      models: { planning: 'model-plan' },
      effort: { planning: 'high' },
      checkpoints: {
        inquiryReview: true,
        researchReview: false,
        designReview: true,
        roadmapReview: true,
        phasePlanReview: true,
        manualPublish: true,
        draftPublish: false,
      },
      riskLevel: 'low',
      exitCriteria: 'No behavior change.',
      inquireness: 'none',
    });
    expect(calls[0]?.init?.body).toEqual({
      name: 'Extract search core',
      images: ['/safe/sketch.png'],
      attachments: ['/safe/spec.pdf', '/resolved/repo-a/src/query.ts'],
      pipeline: 'moonshot',
      models: { planning: 'model-plan' },
      effort: { planning: 'high' },
      checkpoints: {
        inquiry_review: true,
        research_review: false,
        design_review: true,
        roadmap_review: true,
        phase_plan_review: true,
        manual_publish: true,
        draft_publish: false,
      },
      risk_level: 'low',
      exit_criteria: 'No behavior change.',
      inquireness: 'none',
    });
  });

  it('maps discard draining and typed cascade outcomes without closing optimistically', async () => {
    const { service } = makeService((path) =>
      path.includes('/discard')
        ? {
            status: 202,
            body: {
              api_version: 'v1',
              feature_id: 'child1234ef567890',
              result: 'draining sessions',
            },
          }
        : {
            status: 202,
            body: {
              api_version: 'v1',
              feature_id: 'abcd1234ef567890',
              operation_id: 'delete-7',
              status: 'cleanup_pending',
              diagnostics: [{ code: 'branch_cleanup', message: 'retry cleanup' }],
            },
          },
    );
    await expect(
      service.discardRefactorChild({ childId: 'child1234ef567890' }),
    ).resolves.toMatchObject({ status: 'draining' });
    await expect(service.deleteFeatureCascade({ featureId: 'abcd1234ef567890' })).resolves.toEqual({
      featureId: 'abcd1234ef567890',
      operationId: 'delete-7',
      status: 'cleanup_pending',
      diagnostics: [{ code: 'branch_cleanup', message: 'retry cleanup' }],
    });
  });

  it('projects active child, immutable history, impact preview, and transaction diagnostics', async () => {
    const relationship = {
      id: 'child1234ef567890',
      name: 'Extract search core',
      kind: 'refactor',
      display_token: 'R1',
      display_state: 'Review',
      pipeline: 'large',
      status: 'Running',
      relationship_state: 'active',
      started_at: '2026-07-30T10:00:00Z',
      cost: { total_usd: 2.5, by_phase: { review: 2.5 } },
      integration_state: 'attention',
      attention: [{ code: 'conflict', message: 'Resolve conflict', repo: 'repo-a' }],
      cleanup_warnings: [],
    };
    const { service } = makeService(() => ({
      status: 200,
      body: detailBody({
        active_child: relationship,
        child_history: [
          {
            ...relationship,
            id: 'child0000ef567890',
            display_state: 'Closed — Completed',
            relationship_state: 'completed',
            outcome: 'completed',
            closed_at: '2026-07-29T10:00:00Z',
            diff_summary: '3 files changed',
          },
        ],
        actions: [
          {
            id: 'delete',
            enabled: true,
            required_inputs: [],
            impact_preview: {
              kind: 'parent_cascade_delete',
              subject: { id: 'abcd1234ef567890', name: 'Search revamp' },
              categories: [{ key: 'children', label: 'Children', items: ['Extract search core'] }],
              retained: [],
            },
          },
        ],
        transaction: {
          phase: 'attention',
          attention: 'Integration needs recovery',
          entries: [
            {
              repo: 'repo-a',
              prep_state: 'prepared',
              apply_state: 'conflict',
              conflict_files: ['query.ts'],
              dirty: [{ path: '/safe/repo-a', staged_total: 1 }],
            },
          ],
        },
      }),
    }));
    const snapshot = await service.getFeature('abcd1234ef567890');
    expect(snapshot.activeChild).toMatchObject({
      id: 'child1234ef567890',
      displayToken: 'R1',
      integrationState: 'attention',
    });
    expect(snapshot.childHistory?.[0]).toMatchObject({
      outcome: 'completed',
      diffSummary: '3 files changed',
      hasDiffSummary: true,
    });
    expect(snapshot.actions[0]?.impactPreview?.categories[0]?.items).toEqual([
      'Extract search core',
    ]);
    expect(snapshot.transaction?.entries?.[0]).toMatchObject({
      applyState: 'conflict',
      conflictFiles: ['query.ts'],
      dirty: [{ path: '/safe/repo-a', stagedTotal: 1 }],
    });
    expect(() => FeatureSnapshotSchema.parse(snapshot)).not.toThrow();
  });
});

describe('FeatureService review-feedback operations', () => {
  it('maps a fetch to the typed review-feedback fetch endpoint and groups comments by repo', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        repos: [
          {
            repo: 'repo-a',
            pr_url: 'https://github.com/org/repo-a/pull/1',
            comments: [
              {
                repo: 'repo-a',
                id: 41,
                type: 'review',
                path: 'src/query.ts',
                line: 12,
                author: 'octocat',
                body: 'Bearer tok-secret leaks here',
                diff_hunk: 'clone /Users/someone/repo-a',
                in_reply_to_id: 39,
              },
              {
                repo: 'repo-a',
                id: 42,
                type: 'issue',
                body: 'plain note',
              },
            ],
          },
          {
            repo: 'repo-b',
            pr_url: 'https://github.com/org/repo-b/pull/7',
            comments: [{ repo: 'repo-b', id: 90, type: 'review_body', author: 'reviewer' }],
          },
        ],
      },
    }));
    await expect(service.fetchReviewFeedback({ featureId: 'abcd1234ef567890' })).resolves.toEqual({
      featureId: 'abcd1234ef567890',
      repos: [
        {
          repo: 'repo-a',
          prUrl: 'https://github.com/org/repo-a/pull/1',
          comments: [
            {
              repo: 'repo-a',
              id: 41,
              type: 'review',
              path: 'src/query.ts',
              line: 12,
              author: 'octocat',
              body: '[redacted] leaks here',
              diffHunk: 'clone [path]',
              inReplyToId: 39,
            },
            { repo: 'repo-a', id: 42, type: 'issue', body: 'plain note' },
          ],
        },
        {
          repo: 'repo-b',
          prUrl: 'https://github.com/org/repo-b/pull/7',
          comments: [{ repo: 'repo-b', id: 90, type: 'review_body', author: 'reviewer' }],
        },
      ],
    });
    expect(calls).toEqual([
      {
        path: '/api/v1/features/abcd1234ef567890/actions/review-feedback/fetch',
        init: { method: 'POST', body: {} },
      },
    ]);
  });

  it('redacts fetched comment bodies and diff hunks before they cross the boundary', async () => {
    const { service } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        repos: [
          {
            repo: 'repo-a',
            pr_url: 'https://github.com/org/repo-a/pull/1',
            comments: [
              {
                repo: 'repo-a',
                id: 1,
                type: 'review',
                body: 'token=Bearer abc and /Users/secret/path',
                diff_hunk: 'diff --git a/x /home/hidden',
              },
            ],
          },
        ],
      },
    }));
    const result = await service.fetchReviewFeedback({ featureId: 'abcd1234ef567890' });
    const comment = result.repos[0]?.comments[0];
    expect(comment?.body).not.toContain('Bearer abc');
    expect(comment?.body).not.toContain('/Users/secret/path');
    expect(comment?.diffHunk).not.toContain('/home/hidden');
  });

  it('maps a launch to the typed review-feedback action with selected comments and the gate', async () => {
    const { service, calls } = makeService(() => ({
      status: 201,
      body: {
        api_version: 'v1',
        feature_id: 'child1234ef567890',
        parent_id: 'abcd1234ef567890',
        result: 'created',
      },
    }));
    const comments = [
      {
        repo: 'repo-a',
        id: 41,
        type: 'review' as const,
        path: 'src/query.ts',
        line: 12,
        author: 'octocat',
        body: 'redacted body',
        diffHunk: 'redacted hunk',
        inReplyToId: 39,
      },
      { repo: 'repo-b', id: 90, type: 'review_body' as const, author: 'reviewer' },
    ];
    await expect(
      service.launchReviewFeedbackChild({ parentId: 'abcd1234ef567890', comments, gate: true }),
    ).resolves.toEqual({
      childId: 'child1234ef567890',
      parentId: 'abcd1234ef567890',
      result: 'created',
    });
    expect(calls).toEqual([
      {
        path: '/api/v1/features/abcd1234ef567890/actions/review-feedback',
        init: {
          method: 'POST',
          body: {
            comments: [
              {
                repo: 'repo-a',
                id: 41,
                type: 'review',
                path: 'src/query.ts',
                line: 12,
                author: 'octocat',
                body: 'redacted body',
                diff_hunk: 'redacted hunk',
                in_reply_to_id: 39,
              },
              { repo: 'repo-b', id: 90, type: 'review_body', author: 'reviewer' },
            ],
            gate: true,
          },
        },
      },
    ]);
  });

  it('omits the gate field when the modal did not collect one', async () => {
    const { service, calls } = makeService(() => ({
      status: 201,
      body: {
        api_version: 'v1',
        feature_id: 'child1234ef567890',
        parent_id: 'abcd1234ef567890',
        result: 'created',
      },
    }));
    await service.launchReviewFeedbackChild({
      parentId: 'abcd1234ef567890',
      comments: [{ repo: 'repo-a', id: 41, type: 'review' }],
    });
    expect(calls[0]?.init?.body).not.toHaveProperty('gate');
  });
});
