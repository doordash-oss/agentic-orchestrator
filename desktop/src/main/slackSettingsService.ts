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

import { z } from 'zod';
import { CanonicalErrorSchema, validateWithSchema } from '../shared/api/parse';
import { assertCompatibleApiVersion } from '../shared/apiVersion';
import { redactedCanonicalError } from '../shared/errors';
import {
  SlackSettingsDraftSchema,
  SlackSettingsSnapshotSchema,
  SlackRecipientResolveRequestSchema,
  SlackRecipientSchema,
  SlackTestMessageRequestSchema,
  SlackTestMessageResultSchema,
  SlackValidationRequestSchema,
  SlackValidationResultSchema,
  type SlackIdentity,
  type SlackRecipient,
  type SlackRecipientResolveRequest,
  type SlackSettingsDraft,
  type SlackSettingsSnapshot,
  type SlackTestMessageRequest,
  type SlackTestMessageResult,
  type SlackValidationRequest,
  type SlackValidationResult,
} from '../shared/ipc';
import { fencedServerRequest, type ServerIdentitySource } from './serverFence';
import type { ServerTransport } from './serverClient';

const WireSlackIdentitySchema = z.strictObject({
  team_id: z.string(),
  team_name: z.string(),
  user_id: z.string(),
  display_name: z.string(),
  bot_id: z.string().optional(),
});

const WireSlackStatusSchema = z.strictObject({
  state: z.enum(['not_configured', 'connected', 'warning']),
  last_error: CanonicalErrorSchema.nullable().optional(),
  last_checked_at: z.string().datetime().nullable().optional(),
});

const WireSlackRecipientSchema = z.strictObject({
  typed_text: z.string(),
  kind: z.enum(['user', 'channel']),
  id: z.string(),
  display_name: z.string(),
});

const WireResponseMetaSchema = z.strictObject({
  as_of_seq: z.number().int().nonnegative(),
  generated_at: z.string().datetime(),
  revision: z.string(),
});

const WireSlackSettingsSchema = z.strictObject({
  enabled: z.boolean(),
  token_set: z.boolean(),
  token_hint: z.string(),
  token_type: z.enum(['bot', 'user']).nullable().optional(),
  identity: WireSlackIdentitySchema.nullable().optional(),
  granted_scopes: z.array(z.string()),
  missing_scopes: z.array(z.string()),
  default_recipients: z.array(WireSlackRecipientSchema),
  categories: z.strictObject({
    progress: z.boolean(),
    needs_input: z.boolean(),
    problems: z.boolean(),
  }),
  status: WireSlackStatusSchema,
  manifest: z.string(),
});

const RuntimeConfigResponseSchema = z.object({
  api_version: z.string(),
  slack: WireSlackSettingsSchema.optional(),
});

const ActionResponseSchema = z.object({ api_version: z.string() });

const SlackValidateResponseSchema = z.object({
  api_version: z.string(),
  token_type: z.enum(['bot', 'user']),
  identity: WireSlackIdentitySchema,
  granted_scopes: z.array(z.string()),
  missing_scopes: z.array(z.string()),
  suggested_recipient: WireSlackRecipientSchema.nullable(),
});

const SlackResolveResponseSchema = z.strictObject({
  api_version: z.string(),
  meta: WireResponseMetaSchema.optional(),
  recipient: WireSlackRecipientSchema,
});

const SlackTestMessageResponseSchema = z.strictObject({
  api_version: z.string(),
  meta: WireResponseMetaSchema.optional(),
  results: z.array(
    z.strictObject({
      recipient: WireSlackRecipientSchema,
      delivered: z.boolean(),
      error: CanonicalErrorSchema.nullable().optional(),
    }),
  ),
});

export interface SlackSettingsServiceDeps {
  transport: ServerTransport;
  identity?: ServerIdentitySource;
}

function mapIdentity(identity: z.output<typeof WireSlackIdentitySchema>): SlackIdentity {
  return {
    teamId: identity.team_id,
    teamName: identity.team_name,
    userId: identity.user_id,
    displayName: identity.display_name,
    botId: identity.bot_id ?? null,
  };
}

function mapRecipient(recipient: z.output<typeof WireSlackRecipientSchema>): SlackRecipient {
  return {
    typedText: recipient.typed_text,
    kind: recipient.kind,
    id: recipient.id,
    displayName: recipient.display_name,
  };
}

function wireRecipient(recipient: SlackRecipient): z.output<typeof WireSlackRecipientSchema> {
  return {
    typed_text: recipient.typedText,
    kind: recipient.kind,
    id: recipient.id,
    display_name: recipient.displayName,
  };
}

export class SlackSettingsService {
  constructor(private readonly deps: SlackSettingsServiceDeps) {}

  async get(): Promise<SlackSettingsSnapshot> {
    const body = await this.request('/api/v1/config/runtime');
    const response = validateWithSchema(body, RuntimeConfigResponseSchema);
    assertCompatibleApiVersion(response.api_version);
    if (response.slack === undefined) {
      return { supported: false };
    }
    const slack = response.slack;
    return validateWithSchema(
      {
        supported: true,
        enabled: slack.enabled,
        tokenSet: slack.token_set,
        tokenHint: slack.token_hint,
        tokenType: slack.token_type ?? null,
        identity:
          slack.identity === undefined || slack.identity === null
            ? null
            : mapIdentity(slack.identity),
        grantedScopes: slack.granted_scopes,
        missingScopes: slack.missing_scopes,
        defaultRecipients: slack.default_recipients.map(mapRecipient),
        categories: {
          progress: slack.categories.progress,
          needsInput: slack.categories.needs_input,
          problems: slack.categories.problems,
        },
        status: {
          state: slack.status.state,
          lastError:
            slack.status.last_error === undefined || slack.status.last_error === null
              ? null
              : redactedCanonicalError(slack.status.last_error),
          lastCheckedAt: slack.status.last_checked_at ?? null,
        },
        manifest: slack.manifest,
      },
      SlackSettingsSnapshotSchema,
    );
  }

  async update(draft: SlackSettingsDraft): Promise<SlackSettingsSnapshot> {
    const input = validateWithSchema(draft, SlackSettingsDraftSchema);
    const body = await this.request('/api/v1/config/runtime', {
      method: 'PATCH',
      body: {
        slack: {
          ...(input.enabled === undefined ? {} : { enabled: input.enabled }),
          ...(input.token === undefined ? {} : { token: input.token }),
          ...(input.clearToken === undefined ? {} : { clear_token: input.clearToken }),
          ...(input.defaultRecipients === undefined
            ? {}
            : { default_recipients: input.defaultRecipients.map(wireRecipient) }),
          ...(input.categories === undefined
            ? {}
            : {
                categories: {
                  ...(input.categories.progress === undefined
                    ? {}
                    : { progress: input.categories.progress }),
                  ...(input.categories.needsInput === undefined
                    ? {}
                    : { needs_input: input.categories.needsInput }),
                  ...(input.categories.problems === undefined
                    ? {}
                    : { problems: input.categories.problems }),
                },
              }),
        },
      },
    });
    const response = validateWithSchema(body, ActionResponseSchema);
    assertCompatibleApiVersion(response.api_version);
    return this.get();
  }

  async validate(request: SlackValidationRequest): Promise<SlackValidationResult> {
    const input = validateWithSchema(request, SlackValidationRequestSchema);
    const body = await this.request('/api/v1/integrations/slack/validate', {
      method: 'POST',
      body: input.token === undefined ? {} : { token: input.token },
    });
    const response = validateWithSchema(body, SlackValidateResponseSchema);
    assertCompatibleApiVersion(response.api_version);
    return validateWithSchema(
      {
        tokenType: response.token_type,
        identity: mapIdentity(response.identity),
        grantedScopes: response.granted_scopes,
        missingScopes: response.missing_scopes,
        suggestedRecipient:
          response.suggested_recipient === null ? null : mapRecipient(response.suggested_recipient),
      },
      SlackValidationResultSchema,
    );
  }

  async resolveRecipient(request: SlackRecipientResolveRequest): Promise<SlackRecipient> {
    const input = validateWithSchema(request, SlackRecipientResolveRequestSchema);
    const body = await this.request('/api/v1/integrations/slack/recipients/resolve', {
      method: 'POST',
      body: {
        input: input.input,
        ...(input.token === undefined ? {} : { token: input.token }),
      },
    });
    const response = validateWithSchema(body, SlackResolveResponseSchema);
    assertCompatibleApiVersion(response.api_version);
    return validateWithSchema(mapRecipient(response.recipient), SlackRecipientSchema);
  }

  async sendTestMessage(request: SlackTestMessageRequest): Promise<SlackTestMessageResult> {
    const input = validateWithSchema(request, SlackTestMessageRequestSchema);
    const body = await this.request('/api/v1/integrations/slack/test-message', {
      method: 'POST',
      body: { recipients: input.recipients.map(wireRecipient) },
    });
    const response = validateWithSchema(body, SlackTestMessageResponseSchema);
    assertCompatibleApiVersion(response.api_version);
    return validateWithSchema(
      {
        results: response.results.map((result) => ({
          recipient: mapRecipient(result.recipient),
          delivered: result.delivered,
          error:
            result.error === undefined || result.error === null
              ? null
              : redactedCanonicalError(result.error),
        })),
      },
      SlackTestMessageResultSchema,
    );
  }

  private request(
    path: string,
    init?: Parameters<typeof fencedServerRequest>[3],
  ): Promise<unknown> {
    return fencedServerRequest(this.deps.transport, this.deps.identity, path, init);
  }
}
