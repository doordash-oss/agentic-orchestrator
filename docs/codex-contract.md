# Codex phase contract

Agentico uses the same app-server contract for every Codex model. Model choice
changes inference, not the question or completion transport.

## Instructions and controls

Phase instructions are sent as thread-level `developerInstructions` on both
`thread/start` and `thread/resume`. Orchestrated sessions disable Codex's default
collaboration-mode instruction block through the thread configuration. Agentico
does not use a collaboration-mode override to carry its phase contract.
Fresh structured threads record a compatibility marker in Agentico provider
state. Resume requires that marker because Codex persists the dynamic tool
definitions when the thread is created. An older thread without the tools
cannot silently resume as a structured phase.

The Codex adapter exposes two dynamic tools:

- `ask_user` asks one blocking question. A choice question has exactly three
  options, each with a confidence between zero and one, and exactly one
  recommendation whose confidence is strictly greater than the others. An
  inherently unconstrained exact-value question uses `kind: "free_form"` and
  empty options. The adapter validates the payload and maps it to the existing
  `AskUserQuestion` UI and answer-persistence path. Invalid payloads receive
  corrections as tool results.
- `complete_phase` requests `success` or `retry`; the role contract determines
  whether `retry` is valid. The session routes root requests to the phase
  coordinator, which checks pending questions, running tasks, and artifacts.
  Rejections return corrections through the same tool call. Acceptance uses
  the existing harness completion coordinator and receipt writer. A file,
  ordinary assistant message, or outcome-shaped prose cannot independently
  authorize completion for a structured Codex phase.

Completion requests are buffered during startup. A second request is rejected
while the first is being validated, so parallel calls cannot race two commits.
The coordinator sends the completion result before stopping the provider.
Ordinary turn endings without an accepted completion request do not instruct the
model to manufacture a success artifact.

Interactive chat and isolated tool-free reviewers retain their respective
interfaces; they do not receive these orchestrated phase completion tools.
Other providers retain their existing completion transport.

## Verification

The deterministic adapter and session tests check request schemas, provenance,
question routing, rejected requests, and forged prose outcomes. The normal Fast
suite skips model inference.

Run the live compatibility fixture with an authenticated Codex CLI on `PATH`:

```bash
AGENTIC_CODEX_LIVE=1 go test ./test/e2e -run '^TestCodexContractLive$' -count=1 -timeout=20m -v
```

The default model list is `gpt-5.4,gpt-6-astra`. Override it when validating
other available models:

```bash
AGENTIC_CODEX_LIVE=1 AGENTIC_CODEX_MODELS='gpt-5.4,gpt-6-astra' \
  go test ./test/e2e -run '^TestCodexContractLive$' -count=1 -timeout=20m -v
```

For each model, the test starts a new thread and then resumes that thread with
the next model in the list. A one-model list tests same-model resume. Each stage
has a three-minute inference deadline and a separate 45-second handshake
limit. The test logs the installed CLI version. It creates temporary work and
Agentico state directories, keeps existing authentication, and applies
process-local overrides that disable web search, MCP servers, and plugins. It
does not edit user settings. Codex may persist these test threads through its
normal thread storage.

The test uses the real session manager, adapter, and phase completion waiter
with a small fixture validator. It verifies that a marker supplied only in
`developerInstructions` and a token read from a local fixture reach a structured
question, that a nonrecommended answer returns to the model, and that an absent
artifact rejects completion before the corrected artifact is accepted. Fresh
and resumed stages use different markers and artifacts. Production role
contracts and receipt contents are covered by the deterministic suites.

## Compatibility limits

Dynamic tools are an experimental Codex app-server API. The adapter contains
its wire representation; the session and phase layers depend on provider-neutral
requests. The provider's `MinVersion()` in
[`internal/llm/codex/provider.go`](../internal/llm/codex/provider.go) is the
startup version gate. A version gate alone does not establish behavioral
compatibility: run the live fixture for supported models when changing the CLI,
model catalog, instruction delivery, or tool protocol, and record the exact
version and models that passed.

The live fixture verifies transport and instruction execution on a bounded
procedure. Neither it nor structural artifact validation proves that a model
has explored every relevant ambiguity in a real inquiry or design interview.
