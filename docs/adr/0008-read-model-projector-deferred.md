# Read-model projector

Status: deferred

## Context

The server's read model computes children, errors, and warnings separately for
feature summaries and feature detail, duplicating about 45 to 50 mapping lines
because the generated types flatten `allOf`. One internal projector could
build the shared core once.

## Decision

Defer the projector. It is speculative: exported identifiers would go from 829
to 828, so it brings no exported-surface reduction, and the wire format stays
unchanged either way.

## Consequences

Summary and detail projections stay as they are. The one concrete deletion
found in the same review, `PUT /api/v1/config/runtime`, is handled in Group A
rather than waiting on this record.
