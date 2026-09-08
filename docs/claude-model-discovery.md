# Claude model discovery

Agentico discovers Claude Code models with a single stream-JSON `initialize`
control request, the same catalog exposed by the Agent SDK's
[`supportedModels()` and `initializationResult()`](https://code.claude.com/docs/en/agent-sdk/typescript).
It sends no user message and performs no inference. The CLI runs with
`--safe-mode` and `--no-session-persistence`, retaining its authentication and
backend configuration while disabling project customizations. The subprocess
is stopped and reaped after the response, including on failure or cancellation.

The whole initialization response must validate before any model is reported or
published. Missing, empty, malformed, rejected, or truncated catalogs fail the
refresh. The server preserves its previous catalog and cache on failure.
Discovery has no per-model requests, retries, inference budget, or probe fallback.

## Selectors and metadata

- Model IDs come from the CLI's advertised selectors, with context suffix casing
  normalized for display. Launches use the exact original selector, including
  concrete version IDs such as `claude-fable-5-1[1m]`.
- Resolved model IDs become aliases. Unambiguous Claude family aliases preserve
  saved selections such as `fable[1M]`. The registry's existing context-annotation
  resolution continues to handle saved names such as `sonnet[1M]` when the CLI
  advertises `sonnet` without numeric context metadata.
- New and custom model selectors are included without a curated allowlist.
  `default` and `opusplan` are omitted because they represent routing policies.
- Display names and supported effort levels come from initialization. Models
  without advertised effort support offer Auto only. Unknown effort levels are
  ignored until Agentico supports them.
- Initialization does not currently provide numeric context limits for every
  model. Explicit selector suffixes supply known limits; otherwise the catalog
  reports zero (unknown). Actual session usage supplies runtime context limits.
  Agentico does not parse prose descriptions or infer limits from model versions.
- Categories remain Agentico's routing hints. Other SDK capabilities, such as
  adaptive thinking and fast mode, are not yet fields in Agentico's model schema.

## Caching and offline operation

Claude caches identify their discovery source as `sdk-initialize`. Caches from
inference-based discovery are invalidated even if the CLI version has not
changed, forcing initialization at the next startup. A valid catalog is still
cached per CLI version; Recheck explicitly refreshes it.

If startup cannot initialize and has no valid cache, the provider supplies the
shared offline routing defaults. These defaults are not discovery candidates
and advertise no unverified effort support. There is no inference fallback.

## Verification

Unit tests cover catalog parsing, atomic rejection, exact selectors, saved
selection aliases, version separation, effort metadata, and cache invalidation.
The E2E Go tier tests real subprocess exchange and cleanup with a fake CLI.

To check the installed authenticated CLI without sending an inference prompt:

```bash
AGENTIC_CLAUDE_CATALOG_LIVE=1 go test ./test/e2e -run '^TestClaudeCatalogLive$' -count=1 -v
```

This performs three initialization exchanges. Verified with Claude Code 2.1.263.
