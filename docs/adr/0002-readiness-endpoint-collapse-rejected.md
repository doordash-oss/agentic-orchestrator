# Readiness endpoint collapse

Status: rejected

## Context

The REST API exposes four readiness operations. They look redundant, but they
form a deliberate two-by-two grid, and the desktop already carries 404
fallback branches so it can talk to older servers that lack some of them.

## Decision

Keep the four readiness operations. Collapsing them fails the deletion test:
the complexity does not disappear, it moves into the desktop as a third
fallback branch alongside the existing ones.

## Consequences

The readiness grid stays as it is in `api/openapi.yaml`. A future proposal to
merge these operations must first show how the desktop's fallback branches
shrink rather than multiply.
