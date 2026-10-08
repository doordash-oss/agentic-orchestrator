# Static feature-action operations

Status: rejected

## Context

Five feature actions have their own static REST operations in addition to the
generic action route that addresses a feature action by name. On the surface
they duplicate the generic route.

## Decision

Keep the five static operations; folding them into the generic route is
rejected. They pin typed request schemas that the generated Go client and the
mutation module rely on, so removing them would trade compile-checked request
shapes for untyped bodies.

## Consequences

The spec keeps both the generic action route and the five static operations.
Adding a feature action does not require a static operation; one is added only
when a typed request schema is worth pinning.
