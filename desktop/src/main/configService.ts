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
 * Main-process adapter for structured configuration: per-feature config
 * (models per phase, inquireness, gates), workspace defaults, and the model
 * catalogue that powers the pickers. Everything talks to the authoritative
 * server through the gateway transport; the renderer never composes REST
 * paths or sees snake_case server shapes.
 */
import type { z } from 'zod';
import {
  ActionResponseSchema,
  FeatureConfigResponseSchema,
  ModelCatalogResponseSchema,
  ProviderModelRefreshResponseSchema,
  RuntimeConfigResponseSchema,
  type ServerEffortDefaultsSchema,
  type ServerModelDefaultsSchema,
  type ServerCheckpointsSchema,
  type ServerFeatureConfigSchema,
} from '../shared/api/parse';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import {
  FeatureConfigUpdateRequestSchema,
  FeatureIdSchema,
  type Checkpoints,
  type EffortLevel,
  type FeatureConfig,
  type FeatureConfigSnapshot,
  type FeatureConfigUpdateRequest,
  type ModelCatalogue,
  type PhaseEffort,
  type PhaseModels,
  type ProviderModelRefreshResult,
  type WorkspaceDefaults,
} from '../shared/ipc';
import { serverRequest, type ServerTransport } from './serverClient';
import { toReadinessSnapshot } from './setup';
import { defined, definedNonEmpty } from '../shared/defined';

function toPhaseModels(models: z.output<typeof ServerModelDefaultsSchema>): PhaseModels {
  const entry = (value: string | undefined): string | undefined =>
    value === undefined || value === '' ? undefined : value;
  return {
    ...defined('inquiry', entry(models.inquiry) === undefined ? undefined : models.inquiry),
    ...defined('research', entry(models.research) === undefined ? undefined : models.research),
    ...defined('planning', entry(models.planning) === undefined ? undefined : models.planning),
    ...defined(
      'implementation',
      entry(models.implementation) === undefined ? undefined : models.implementation,
    ),
    ...defined('review', entry(models.review) === undefined ? undefined : models.review),
    ...defined('utilities', entry(models.utilities) === undefined ? undefined : models.utilities),
    ...defined('kbBuild', entry(models.kb_build) === undefined ? undefined : models.kb_build),
    ...defined(
      'automaticReview',
      entry(models.automatic_review) === undefined ? undefined : models.automatic_review,
    ),
  };
}

function toServerModels(models: PhaseModels): Record<string, string> {
  return {
    inquiry: models.inquiry ?? '',
    research: models.research ?? '',
    planning: models.planning ?? '',
    implementation: models.implementation ?? '',
    review: models.review ?? '',
    utilities: models.utilities ?? '',
    kb_build: models.kbBuild ?? '',
    automatic_review: models.automaticReview ?? '',
  };
}

const EFFORT_LEVELS = new Set<EffortLevel>([
  'auto',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
  'ultra',
]);

function effortEntry(value: string | undefined): EffortLevel | undefined {
  return value !== undefined && EFFORT_LEVELS.has(value as EffortLevel)
    ? (value as EffortLevel)
    : undefined;
}

function toPhaseEffort(
  effort: z.output<typeof ServerEffortDefaultsSchema> | undefined,
): PhaseEffort {
  if (effort === undefined) return {};
  const inquiry = effortEntry(effort.inquiry);
  const research = effortEntry(effort.research);
  const planning = effortEntry(effort.planning);
  const implementation = effortEntry(effort.implementation);
  const review = effortEntry(effort.review);
  const utilities = effortEntry(effort.utilities);
  const kbBuild = effortEntry(effort.kb_build);
  return {
    ...defined('inquiry', inquiry),
    ...defined('research', research),
    ...defined('planning', planning),
    ...defined('implementation', implementation),
    ...defined('review', review),
    ...defined('utilities', utilities),
    ...defined('kbBuild', kbBuild),
  };
}

function toServerEffort(effort: PhaseEffort): Record<string, string> {
  return {
    inquiry: effort.inquiry ?? '',
    research: effort.research ?? '',
    planning: effort.planning ?? '',
    implementation: effort.implementation ?? '',
    review: effort.review ?? '',
    utilities: effort.utilities ?? '',
    kb_build: effort.kbBuild ?? '',
  };
}

function toCheckpoints(cp: z.output<typeof ServerCheckpointsSchema>): Checkpoints {
  return {
    inquiryReview: cp.inquiry_review ?? false,
    researchReview: cp.research_review ?? false,
    designReview: cp.design_review ?? false,
    roadmapReview: cp.roadmap_review ?? false,
    phasePlanReview: cp.phase_plan_review ?? false,
    manualPublish: cp.manual_publish ?? false,
    draftPublish: cp.draft_publish ?? false,
  };
}

function toServerCheckpoints(cp: Checkpoints): Record<string, boolean> {
  return {
    inquiry_review: cp.inquiryReview,
    research_review: cp.researchReview,
    design_review: cp.designReview,
    roadmap_review: cp.roadmapReview,
    phase_plan_review: cp.phasePlanReview,
    manual_publish: cp.manualPublish,
    draft_publish: cp.draftPublish,
  };
}

function normalizeInquireness(value: string | undefined): 'none' | 'medium' | 'high' {
  return value === 'none' || value === 'high' ? value : 'medium';
}

function normalizeInputNotifications(value: string | undefined): 'default' | 'enabled' | 'muted' {
  return value === 'enabled' || value === 'muted' ? value : 'default';
}

function normalizeAutomaticReviewMode(
  value: string | undefined,
): 'default' | 'enabled' | 'disabled' | 'dangerously_skip_permissions' {
  return value === 'enabled' || value === 'disabled' || value === 'dangerously_skip_permissions'
    ? value
    : 'default';
}

function toFeatureConfig(cfg: z.output<typeof ServerFeatureConfigSchema>): FeatureConfig {
  return {
    models: toPhaseModels(cfg.models),
    effort: toPhaseEffort(cfg.effort),
    inquireness: normalizeInquireness(cfg.inquireness),
    checkpoints: toCheckpoints(cfg.checkpoints),
    pipeline: cfg.pipeline ?? '',
    inputNotifications: normalizeInputNotifications(cfg.input_notifications),
    automaticReviewMode: normalizeAutomaticReviewMode(cfg.automatic_review_mode),
  };
}

export class ConfigService {
  constructor(private readonly transport: ServerTransport) {}

  async getFeatureConfig(featureId: string): Promise<FeatureConfigSnapshot> {
    const id = FeatureIdSchema.parse(featureId);
    const body = await serverRequest(
      this.transport,
      `/api/v1/features/${encodeURIComponent(id)}/config`,
      undefined,
    );
    const parsed = FeatureConfigResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_FEATURE_CONFIG: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    return {
      featureId: parsed.data.feature_id,
      current: toFeatureConfig(parsed.data.current),
      defaults: toFeatureConfig(parsed.data.defaults),
      manualPublishAvailable: parsed.data.publishability?.manual_publish ?? true,
    };
  }

  async updateFeatureConfig(request: FeatureConfigUpdateRequest): Promise<FeatureConfigSnapshot> {
    const req = FeatureConfigUpdateRequestSchema.parse(request);
    const body = await serverRequest(
      this.transport,
      `/api/v1/features/${encodeURIComponent(req.featureId)}/config`,
      {
        method: 'POST',
        body: {
          models: toServerModels(req.config.models),
          effort: toServerEffort(req.config.effort),
          inquireness: req.config.inquireness,
          checkpoints: toServerCheckpoints(req.config.checkpoints),
          ...definedNonEmpty('pipeline', req.config.pipeline),
          input_notifications: req.config.inputNotifications,
          automatic_review_mode: req.config.automaticReviewMode,
        },
      },
    );
    const parsed = ActionResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_FEATURE_CONFIG_SAVE: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    return this.getFeatureConfig(req.featureId);
  }

  async getWorkspaceDefaults(): Promise<WorkspaceDefaults> {
    const body = await serverRequest(this.transport, '/api/v1/config/runtime', undefined);
    const parsed = RuntimeConfigResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_WORKSPACE_DEFAULTS: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    const defaults = parsed.data.feature_defaults;
    return {
      models: toPhaseModels(defaults.models),
      effort: toPhaseEffort(defaults.effort),
      inquireness: normalizeInquireness(defaults.inquireness),
      checkpoints: toCheckpoints(defaults.checkpoints ?? {}),
      pipeline: defaults.pipeline ?? '',
      muteFeatureInput: parsed.data.notifications?.mute_feature_input ?? false,
      automaticReviewEnabled: defaults.automatic_review_enabled ?? false,
      dangerouslySkipPermissions: defaults.dangerously_skip_permissions ?? false,
    };
  }

  async updateWorkspaceDefaults(defaults: WorkspaceDefaults): Promise<WorkspaceDefaults> {
    const body = await serverRequest(this.transport, '/api/v1/config/runtime', {
      method: 'PATCH',
      body: {
        defaults: {
          models: toServerModels(defaults.models),
          effort: toServerEffort(defaults.effort),
          inquireness: defaults.inquireness,
          checkpoints: toServerCheckpoints(defaults.checkpoints),
          ...definedNonEmpty('pipeline', defaults.pipeline),
          automatic_review_enabled: defaults.automaticReviewEnabled,
          dangerously_skip_permissions: defaults.dangerouslySkipPermissions ?? false,
        },
        notifications: { mute_feature_input: defaults.muteFeatureInput },
      },
    });
    const parsed = ActionResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_WORKSPACE_DEFAULTS_SAVE: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    return this.getWorkspaceDefaults();
  }

  async getModelCatalogue(): Promise<ModelCatalogue> {
    const body = await serverRequest(this.transport, '/api/v1/catalog/models', undefined);
    const parsed = ModelCatalogResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_MODEL_CATALOGUE: invalid server response');
    return toModelCatalogue(parsed.data);
  }

  async refreshProviderModels(provider: string): Promise<ProviderModelRefreshResult> {
    const body = await serverRequest(this.transport, '/api/v1/catalog/models/refresh', {
      method: 'POST',
      body: { provider },
    });
    const parsed = ProviderModelRefreshResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_PROVIDER_MODEL_REFRESH: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    return {
      readiness: toReadinessSnapshot(parsed.data.readiness),
      catalogue: toModelCatalogue(parsed.data.catalog),
    };
  }
}

function toModelCatalogue(data: z.output<typeof ModelCatalogResponseSchema>): ModelCatalogue {
  assertCompatibleApiVersion(data.api_version);
  const providerModels: ModelCatalogue['providerModels'] = {};
  for (const [provider, models] of Object.entries(data.provider_models ?? {})) {
    providerModels[provider] = models.map((m) => ({
      id: m.id,
      ...(m.display_name === undefined || m.display_name === ''
        ? {}
        : { displayName: m.display_name }),
      ...defined(
        'aliases',
        m.aliases === undefined ? undefined : m.aliases.filter((alias) => alias.trim() !== ''),
      ),
      ...definedNonEmpty('category', m.category),
      ...(m.context_window === undefined || m.context_window <= 0
        ? {}
        : { contextWindow: m.context_window }),
      ...defined(
        'effortCapabilities',
        m.effort_capabilities === undefined
          ? undefined
          : m.effort_capabilities.filter((value): value is EffortLevel =>
              EFFORT_LEVELS.has(value as EffortLevel),
            ),
      ),
    }));
  }
  return {
    providerOrder: data.provider_order ?? [],
    providerModels,
    phaseDefaults: toPhaseModels(data.phase_defaults ?? {}),
    phaseProviderModels: data.phase_provider_models ?? {},
    chatDefaultEffort: data.chat_default_effort ?? {},
  };
}
