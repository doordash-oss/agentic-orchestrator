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
import { ConfigService } from '../configService';
import type { ApiRequestInit, HttpResult } from '../gateway/runtimeGateway';

interface Call {
  path: string;
  init?: ApiRequestInit;
}

function makeService(
  respond: (path: string, init?: ApiRequestInit) => HttpResult | Promise<HttpResult>,
) {
  const calls: Call[] = [];
  const service = new ConfigService({
    apiRequest: (path, init) => {
      calls.push(init === undefined ? { path } : { path, init });
      return Promise.resolve(respond(path, init));
    },
  });
  return { service, calls };
}

const checkpoints = {
  inquiry_review: false,
  research_review: false,
  design_review: false,
  roadmap_review: true,
  phase_plan_review: true,
  manual_publish: true,
  draft_publish: false,
};

describe('ConfigService effort routing', () => {
  it('round-trips the feature automatic-review override and reviewer model', async () => {
    const featureConfig = {
      api_version: 'v1',
      feature_id: 'feat-1',
      current: {
        models: { automatic_review: 'claude:haiku' },
        inquireness: 'medium',
        checkpoints,
        automatic_review_mode: 'enabled',
      },
      defaults: {
        models: { automatic_review: '' },
        inquireness: 'medium',
        checkpoints,
        automatic_review_mode: 'default',
      },
      publishability: { manual_publish: true },
    };
    const { service, calls } = makeService((_path, init) =>
      init?.method === 'POST'
        ? { status: 200, body: { api_version: 'v1' } }
        : { status: 200, body: featureConfig },
    );

    const snapshot = await service.getFeatureConfig('feat-1');
    expect(Reflect.get(snapshot.current, 'automaticReviewMode')).toBe('enabled');
    expect(Reflect.get(snapshot.current.models, 'automaticReview')).toBe('claude:haiku');

    const config = Object.assign({}, snapshot.current, { automaticReviewMode: 'disabled' });
    await service.updateFeatureConfig({ featureId: 'feat-1', config });
    expect(calls[1]?.init?.body).toEqual(
      expect.objectContaining({
        automatic_review_mode: 'disabled',
        models: expect.objectContaining({ automatic_review: 'claude:haiku' }),
      }),
    );
  });

  it('round-trips workspace automatic-review enablement and reviewer selection', async () => {
    const runtimeConfig = {
      api_version: 'v1',
      feature_defaults: {
        models: { automatic_review: 'opencode:anthropic/claude-haiku' },
        inquireness: 'medium',
        checkpoints,
        pipeline: 'large',
        automatic_review_enabled: true,
      },
      notifications: { mute_feature_input: false },
    };
    const { service, calls } = makeService((_path, init) =>
      init?.method === 'PATCH'
        ? { status: 200, body: { api_version: 'v1' } }
        : { status: 200, body: runtimeConfig },
    );

    const defaults = await service.getWorkspaceDefaults();
    expect(Reflect.get(defaults, 'automaticReviewEnabled')).toBe(true);
    expect(Reflect.get(defaults.models, 'automaticReview')).toBe('opencode:anthropic/claude-haiku');

    await service.updateWorkspaceDefaults(
      Object.assign({}, defaults, { automaticReviewEnabled: false }),
    );
    expect(calls[1]?.init?.body).toEqual(
      expect.objectContaining({
        defaults: expect.objectContaining({
          automatic_review_enabled: false,
          models: expect.objectContaining({
            automatic_review: 'opencode:anthropic/claude-haiku',
          }),
        }),
      }),
    );
  });

  it('round-trips per-phase effort through feature config updates', async () => {
    const featureConfig = {
      api_version: 'v1',
      feature_id: 'feat-1',
      current: {
        models: { implementation: 'claude:opus', kb_build: 'claude:sonnet' },
        effort: { implementation: 'high', kb_build: 'medium' },
        inquireness: 'medium',
        checkpoints,
        pipeline: 'large',
        input_notifications: 'default',
      },
      defaults: {
        models: { implementation: 'claude:sonnet' },
        effort: { implementation: 'auto' },
        inquireness: 'medium',
        checkpoints,
        pipeline: 'large',
        input_notifications: 'default',
      },
      publishability: { manual_publish: true },
    };
    const { service, calls } = makeService((path, init) =>
      init?.method === 'POST'
        ? { status: 200, body: { api_version: 'v1' } }
        : { status: 200, body: featureConfig },
    );

    const snapshot = await service.getFeatureConfig('feat-1');
    expect(snapshot.current.effort).toEqual({ implementation: 'high', kbBuild: 'medium' });

    await service.updateFeatureConfig({
      featureId: 'feat-1',
      config: { ...snapshot.current, effort: { implementation: 'max', kbBuild: 'low' } },
    });
    expect(calls[1]?.init?.body).toEqual(
      expect.objectContaining({
        effort: expect.objectContaining({ implementation: 'max', kb_build: 'low' }),
      }),
    );
  });

  it('preserves ordered effort capabilities in the model catalogue', async () => {
    const { service } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        provider_order: ['claude'],
        provider_models: {
          claude: [
            {
              id: 'opus',
              display_name: 'Opus',
              aliases: ['opus-latest'],
              effort_capabilities: ['low', 'medium', 'high', 'max'],
            },
          ],
        },
        phase_defaults: { implementation: 'opus' },
        phase_provider_models: { implementation: { claude: ['opus'] } },
      },
    }));

    const catalogue = await service.getModelCatalogue();
    expect(catalogue.providerModels.claude?.[0]?.aliases).toEqual(['opus-latest']);
    expect(catalogue.providerModels.claude?.[0]?.effortCapabilities).toEqual([
      'low',
      'medium',
      'high',
      'max',
    ]);
  });

  it('refreshes one provider and converts the combined readiness and catalogue response', async () => {
    const { service, calls } = makeService(() => ({
      status: 200,
      body: {
        api_version: 'v1',
        readiness: {
          api_version: 'v1',
          ready: true,
          providers: [
            {
              name: 'claude',
              installed: true,
              version: '2.1.220',
              ready: true,
            },
          ],
          models: { available: true, models: ['claude-new'] },
          configuration: { valid: true },
          workspace: { roots: [], repositories: [] },
          issues: [],
        },
        catalog: {
          api_version: 'v1',
          provider_order: ['claude'],
          provider_models: {
            claude: [{ id: 'claude-new', display_name: 'Claude New', category: 'capable' }],
          },
          phase_defaults: { implementation: 'claude-new' },
          phase_provider_models: { implementation: { claude: ['claude-new'] } },
        },
      },
    }));

    const result = await service.refreshProviderModels('claude');

    expect(calls).toEqual([
      {
        path: '/api/v1/catalog/models/refresh',
        init: { method: 'POST', body: { provider: 'claude' } },
      },
    ]);
    expect(result.readiness.providers[0]).toEqual({
      name: 'claude',
      installed: true,
      version: '2.1.220',
      ready: true,
    });
    expect(result.catalogue.providerModels.claude).toEqual([
      { id: 'claude-new', displayName: 'Claude New', category: 'capable' },
    ]);
  });
});
