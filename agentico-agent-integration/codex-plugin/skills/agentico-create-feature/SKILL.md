---
name: agentico-create-feature
description: Create an Agentico feature from a harness by driving only Agentico CLI JSON commands.
---

# Agentico Create Feature

Use this skill when the user wants this harness to create new Agentico-managed work.

## Contract

The harness owns reasoning, user interaction, option selection, and explaining tradeoffs. Agentico owns runtime discovery, server startup, REST details, retries, event watching, and state classification through CLI JSON.

Do not call REST endpoints, scrape logs, infer state from files, or launch the TUI. Every runtime operation must be an `agentico ... --json` command.

## Flow

1. Ensure the runtime is available:

   ```bash
   agentico server ensure --json
   ```

2. Discover existing features and defaults through CLI JSON:

   ```bash
   agentico feature select --json
   ```

   Use the returned runtime/config/feature data to avoid duplicate work, pick repo defaults, and decide whether the new feature should start immediately.

3. Build the creation request from the user's goal. Use the fields documented in `references/creation-options.md`; omit fields that the user did not choose and Agentico can default.

   ```bash
   agentico feature create --json --input-json '{"name":"Short name","description":"User goal","repos":["repo-name"],"pipeline":"medium"}'
   ```

4. If the response says the feature is created but not active, start it through Agentico:

   ```bash
   agentico feature action <feature-id> start --json
   ```

5. Hand off immediately to manage mode:

   ```bash
   agentico feature manage <feature-id> --json --watch
   ```

## Harness Responsibilities

- Ask only for missing decisions that cannot be inferred from CLI JSON results.
- Convert user choices into a `feature create` JSON request.
- Keep all user-facing explanation in the harness.
- After creation, load `agentico-manage-feature` and keep the same feature-scoped context.

## Agentico Responsibilities

- Discover or start the local runtime.
- Resolve config, workspace roots, repos, provider policy, defaults, and feature state.
- Own REST transport, retries, event watching, and normalized state classification.
- Return structured JSON envelopes for all command results.
