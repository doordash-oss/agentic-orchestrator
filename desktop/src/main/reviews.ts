import { z } from 'zod';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import {
  ReviewConflictResponseSchema,
  ReviewDecisionResponseSchema,
  ReviewDraftValidationResponseSchema,
  ReviewSessionResponseSchema,
  validateWithSchema,
  type ServerReviewDecision,
  type ServerReviewDraftValidation,
  type ServerReviewSession,
} from '../shared/api/parse';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, mapServerError, type ServerTransport } from './serverClient';

const identifier = z.string().min(1).max(200);
const revision = z.string().min(1).max(200);
const text = z.string().max(2 * 1024 * 1024);

const ReviewOpenRequestSchema = z.strictObject({ featureId: identifier });
const ReviewSaveRequestSchema = z.strictObject({
  featureId: identifier,
  reviewId: identifier,
  baseRevision: revision,
  text,
});
const ReviewValidateRequestSchema = z.strictObject({
  featureId: identifier,
  reviewId: identifier,
  text,
});
const ReviewDecisionRequestSchema = z.strictObject({
  featureId: identifier,
  reviewId: identifier,
  baseRevision: revision,
  decision: z.enum(['proceed', 'iterate']),
});

export type ReviewOpenRequest = z.output<typeof ReviewOpenRequestSchema>;
export type ReviewSaveRequest = z.output<typeof ReviewSaveRequestSchema>;
export type ReviewValidateRequest = z.output<typeof ReviewValidateRequestSchema>;
export type ReviewDecisionRequest = z.output<typeof ReviewDecisionRequestSchema>;
export type ReviewMutationResult<T> =
  | { type: 'saved'; session: T }
  | { type: 'conflict'; expectedRevision: string; currentRevision: string };

const REMEDIES = {
  remedyByCode: {
    not_found: 'This review is no longer active. Refresh the feature to see its current state.',
    conflict: 'This review changed elsewhere. Reconcile your draft with the current server copy.',
  },
} as const;

/** Main-process-only adapter for the revisioned review-session REST contract. */
export class ReviewService {
  constructor(private readonly transport: ServerTransport) {}

  async read(featureId: string): Promise<ServerReviewSession> {
    const input = validateWithSchema({ featureId }, ReviewOpenRequestSchema);
    return this.getSession(`/api/v1/features/${input.featureId}/reviews`);
  }

  async open(featureId: string): Promise<ServerReviewSession> {
    const input = validateWithSchema({ featureId }, ReviewOpenRequestSchema);
    return this.getSession(`/api/v1/features/${input.featureId}/reviews`, {
      method: 'POST',
      body: {},
    });
  }

  async save(request: ReviewSaveRequest): Promise<ReviewMutationResult<ServerReviewSession>> {
    const input = validateWithSchema(request, ReviewSaveRequestSchema);
    const path = `/api/v1/features/${input.featureId}/reviews/${input.reviewId}/draft`;
    const result = await this.mutate(
      path,
      { base_revision: input.baseRevision, text: input.text },
      input.baseRevision,
      'PUT',
    );
    return result.type === 'conflict'
      ? result
      : { type: 'saved', session: validateWithSchema(result.body, ReviewSessionResponseSchema) };
  }

  async validate(request: ReviewValidateRequest): Promise<ServerReviewDraftValidation> {
    const input = validateWithSchema(request, ReviewValidateRequestSchema);
    const body = await serverRequest(
      this.transport,
      `/api/v1/features/${input.featureId}/reviews/${input.reviewId}/validate`,
      { method: 'POST', body: { text: input.text } },
      REMEDIES,
    );
    return validateWithSchema(body, ReviewDraftValidationResponseSchema);
  }

  async decide(
    request: ReviewDecisionRequest,
  ): Promise<ReviewMutationResult<ServerReviewDecision>> {
    const input = validateWithSchema(request, ReviewDecisionRequestSchema);
    const path = `/api/v1/features/${input.featureId}/reviews/${input.reviewId}/decision`;
    const result = await this.mutate(
      path,
      { decision: input.decision, base_revision: input.baseRevision },
      input.baseRevision,
    );
    return result.type === 'conflict'
      ? result
      : { type: 'saved', session: validateWithSchema(result.body, ReviewDecisionResponseSchema) };
  }

  private async getSession(path: string, init?: ApiRequestInit): Promise<ServerReviewSession> {
    const body = await serverRequest(this.transport, path, init, REMEDIES);
    return validateWithSchema(body, ReviewSessionResponseSchema);
  }

  private async mutate(
    path: string,
    body: Record<string, unknown>,
    expectedRevision: string,
    method: 'POST' | 'PUT' = 'POST',
  ): Promise<
    | { type: 'body'; body: unknown }
    | { type: 'conflict'; expectedRevision: string; currentRevision: string }
  > {
    const response = await this.transport.apiRequest(path, {
      method,
      body,
    });
    if (response.status >= 200 && response.status < 300) {
      return { type: 'body', body: response.body };
    }
    if (response.status === 409) {
      assertNoPrototypePollution(response.body);
      assertWithinByteSize(JSON.stringify(response.body) ?? '');
      const conflict = ReviewConflictResponseSchema.safeParse(response.body);
      if (conflict.success) {
        assertCompatibleApiVersion(conflict.data.api_version);
        return {
          type: 'conflict',
          expectedRevision,
          currentRevision: conflict.data.error.target.current_revision,
        };
      }
    }
    throw mapServerError(response, REMEDIES);
  }
}
