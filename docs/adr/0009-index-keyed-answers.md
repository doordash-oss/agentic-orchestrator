# Index-keyed answers

Status: accepted

## Context

Ask-user answers travelled keyed by question text. Because display layers
truncate long text and clients fall back to the header when the text is blank,
the server repaired keys in four places: the mutation module matched truncated
keys by prefix and force-assigned a lone answer, a shared label matcher ignored
a "(Recommended)" suffix and both ellipsis forms, the Claude adapter re-matched
labels, and the session re-derived presented order from the keys. The
need-user-input draft receiver accepted a third convention of its own: the
stored index, a `qN` form or the prompt text. Both it and the read model
substituted an ordinal for a zero index, and each used a different ordinal.

## Decision

The server emits a one-based question index with every ask-user and gate
question. Clients echo it back as the answer key, as a decimal string, and
nothing else is accepted. Answers are resolved once, by `Bundle.Resolve` in
`internal/llm/askuser`. Resolution is strict: every key must name a question,
and every question must have a non-blank answer. An answer selects options only
when it equals a label verbatim. For multi-select, every comma-space part must
equal a label. Any other answer is free text. The gate reader rejects a stored
index that is not positive or not unique. Receiver-side fuzzy matching was
deleted, not relocated. Schema series 4 carries the change.

Rejected alternatives:

- Text keys with fuzzy repair at one seam. This keeps the contract ambiguous
  wherever display truncation, duplicate question text or a blank question
  meets the key. One repair seam still has to guess, and clients cannot tell
  when it guessed wrong.
- Zero-based keys. Gate files have stored one-based indexes since the initial
  commit, and the supervisor and desktop already sent them. A zero-based ask-user
  key would give the two surfaces different conventions. A zero index would
  also be indistinguishable from a missing one in the gate's YAML.

## Consequences

A client that sends text keys gets a 400 naming the offending key, and nothing
reaches the provider. The desktop, the supervisor skill and the e2e journeys
moved together with the bump to series 4. The on-disk gate format, the
supervisor transcript's text-keyed answer map and the desktop's visible
behavior are unchanged. An answer that differs from a label only by a
"(Recommended)" suffix or a truncation ellipsis is now delivered as free text
rather than as that option.
