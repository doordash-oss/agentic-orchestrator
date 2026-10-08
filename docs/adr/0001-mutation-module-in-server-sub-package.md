# Mutation module lives in a server sub-package

Status: accepted

## Context

The REST mutation adapter lived in `package main` as a 1,560-line type with 42
methods, satisfying the server's 32-method `MutationTarget` interface. It was
the only production adapter, so it was reachable only from the command package,
and the e2e journeys carried a hand copy of it. The adapter has to translate
server DTOs into orchestrator calls, so wherever it lives must see both the
server and the orchestrator. Neither of those packages imports the other today.

## Decision

The mutation module lives in `internal/server/mutations`, imports both the
server DTO types and the orchestrator, and exports one constructor returning
`server.MutationTarget` plus its dependency struct.

Two placements were rejected. Inside `internal/server`: the server would have
to import the orchestrator, coupling the HTTP layer to the domain, and all of
the server's in-package tests would build against the orchestrator; the server
would also lose the interface its tests fake. Inside `internal/orchestrator`:
the orchestrator would have to learn REST and DTO shapes, which belong at the
HTTP edge, not in the domain chokepoint.

## Consequences

The sub-package is cycle-free and reachable from `cmd/agentico`, `test/e2e`,
and `test/integration`, so the journeys use the real module instead of a copy.
The server cannot import its own sub-package, so it keeps `MutationTarget`;
this is an existing seam being narrowed, not a new one. The server's in-package
tests keep fakes, which shrink as the interface narrows.
