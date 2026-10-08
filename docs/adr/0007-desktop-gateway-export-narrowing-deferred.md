# Desktop gateway export narrowing

Status: deferred

## Context

`desktop/src/main/gateway/` has 103 exports across 16 modules, but only 16
cross into production code outside the gateway; 23 are imported nowhere.
Transport types are imported from the gateway instead of the transport seam,
and SSE framing lives in the gateway though others share it.

## Decision

Defer the narrowing. It is a surface narrowing with no behavior consolidation,
and the Go phases of this pass already fill the review budget.

## Consequences

The gateway keeps its current exports. A later desktop pass can unexport the
unused identifiers, re-home transport types next to `ServerTransport`, and move
SSE framing to a shared module.
