# Shared Codex and OpenCode JSON-RPC framing

Status: rejected

## Context

The Codex and OpenCode provider adapters both speak JSON-RPC, and their
protocol files have overlapping names. A shared framing module was considered.

## Decision

Keep the framing separate. The measured overlap is about 110 and 130 lines, 4
to 5 percent of each protocol file. A shared module would add about 150 lines
plus tests, would have to reconcile integer versus raw ids and opposite
malformed-line policies, and would still have only two adapters because Claude
is not JSON-RPC.

## Consequences

Each provider owns its own framing and its own malformed-line policy. The
question can be reopened if a third JSON-RPC provider appears or the overlap
grows substantially.
