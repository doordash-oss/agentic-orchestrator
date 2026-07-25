# Max-Iteration Retry Budget Design

## Problem

The Electron cockpit exposes `Retry` for failed features and describes `Restart`
as extending a maximum-iteration budget. Both actions currently submit an empty
request body. The server's retry handler also calls `RestartPhase` with zero
deltas. A feature that failed with `max_iterations` therefore restarts without
the promised additional budget and can immediately fail at the unchanged cap.

The removed TUI established the intended increments: 10 general phase
iterations and 2 plan-specific iterations.

## Behavior

- Retrying a feature whose failure type is `max_iterations` adds 10 to the
  feature's general iteration budget before restarting the failed phase.
- When that feature is in the plan phase, the same retry also adds 2 to its
  plan-specific iteration budget.
- Retrying setup failures retains the existing setup-only behavior.
- Retrying failures other than `max_iterations` restarts without changing
  iteration budgets.
- Confirming `Restart` in Electron for a `max_iterations` failure sends the
  same `10` and `2` deltas through the existing restart request contract.
- Restarting other feature states sends no iteration deltas.

## Design

The server retry handler remains the authoritative behavior for `Retry`. It
loads the current feature, distinguishes setup failure from a
`max_iterations` phase failure, and passes the established deltas to
`RestartPhase` only for the latter. This repairs every client of the retry
endpoint and avoids trusting renderer state for the server-side decision.

The Electron action request schema gains a typed restart body containing
`max_iterations_delta` and `max_plan_iterations_delta`. The cockpit supplies
that body only when the authoritative snapshot reports the
`max_iterations` failure type. The main-process feature service continues to
forward validated action bodies without interpreting them.

The Retry button continues to call the retry endpoint. It does not masquerade
as Restart, and it does not duplicate server failure classification.

## Error Handling

Existing structured mutation errors and authoritative refresh behavior remain
unchanged. If the feature changes state between the snapshot and action, the
server remains authoritative. Non-positive deltas retain their existing
no-op behavior in `ExtendFailedPhaseBudget`.

## Testing

- Extend the server mutation-target retry test to start from a failed
  `max_iterations` feature and assert that Retry persists the additional
  general budget and dispatches the failed phase.
- Add a plan-phase case asserting the additional plan budget.
- Add a non-maximum failure case asserting that Retry does not change either
  budget.
- Add Electron feature-service and cockpit tests asserting that a confirmed
  maximum-iteration Restart sends the typed `10`/`2` body, while ordinary
  restarts do not.
- Run the Fast suite, `go vet ./...`, and `go build ./...`. Because this changes
  session lifecycle/restart behavior, also run Isolated integration and E2E Go.
