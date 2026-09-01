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
import { z } from 'zod';
import { ReadinessResponseSchema } from '../shared/api/parse';
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

const ServerModelsSchema = z.object({
  inquiry: z.string().optional(),
  research: z.string().optional(),
  planning: z.string().optional(),
  implementation: z.string().optional(),
  review: z.string().optional(),
  utilities: z.string().optional(),
  kb_build: z.string().optional(),
  automatic_review: z.string().optional(),
});

const ServerEffortSchema = z.object({
  inquiry: z.string().optional(),
  research: z.string().optional(),
  planning: z.string().optional(),
  implementation: z.string().optional(),
  review: z.string().optional(),
  utilities: z.string().optional(),
  kb_build: z.string().optional(),
});

const ServerCheckpointsSchema = z.object({
  inquiry_review: z.boolean().optional(),
  research_review: z.boolean().optional(),
  design_review: z.boolean().optional(),
  roadmap_review: z.boolean().optional(),
  phase_plan_review: z.boolean().optional(),
  manual_publish: z.boolean().optional(),
  draft_publish: z.boolean().optional(),
});

const ServerFeatureConfigSchema = z.object({
  models: ServerModelsSchema,
  effort: ServerEffortSchema.optional(),
  inquireness: z.string().optional(),
  checkpoints: ServerCheckpointsSchema,
  pipeline: z.string().optional(),
  input_notifications: z.string().optional(),
  automatic_review_mode: z.string().optional(),
});

const FeatureConfigResponseSchema = z.object({
  api_version: z.string(),
  feature_id: z.string(),
  current: ServerFeatureConfigSchema,
  defaults: ServerFeatureConfigSchema,
  publishability: z.object({ manual_publish: z.boolean() }).partial().optional(),
});

const RuntimeConfigResponseSchema = z.object({
  api_version: z.string(),
  feature_defaults: z.object({
    models: ServerModelsSchema,
    effort: ServerEffortSchema.optional(),
    inquireness: z.string().optional(),
    checkpoints: ServerCheckpointsSchema.optional(),
    pipeline: z.string().optional(),
    automatic_review_enabled: z.boolean().optional(),
  }),
  notifications: z.object({ mute_feature_input: z.boolean() }).optional(),
});

const ServerModelInfoSchema = z.object({
  id: z.string(),
  display_name: z.string().optional(),
  aliases: z.array(z.string()).optional(),
  category: z.string().optional(),
  context_window: z.number().int().optional(),
  effort_capabilities: z.array(z.string()).optional(),
});

const ModelCatalogResponseSchema = z.object({
  api_version: z.string(),
  provider_order: z.array(z.string()).optional(),
  provider_models: z.record(z.string(), z.array(ServerModelInfoSchema)).optional(),
  phase_defaults: ServerModelsSchema.optional(),
  phase_provider_models: z.record(z.string(), z.record(z.string(), z.array(z.string()))).optional(),
});

const ProviderModelRefreshResponseSchema = z.object({
  api_version: z.string(),
  readiness: ReadinessResponseSchema,
  catalog: ModelCatalogResponseSchema,
});

const ActionResponseSchema = z.object({ api_version: z.string() });

function toPhaseModels(models: z.output<typeof ServerModelsSchema>): PhaseModels {
  const entry = (value: string | undefined): string | undefined =>
    value === undefined || value === '' ? undefined : value;
  return {
    ...(entry(models.inquiry) === undefined ? {} : { inquiry: models.inquiry }),
    ...(entry(models.research) === undefined ? {} : { research: models.research }),
    ...(entry(models.planning) === undefined ? {} : { planning: models.planning }),
    ...(entry(models.implementation) === undefined
      ? {}
      : { implementation: models.implementation }),
    ...(entry(models.review) === undefined ? {} : { review: models.review }),
    ...(entry(models.utilities) === undefined ? {} : { utilities: models.utilities }),
    ...(entry(models.kb_build) === undefined ? {} : { kbBuild: models.kb_build }),
    ...(entry(models.automatic_review) === undefined
      ? {}
      : { automaticReview: models.automatic_review }),
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

const EFFORT_LEVELS = new Set<EffortLevel>(['auto', 'low', 'medium', 'high', 'xhigh', 'max']);

function effortEntry(value: string | undefined): EffortLevel | undefined {
  return value !== undefined && EFFORT_LEVELS.has(value as EffortLevel)
    ? (value as EffortLevel)
    : undefined;
}

function toPhaseEffort(effort: z.output<typeof ServerEffortSchema> | undefined): PhaseEffort {
  if (effort === undefined) return {};
  const inquiry = effortEntry(effort.inquiry);
  const research = effortEntry(effort.research);
  const planning = effortEntry(effort.planning);
  const implementation = effortEntry(effort.implementation);
  const review = effortEntry(effort.review);
  const utilities = effortEntry(effort.utilities);
  const kbBuild = effortEntry(effort.kb_build);
  return {
    ...(inquiry === undefined ? {} : { inquiry }),
    ...(research === undefined ? {} : { research }),
    ...(planning === undefined ? {} : { planning }),
    ...(implementation === undefined ? {} : { implementation }),
    ...(review === undefined ? {} : { review }),
    ...(utilities === undefined ? {} : { utilities }),
    ...(kbBuild === undefined ? {} : { kbBuild }),
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
): 'default' | 'enabled' | 'disabled' {
  return value === 'enabled' || value === 'disabled' ? value : 'default';
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
          ...(req.config.pipeline === '' ? {} : { pipeline: req.config.pipeline }),
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
          ...(defaults.pipeline === '' ? {} : { pipeline: defaults.pipeline }),
          automatic_review_enabled: defaults.automaticReviewEnabled,
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
      ...(m.aliases === undefined
        ? {}
        : { aliases: m.aliases.filter((alias) => alias.trim() !== '') }),
      ...(m.category === undefined || m.category === '' ? {} : { category: m.category }),
      ...(m.context_window === undefined || m.context_window <= 0
        ? {}
        : { contextWindow: m.context_window }),
      ...(m.effort_capabilities === undefined
        ? {}
        : {
            effortCapabilities: m.effort_capabilities.filter((value): value is EffortLevel =>
              EFFORT_LEVELS.has(value as EffortLevel),
            ),
          }),
    }));
  }
  return {
    providerOrder: data.provider_order ?? [],
    providerModels,
    phaseDefaults: toPhaseModels(data.phase_defaults ?? {}),
    phaseProviderModels: data.phase_provider_models ?? {},
  };
}
