# Provider Custom Answer Contract Design

## Problem

The desktop UI accepts a custom free-text answer for an `AskUserQuestion`, but
Claude rejects the resulting control response. The Claude adapter makes free
text selectable by injecting it as an additional option, yet the injected
option contains only `label`. Claude's `AskUserQuestion` schema also requires
`description`, so the tool result fails validation and the model asks the
question again.

Codex and OpenCode use different wire protocols for custom answers. The fix
must preserve those provider-native behaviors while adding deterministic
contract coverage for all three providers.

## Design

Keep custom-answer translation inside each provider adapter:

- Claude continues to inject unmatched free text as a selectable option. The
  injected option will include the custom answer as its `label` and a stable,
  non-empty `description`, producing schema-valid `updatedInput`. The answer
  map will continue to select that exact label.
- Codex continues to return native `AskUserResult` entries. A contract test
  will prove that a custom answer preserves the provider question ID and the
  user's value verbatim.
- OpenCode continues to cancel a structured option request when no option
  matches, then sends a framed follow-up turn. Its contract test will prove
  that the cancellation occurs and the follow-up contains the original
  question and custom answer.

No renderer, IPC, HTTP API, session-state, or shared provider interface changes
are required.

## Error Handling

The change introduces no new runtime fallback. Claude receives a valid option
instead of rejecting the control response. Existing serialization and write
errors continue to propagate through the provider protocol methods unchanged.

## Testing

Implementation will follow red-green TDD:

1. Strengthen the Claude free-text test to require a non-empty injected option
   description and observe it fail against the current label-only payload.
2. Add or strengthen deterministic Codex and OpenCode custom-answer contract
   tests for their native wire behavior.
3. Add the minimal Claude production change and rerun all three provider test
   packages.
4. Run the repository Fast suite, Go static analysis, and Go build gates.

The tests deliberately avoid live provider calls, credentials, and network
dependencies.

## Scope

This change only covers custom answers submitted to provider-backed
`AskUserQuestion` requests. It does not alter option selection, auto-picking,
question presentation, or general follow-up messaging.
