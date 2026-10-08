# Feature-lifecycle port removal

Status: deferred

## Context

`ports.FeatureLifecycle` is a 39-method mirror of `feature.Manager` with one
production adapter, and the orchestrator downcasts past it at three sites. The
orchestrator also bypasses its own chokepoint with direct store writes and thin
setters. Group A (Phases 1 to 4) already absorbs the bypass half: child
creation and help-queue routing move behind the orchestrator, and methods with
no callers are deleted.

## Decision

Defer deleting the port and rewriting the 14 mock-based orchestrator test files
against a real temp-dir `Manager`. Re-measure the port, the setters, and the
direct store sites after Group A lands.

## Consequences

The port and its mock stay for now. The next pass starts from the measured
state after Group A rather than from the numbers taken before it.
