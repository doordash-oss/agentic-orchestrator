/**
 * Main-process adapter for structured configuration: per-feature config
 * (models per phase, inquireness, gates), workspace defaults, and the model
 * catalogue that powers the pickers. Everything talks to the authoritative
 * server through the gateway transport; the renderer never composes REST
 * paths or sees snake_case server shapes.
 */
import { z } from 'zod';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import {
  FeatureConfigUpdateRequestSchema,
  FeatureIdSchema,
  type Checkpoints,
  type FeatureConfig,
  type FeatureConfigSnapshot,
  type FeatureConfigUpdateRequest,
  type ModelCatalogue,
  type PhaseModels,
  type WorkspaceDefaults,
} from '../shared/ipc';
import { serverRequest, type ServerTransport } from './serverClient';

const REMEDIES = {
  remedyByCode: {
    not_found: 'The item no longer exists on the server. Refresh and retry.',
    bad_request: 'Correct the highlighted input, then try again.',
    validation_failed: 'Fix the reported issues, then try again.',
    conflict: 'The configuration changed on the server. Refresh and retry.',
  },
} as const;

const ServerModelsSchema = z.object({
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
  inquireness: z.string().optional(),
  checkpoints: ServerCheckpointsSchema,
  pipeline: z.string().optional(),
  input_notifications: z.string().optional(),
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
    inquireness: z.string().optional(),
    checkpoints: ServerCheckpointsSchema.optional(),
    pipeline: z.string().optional(),
  }),
  notifications: z.object({ mute_feature_input: z.boolean() }).optional(),
});

const ServerModelInfoSchema = z.object({
  id: z.string(),
  display_name: z.string().optional(),
  category: z.string().optional(),
  context_window: z.number().int().optional(),
});

const ModelCatalogResponseSchema = z.object({
  api_version: z.string(),
  provider_order: z.array(z.string()).optional(),
  provider_models: z.record(z.string(), z.array(ServerModelInfoSchema)).optional(),
  phase_defaults: ServerModelsSchema.optional(),
  phase_provider_models: z.record(z.string(), z.record(z.string(), z.array(z.string()))).optional(),
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

function toFeatureConfig(cfg: z.output<typeof ServerFeatureConfigSchema>): FeatureConfig {
  return {
    models: toPhaseModels(cfg.models),
    inquireness: normalizeInquireness(cfg.inquireness),
    checkpoints: toCheckpoints(cfg.checkpoints),
    pipeline: cfg.pipeline ?? '',
    inputNotifications: normalizeInputNotifications(cfg.input_notifications),
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
      REMEDIES,
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
          inquireness: req.config.inquireness,
          checkpoints: toServerCheckpoints(req.config.checkpoints),
          ...(req.config.pipeline === '' ? {} : { pipeline: req.config.pipeline }),
          input_notifications: req.config.inputNotifications,
        },
      },
      REMEDIES,
    );
    const parsed = ActionResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_FEATURE_CONFIG_SAVE: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    return this.getFeatureConfig(req.featureId);
  }

  async getWorkspaceDefaults(): Promise<WorkspaceDefaults> {
    const body = await serverRequest(this.transport, '/api/v1/config/runtime', undefined, REMEDIES);
    const parsed = RuntimeConfigResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_WORKSPACE_DEFAULTS: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    const defaults = parsed.data.feature_defaults;
    return {
      models: toPhaseModels(defaults.models),
      inquireness: normalizeInquireness(defaults.inquireness),
      checkpoints: toCheckpoints(defaults.checkpoints ?? {}),
      pipeline: defaults.pipeline ?? '',
      muteFeatureInput: parsed.data.notifications?.mute_feature_input ?? false,
    };
  }

  async updateWorkspaceDefaults(defaults: WorkspaceDefaults): Promise<WorkspaceDefaults> {
    const body = await serverRequest(
      this.transport,
      '/api/v1/config/runtime',
      {
        method: 'PATCH',
        body: {
          defaults: {
            models: toServerModels(defaults.models),
            inquireness: defaults.inquireness,
            checkpoints: toServerCheckpoints(defaults.checkpoints),
            ...(defaults.pipeline === '' ? {} : { pipeline: defaults.pipeline }),
          },
          notifications: { mute_feature_input: defaults.muteFeatureInput },
        },
      },
      REMEDIES,
    );
    const parsed = ActionResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_WORKSPACE_DEFAULTS_SAVE: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    return this.getWorkspaceDefaults();
  }

  async getModelCatalogue(): Promise<ModelCatalogue> {
    const body = await serverRequest(this.transport, '/api/v1/catalog/models', undefined, REMEDIES);
    const parsed = ModelCatalogResponseSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_MODEL_CATALOGUE: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    const providerModels: ModelCatalogue['providerModels'] = {};
    for (const [provider, models] of Object.entries(parsed.data.provider_models ?? {})) {
      providerModels[provider] = models.map((m) => ({
        id: m.id,
        ...(m.display_name === undefined || m.display_name === ''
          ? {}
          : { displayName: m.display_name }),
        ...(m.category === undefined || m.category === '' ? {} : { category: m.category }),
        ...(m.context_window === undefined || m.context_window <= 0
          ? {}
          : { contextWindow: m.context_window }),
      }));
    }
    return {
      providerOrder: parsed.data.provider_order ?? [],
      providerModels,
      phaseDefaults: toPhaseModels(parsed.data.phase_defaults ?? {}),
      phaseProviderModels: parsed.data.phase_provider_models ?? {},
    };
  }
}
