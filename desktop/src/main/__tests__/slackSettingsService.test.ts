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

import { describe, expect, it, vi } from 'vitest';
import type { HttpResult } from '../gateway/runtimeGateway';
import { SlackSettingsService } from '../slackSettingsService';

const projection = {
  enabled: false,
  token_set: true,
  token_hint: '1234',
  token_type: 'bot',
  identity: {
    team_id: 'T123',
    team_name: 'Acme',
    user_id: 'U123',
    display_name: 'Agentico',
    bot_id: 'B123',
  },
  granted_scopes: ['chat:write', 'users:read'],
  missing_scopes: [],
  default_recipients: [
    {
      typed_text: '@ada',
      kind: 'user',
      id: 'U12345678',
      display_name: 'Ada Lovelace',
    },
  ],
  categories: { progress: true, needs_input: true, problems: true },
  status: {
    state: 'connected',
    last_error: null,
    last_checked_at: '2026-09-19T10:00:00Z',
  },
  manifest: '{"display_information":{"name":"Agentico"}}',
};

function response(body: unknown, status = 200): HttpResult {
  return { status, body };
}

function transport(...responses: HttpResult[]) {
  return {
    apiRequest: vi.fn(async () => {
      const next = responses.shift();
      if (next === undefined) throw new Error('unexpected request');
      return next;
    }),
  };
}

describe('SlackSettingsService', () => {
  it('maps the runtime Slack projection without exposing token material', async () => {
    const server = transport(response({ api_version: 'v1', slack: projection }));
    const service = new SlackSettingsService({ transport: server });

    const result = await service.get();

    expect(result).toStrictEqual({
      supported: true,
      enabled: false,
      tokenSet: true,
      tokenHint: '1234',
      tokenType: 'bot',
      identity: {
        teamId: 'T123',
        teamName: 'Acme',
        userId: 'U123',
        displayName: 'Agentico',
        botId: 'B123',
      },
      grantedScopes: ['chat:write', 'users:read'],
      missingScopes: [],
      defaultRecipients: [
        {
          typedText: '@ada',
          kind: 'user',
          id: 'U12345678',
          displayName: 'Ada Lovelace',
        },
      ],
      categories: { progress: true, needsInput: true, problems: true },
      status: {
        state: 'connected',
        lastError: null,
        lastCheckedAt: '2026-09-19T10:00:00Z',
      },
      manifest: '{"display_information":{"name":"Agentico"}}',
    });
    expect(JSON.stringify(result)).not.toContain('xox');
  });

  it('accepts credential_error and redacts the canonical diagnostics', async () => {
    const server = transport(
      response({
        api_version: 'v1',
        slack: {
          ...projection,
          status: {
            state: 'credential_error',
            last_error: {
              code: 'slack_token_rejected',
              class: 'needs_action',
              title: 'Slack rejected the saved token',
              summary: 'Slack rejected the saved token with invalid_auth.',
              diagnostics: 'Authorization: Bearer xoxb-secret-token',
            },
            last_checked_at: '2026-09-22T10:00:00Z',
          },
        },
      }),
    );
    const service = new SlackSettingsService({ transport: server });

    const result = await service.get();

    expect(result).toMatchObject({
      supported: true,
      status: {
        state: 'credential_error',
        lastError: {
          code: 'slack_token_rejected',
          diagnostics: 'Authorization: [redacted]',
        },
        lastCheckedAt: '2026-09-22T10:00:00Z',
      },
    });
    expect(JSON.stringify(result)).not.toContain('xoxb-secret-token');
  });

  it('returns an explicit unsupported marker when the server omits Slack', async () => {
    const server = transport(response({ api_version: 'v1' }));
    const service = new SlackSettingsService({ transport: server });

    await expect(service.get()).resolves.toStrictEqual({ supported: false });
  });

  it('patches only Slack and omits a token when the draft does not carry one', async () => {
    const server = transport(
      response({ api_version: 'v1' }),
      response({ api_version: 'v1', slack: projection }),
    );
    const service = new SlackSettingsService({ transport: server });

    const result = await service.update({ enabled: false, clearToken: true });

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/config/runtime', {
      method: 'PATCH',
      body: { slack: { enabled: false, clear_token: true } },
    });
    expect(result.supported).toBe(true);
    expect(JSON.stringify(result)).not.toContain('token-should-never-return');
  });

  it('forwards a write-only token once and never returns it', async () => {
    const token = 'xoxb-111-222-secret';
    const server = transport(
      response({ api_version: 'v1' }),
      response({ api_version: 'v1', slack: projection }),
    );
    const service = new SlackSettingsService({ transport: server });

    const result = await service.update({ enabled: true, token });

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/config/runtime', {
      method: 'PATCH',
      body: { slack: { enabled: true, token } },
    });
    expect(JSON.stringify(result)).not.toContain(token);
  });

  it('forwards default recipients only when the draft carries them', async () => {
    const server = transport(
      response({ api_version: 'v1' }),
      response({ api_version: 'v1', slack: projection }),
    );
    const service = new SlackSettingsService({ transport: server });
    const recipients = [
      {
        typedText: '@ada',
        kind: 'user' as const,
        id: 'U12345678',
        displayName: 'Ada Lovelace',
      },
      {
        typedText: '#eng',
        kind: 'channel' as const,
        id: 'C12345678',
        displayName: '#eng',
      },
    ];

    await service.update({ defaultRecipients: recipients });

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/config/runtime', {
      method: 'PATCH',
      body: {
        slack: {
          default_recipients: [
            {
              typed_text: '@ada',
              kind: 'user',
              id: 'U12345678',
              display_name: 'Ada Lovelace',
            },
            {
              typed_text: '#eng',
              kind: 'channel',
              id: 'C12345678',
              display_name: '#eng',
            },
          ],
        },
      },
    });
  });

  it('maps the category defaults into the snapshot', async () => {
    const server = transport(
      response({
        api_version: 'v1',
        slack: {
          ...projection,
          categories: { progress: false, needs_input: true, problems: false },
        },
      }),
    );
    const service = new SlackSettingsService({ transport: server });

    const result = await service.get();

    expect(result).toMatchObject({
      supported: true,
      categories: { progress: false, needsInput: true, problems: false },
    });
  });

  it('forwards only the changed category fields', async () => {
    const server = transport(
      response({ api_version: 'v1' }),
      response({ api_version: 'v1', slack: projection }),
    );
    const service = new SlackSettingsService({ transport: server });

    await service.update({ categories: { progress: false } });

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/config/runtime', {
      method: 'PATCH',
      body: { slack: { categories: { progress: false } } },
    });
  });

  it('sends no categories key when the draft does not carry them', async () => {
    const server = transport(
      response({ api_version: 'v1' }),
      response({ api_version: 'v1', slack: projection }),
    );
    const service = new SlackSettingsService({ transport: server });

    await service.update({ enabled: true });

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/config/runtime', {
      method: 'PATCH',
      body: { slack: { enabled: true } },
    });
  });

  it('validates with and without a draft token', async () => {
    const token = 'xoxp-111-222-secret';
    const validated = {
      api_version: 'v1',
      token_type: 'user',
      identity: {
        team_id: 'T123',
        team_name: 'Acme',
        user_id: 'U234',
        display_name: 'Ada',
      },
      granted_scopes: ['chat:write'],
      missing_scopes: [],
      suggested_recipient: {
        typed_text: '@ada',
        kind: 'user',
        id: 'U23456789',
        display_name: 'Ada',
      },
    };
    const server = transport(response(validated), response(validated));
    const service = new SlackSettingsService({ transport: server });

    const draftResult = await service.validate({ token });
    const storedResult = await service.validate({});

    expect(server.apiRequest).toHaveBeenNthCalledWith(1, '/api/v1/integrations/slack/validate', {
      method: 'POST',
      body: { token },
    });
    expect(server.apiRequest).toHaveBeenNthCalledWith(2, '/api/v1/integrations/slack/validate', {
      method: 'POST',
      body: {},
    });
    expect(draftResult).toStrictEqual(storedResult);
    expect(draftResult.suggestedRecipient).toStrictEqual({
      typedText: '@ada',
      kind: 'user',
      id: 'U23456789',
      displayName: 'Ada',
    });
    expect(JSON.stringify(draftResult)).not.toContain(token);
  });

  it('resolves one recipient with an optional draft token and redacts the result', async () => {
    const token = 'xoxp-111-222-secret';
    const server = transport(
      response({
        api_version: 'v1',
        recipient: {
          typed_text: 'ada@example.com',
          kind: 'user',
          id: 'U23456789',
          display_name: 'Ada Lovelace',
        },
      }),
    );
    const service = new SlackSettingsService({ transport: server });

    const result = await service.resolveRecipient({ input: 'ada@example.com', token });

    expect(server.apiRequest).toHaveBeenCalledWith(
      '/api/v1/integrations/slack/recipients/resolve',
      {
        method: 'POST',
        body: { input: 'ada@example.com', token },
      },
    );
    expect(result).toStrictEqual({
      typedText: 'ada@example.com',
      kind: 'user',
      id: 'U23456789',
      displayName: 'Ada Lovelace',
    });
    expect(JSON.stringify(result)).not.toContain(token);
  });

  it('sends a test message and maps delivered and failed recipient results', async () => {
    const recipients = [
      {
        typedText: '@ada',
        kind: 'user' as const,
        id: 'U12345678',
        displayName: 'Ada Lovelace',
      },
      {
        typedText: '#private-ops',
        kind: 'channel' as const,
        id: 'C12345678',
        displayName: '#private-ops',
      },
    ];
    const server = transport(
      response({
        api_version: 'v1',
        meta: {
          as_of_seq: 0,
          generated_at: '0001-01-01T00:00:00Z',
          revision: '',
        },
        results: [
          {
            recipient: {
              typed_text: '@ada',
              kind: 'user',
              id: 'U12345678',
              display_name: 'Ada Lovelace',
            },
            delivered: true,
          },
          {
            recipient: {
              typed_text: '#private-ops',
              kind: 'channel',
              id: 'C12345678',
              display_name: '#private-ops',
            },
            delivered: false,
            error: {
              code: 'slack_not_in_channel',
              class: 'warning',
              title: 'Agentico is not in this channel',
              summary: 'Agentico could not send to #private-ops.',
              remediation: { hint: 'Invite the Agentico app to #private-ops in Slack.' },
            },
          },
        ],
      }),
    );
    const service = new SlackSettingsService({ transport: server });

    const result = await service.sendTestMessage({ recipients });

    expect(server.apiRequest).toHaveBeenCalledWith('/api/v1/integrations/slack/test-message', {
      method: 'POST',
      body: {
        recipients: [
          {
            typed_text: '@ada',
            kind: 'user',
            id: 'U12345678',
            display_name: 'Ada Lovelace',
          },
          {
            typed_text: '#private-ops',
            kind: 'channel',
            id: 'C12345678',
            display_name: '#private-ops',
          },
        ],
      },
    });
    expect(result.results[0]).toMatchObject({ delivered: true });
    expect(result.results[1]).toMatchObject({
      delivered: false,
      error: { code: 'slack_not_in_channel' },
    });
  });

  it('discards a response when the connected server identity changes', async () => {
    let generation = 1;
    const server = {
      apiRequest: vi.fn(async () => {
        generation = 2;
        return response({ api_version: 'v1', slack: projection });
      }),
    };
    const service = new SlackSettingsService({
      transport: server,
      identity: () => ({ serverKey: 'server-a', generation }),
    });

    await expect(service.get()).rejects.toMatchObject({
      canonical: { code: 'E_SERVER_SWITCHED' },
    });
  });

  it.each([
    {
      name: 'recipient resolution',
      run: (service: SlackSettingsService) =>
        service.resolveRecipient({ input: 'ada@example.com' }),
      body: {
        api_version: 'v1',
        recipient: {
          typed_text: 'ada@example.com',
          kind: 'user',
          id: 'U23456789',
          display_name: 'Ada Lovelace',
        },
      },
    },
    {
      name: 'test-message delivery',
      run: (service: SlackSettingsService) =>
        service.sendTestMessage({
          recipients: [
            {
              typedText: '@ada',
              kind: 'user',
              id: 'U12345678',
              displayName: 'Ada Lovelace',
            },
          ],
        }),
      body: {
        api_version: 'v1',
        results: [
          {
            recipient: {
              typed_text: '@ada',
              kind: 'user',
              id: 'U12345678',
              display_name: 'Ada Lovelace',
            },
            delivered: true,
          },
        ],
      },
    },
  ])('discards $name when the connected server identity changes', async ({ run, body }) => {
    let generation = 1;
    const server = {
      apiRequest: vi.fn(async () => {
        generation = 2;
        return response(body);
      }),
    };
    const service = new SlackSettingsService({
      transport: server,
      identity: () => ({ serverKey: 'server-a', generation }),
    });

    await expect(run(service)).rejects.toMatchObject({
      canonical: { code: 'E_SERVER_SWITCHED' },
    });
  });

  it('classifies malformed server responses as schema mismatches', async () => {
    const server = transport(
      response({ api_version: 'v1', slack: { ...projection, missing_scopes: null } }),
      response({ api_version: 'v1', token_type: 'bot' }),
    );
    const service = new SlackSettingsService({ transport: server });

    await expect(service.get()).rejects.toMatchObject({
      canonical: { code: 'E_SCHEMA_MISMATCH' },
    });
    await expect(service.validate({})).rejects.toMatchObject({
      canonical: { code: 'E_SCHEMA_MISMATCH' },
    });
  });
});
