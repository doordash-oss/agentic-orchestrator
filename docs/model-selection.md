# Model selection and reasoning effort

Agentico separates technical compatibility from model recommendations. OpenCode
model names are routing identifiers, not quality or price classifications.

## Recommendations

Set `model_recommendations` in the server configuration to define an ordered
fallback chain for each role. Use exact `agentico-provider:backend-model` IDs or
catalog aliases. For example, this illustrative policy prefers two gateway
models in different orders:

```yaml
model_recommendations:
  planning:
    - opencode:portkey/my-planning-model
    - opencode:portkey/my-implementation-model
  implementation:
    - opencode:portkey/my-implementation-model
    - opencode:portkey/my-planning-model
```

Replace these example IDs with models discovered on the machine running Agentico.
Available roles are `inquiry`, `research`, `planning`, `implementation`, `review`,
`chat`, and `kb_build`. Unknown roles and malformed selectors fail configuration
validation. Unavailable model IDs are allowed: they are skipped until discovery
makes them available. Recommendations are loaded at server startup.

This policy controls catalog defaults and ordering of eligible models. Explicit
`defaults.models` selections remain authoritative. Startup recovery also uses
role recommendations when a configured model cannot be resolved. Changing the
recommendations does not replace valid selections already saved in configuration.

Use evaluated model combinations here: for example, prefer stronger models for
requirements, planning, and review, and a cheaper implementation model that passes
your coding evaluations. Updating this policy requires no Agentico code changes.
Discovery alone cannot establish coding quality or an optimal quality/cost mix.

## Compatibility and fallback ordering

All discovered, technically compatible models remain available for each phase;
there is no per-provider three-model limit. Explicit lack of text output excludes
a model. Explicit lack of tool calling excludes it from tool-using phases; chat
can still use it. Missing metadata stays unknown and does not hide a model.

After explicit recommendations, automatic ordering prefers reported technical
support, then lower positive advertised input-plus-output cost (a reference
workload of one million tokens each), then larger context, then stable provider
and model IDs. Missing and zero prices are treated as unknown, not proof of free
inference. This deterministic fallback is not a model-quality ranking. Configure
recommendations when quality matters. Native toolless automatic review retains
its separate provider compatibility and preference policy.

## Reasoning effort

For OpenCode, Agentico reads each model's effective `variants` metadata from
`opencode models --verbose`. A provider called `portkey`, `fireworks`, or a custom
name is handled in the same way as `openai`.

Recognized effort levels are `low`, `medium`, `high`, `xhigh`, `max`, and `ultra`.
A custom variant name can also expose a level through its `reasoningEffort`
option. Opaque custom names are not assigned an invented effort rank. Disabled,
empty, and semantically identical option maps do not add selectable levels.
Models without usable variant metadata expose Auto only.

A selected level forwards that variant's exact option map, including nested
thinking budgets. Distinct `high` and `max` options remain distinct. Auto uses
the pipeline's requested level when supported, otherwise the first supported
level at or above it, or the highest supported level. For example, a model with
only `high` and `max` receives `high` for a pipeline request of `medium`. The
resolved level is also recorded in session telemetry. Without known capabilities,
Agentico adds no effort options to OpenCode's existing configuration.

See [OpenCode models and variants](https://opencode.ai/docs/models/) for the
underlying configuration contract. Catalog metadata reports what OpenCode is
configured to send; it does not prove that the upstream gateway honors every
option. Validate new routes with a real request before recommending them.

## Catalog freshness and recovery

OpenCode discovery runs on every server startup because gateway configuration
and variants can change without changing the CLI version. Discovery first tries
refreshing upstream model metadata, then falls back to the supported local
verbose or model-list commands. Agentico does not modify Flux's configuration or
install additional gateway routes as part of discovery.

The catalog cache preserves capabilities, advertised prices, and exact effort
options. Its schema version invalidates older caches missing that metadata.
Cache files are private to the user. A failed refresh can reuse a compatible
cached catalog with a startup warning. Without a usable cache, Agentico does not
invent OpenCode models or capabilities. A restart is required to discover changes
made while the server is running.
