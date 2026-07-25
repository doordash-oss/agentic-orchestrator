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
});
