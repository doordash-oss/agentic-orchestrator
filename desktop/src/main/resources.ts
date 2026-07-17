/**
 * Main-process adapter for the server resource catalogue API.
 * Proxies catalogue, read, validate, and write operations through the
 * ServerTransport, mapping server snake_case to renderer camelCase.
 */
import { z } from 'zod';
import type { ServerTransport, ServerErrorMapping } from './serverClient';
import { serverRequest } from './serverClient';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import {
  type ResourceCatalogue,
  type ResourceRead,
  type ResourceValidateRequest,
  type ResourceValidateResult,
  type ResourceWriteRequest,
  type ResourceWriteResult,
  type ResourceEntry,
  type ResourceFinding,
  type ResourceEffect,
} from '../shared/ipc';

const identifier = z.string().min(1).max(256);

const ResourceEntryServerSchema = z.strictObject({
  id: z.string().min(1).max(256),
  kind: z.enum(['feature_config', 'runtime_config', 'skill', 'guideline']),
  label: z.string().min(1).max(500),
  content_type: z.enum(['yaml', 'markdown', 'text']),
  revision: z.string().min(1).max(128),
  effect: z.enum(['immediate', 'next_dispatch', 'next_session', 'restart_required']).optional(),
  validatable: z.boolean(),
  hierarchy: z.array(z.string().max(200)).max(50).optional(),
  feature_id: z.string().max(200).optional(),
});

const ResourceCatalogueServerSchema = z.object({
  api_version: z.string(),
  resources: z.array(ResourceEntryServerSchema).max(5000),
  truncated: z.boolean().optional(),
});

const ResourceReadServerSchema = z.object({
  api_version: z.string(),
  id: z.string().min(1).max(256),
  kind: z.enum(['feature_config', 'runtime_config', 'skill', 'guideline']),
  label: z.string().min(1).max(500),
  content_type: z.enum(['yaml', 'markdown', 'text']),
  revision: z.string().min(1).max(128),
  text: z.string().max(2 * 1024 * 1024),
  effect: z.enum(['immediate', 'next_dispatch', 'next_session', 'restart_required']).optional(),
  validatable: z.boolean(),
  hierarchy: z.array(z.string().max(200)).max(50).optional(),
  feature_id: z.string().max(200).optional(),
});

const ResourceValidateServerSchema = z.object({
  api_version: z.string(),
  id: z.string().min(1).max(256),
  valid: z.boolean(),
  revision: z.string().max(128),
  findings: z
    .array(
      z.strictObject({
        code: z.string().min(1),
        message: z.string().min(1),
        field: z.string().optional(),
      }),
    )
    .max(100),
});

const ResourceWriteServerSchema = z.object({
  api_version: z.string(),
  type: z.enum(['saved', 'conflict']),
  id: z.string().min(1).max(256),
  revision: z.string().max(128).optional(),
  effect: z.enum(['immediate', 'next_dispatch', 'next_session', 'restart_required']).optional(),
  expected_revision: z.string().max(128).optional(),
  current_revision: z.string().max(128).optional(),
  current_text: z
    .string()
    .max(2 * 1024 * 1024)
    .optional(),
});

const REMEDIES: ServerErrorMapping = {
  remedyByCode: {
    not_found: 'This resource is no longer available. Refresh the catalogue.',
    conflict: 'This resource changed elsewhere. Reconcile your edits with the current server copy.',
    bad_request: 'The content could not be processed. Check validation findings and try again.',
    validation_failed: 'Check the validation findings below and try again.',
  },
};

function toResourceEntry(raw: z.output<typeof ResourceEntryServerSchema>): ResourceEntry {
  return {
    id: raw.id,
    kind: raw.kind,
    label: raw.label,
    contentType: raw.content_type,
    revision: raw.revision,
    effect: raw.effect,
    validatable: raw.validatable,
    hierarchy: raw.hierarchy,
    featureId: raw.feature_id,
  };
}

export class ResourceService {
  constructor(private readonly transport: ServerTransport) {}

  async catalogue(kind?: string): Promise<ResourceCatalogue> {
    const path = kind ? `/api/v1/resources?kind=${encodeURIComponent(kind)}` : '/api/v1/resources';
    const body = await serverRequest(this.transport, path, undefined, REMEDIES);
    const parsed = ResourceCatalogueServerSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_RESOURCE_CATALOGUE: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    return {
      resources: parsed.data.resources.map(toResourceEntry),
      truncated: parsed.data.truncated ?? false,
    };
  }

  async read(resourceId: string): Promise<ResourceRead> {
    const id = identifier.parse(resourceId);
    const body = await serverRequest(
      this.transport,
      `/api/v1/resources/${encodeURIComponent(id)}`,
      undefined,
      REMEDIES,
    );
    const parsed = ResourceReadServerSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_RESOURCE_READ: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    const r = parsed.data;
    return {
      id: r.id,
      kind: r.kind,
      label: r.label,
      contentType: r.content_type,
      revision: r.revision,
      text: r.text,
      effect: r.effect,
      validatable: r.validatable,
      hierarchy: r.hierarchy,
      featureId: r.feature_id,
    };
  }

  async validate(request: ResourceValidateRequest): Promise<ResourceValidateResult> {
    const id = identifier.parse(request.resourceId);
    const body = await serverRequest(
      this.transport,
      `/api/v1/resources/${encodeURIComponent(id)}/validate`,
      { method: 'POST', body: { text: request.text } },
      REMEDIES,
    );
    const parsed = ResourceValidateServerSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_RESOURCE_VALIDATE: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    return {
      id: parsed.data.id,
      valid: parsed.data.valid,
      revision: parsed.data.revision,
      findings: parsed.data.findings as ResourceFinding[],
    };
  }

  async write(request: ResourceWriteRequest): Promise<ResourceWriteResult> {
    const id = identifier.parse(request.resourceId);
    const body = await serverRequest(
      this.transport,
      `/api/v1/resources/${encodeURIComponent(id)}`,
      { method: 'PUT', body: { base_revision: request.baseRevision, text: request.text } },
      REMEDIES,
    );
    const parsed = ResourceWriteServerSchema.safeParse(body);
    if (!parsed.success) throw new Error('E_RESOURCE_WRITE: invalid server response');
    assertCompatibleApiVersion(parsed.data.api_version);
    const r = parsed.data;
    if (r.type === 'saved') {
      return {
        type: 'saved',
        id: r.id,
        revision: r.revision ?? '',
        effect: r.effect as ResourceEffect | undefined,
      };
    }
    return {
      type: 'conflict',
      id: r.id,
      expectedRevision: r.expected_revision ?? '',
      currentRevision: r.current_revision ?? '',
      currentText: r.current_text ?? '',
    };
  }
}
