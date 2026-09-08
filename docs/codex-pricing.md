# Codex usage and pricing

Agentico uses the app-server connection for usage accounting. It does not poll
Codex's files or launch a second CLI for pricing.

## Live estimates and reconciliation

`thread/tokenUsage/updated` supplies cumulative token counts. Agentico prices
only new input, cached input, cache-write input, and output tokens. Reasoning
output is already part of output and is not charged again. `last.inputTokens`
selects short- versus long-context rates for each update. Duplicate and
regressing counters do not charge tokens again, and child-thread counters do
not replace root-thread counters.

After a root turn completes (including failed/interrupted turns), and after
resuming a thread, Agentico sends `account/usage/read` with `threadId`. In
Codex CLI 0.153.2 this non-experimental request can return:

- `threadUsage.estimatedUsageUsdMicros`: optional estimated USD cost.
- `threadUsage.estimatedUsageCreditsMicros`: estimated credits, kept separately.

An available USD estimate replaces the cumulative local estimate, including
when the provider returns zero. New inference then adds only new token costs.
Replies that cross new turn/inference activity, superseded replies, malformed
results, and mismatched thread IDs cannot replace newer usage. The endpoint
has no billing watermark, so Agentico conservatively discards such replies.

Lookup errors never fail an agent turn. Method-not-found disables subsequent
lookups for that connection; missing billing data leaves the local estimate.
Final accounting waits at most two seconds for an outstanding lookup and then
uses the available snapshot. Native tool-less review sessions retain their
isolated protocol and use token estimates only.

The terminal result remains immutable: a billing update must not be mistaken
for another agent completion. Cumulative usage, final accounting, and live
session/run cost responses use the reconciled value. Session ledger records
and session-ended events preserve `cost_source` and `cost_credits_micros`;
credits are never converted to dollars. These are estimates, not invoices.

The initial cumulative token snapshot on resume is historical and seeds the
baseline without charging those tokens again. If thread billing is unavailable,
Agentico can estimate new usage but cannot reconstruct historical cost across
model, tier, or rate changes from aggregate token counts alone.

## Rate fallback and Astra

The standard API fallback table is dated in `internal/llm/codex/rates.go`.
It includes GPT-6 Astra and the current GPT-5.6 rates. Unknown models receive
`cost_source: unavailable` instead of another model's rate. Known service tiers
and model-rerouting notifications affect live estimates; provider estimates
remain preferable for account-specific billing. The internal-only raw-response
schema is not used.

Astra is classified as capable and has offline Codex context choices of 272K
and 872K, taken from the installed 0.153.2 harness catalog. Live discovery
supersedes these values and respects model visibility/access. Context suffixes
stay in Agentico's selection IDs and are stripped before sending the model to
Codex. Effort discovery uses the harness's advertised levels, including `ultra`
when available; other models do not inherit Astra's effort capabilities.

Sources checked September 4, 2026:

- [OpenAI API pricing](https://developers.openai.com/api/docs/pricing)
- [GPT-6 Astra](https://developers.openai.com/api/docs/models/gpt-6-astra)
- [Codex app-server](https://learn.chatgpt.com/docs/app-server)
- [Codex plan and credit pricing](https://learn.chatgpt.com/docs/pricing)
- Local `codex app-server generate-json-schema --out <directory>` and
  `codex debug models --bundled`, using `codex-cli 0.153.2`.
