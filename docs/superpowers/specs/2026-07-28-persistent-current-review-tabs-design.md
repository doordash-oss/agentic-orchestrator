# Persistent Current Review Tabs

## Goal

Use the live activity frame as the single detailed status surface during
implementation review and harness verification. Remove the redundant summary
section beneath it, while ensuring the left review rail survives a renderer
refresh without showing reviewers from earlier iterations.

## Scope

This change applies to the current-run inspection surface in the Electron
renderer.

- Remove the lower review-gate summary during implementation review.
- Remove the lower verification summary while the harness executes the
  verification contract.
- Keep the roadmap status, phase activity, phase metrics, run artifacts, and
  bounded logs unchanged.
- Apply the removal to regular and cycle current-run presentations.
- Keep the sealed record presentation unchanged.

## Current Behavior

The left activity rail retains completed review sessions only in React state.
On a renderer refresh, cohort membership starts empty and is rebuilt from
active sessions. If one reviewer is still running, completed reviewers from
the same batch disappear from the rail.

The review-gate state remains durable and is rendered again in a separate
summary below the live activity frame. Harness verification similarly renders
the command list inside the live activity frame and repeats it in a lower
summary.

## Design

### Current review cohort

Pass the feature's current iteration into cohort discovery. When membership is
initialized or reset, include non-chat sessions from the current iteration as
the current cohort, including terminal sessions. This restores completed,
failed, and running reviewers after a renderer refresh.

Continue using the existing retention behavior while the component remains
mounted. A newly started disjoint retry batch replaces the prior terminal
batch. A feature phase change still resets cohort retention.

If current-iteration metadata is unavailable, preserve the existing active
session selection behavior rather than guessing across historical iterations.

### Left rail

The left rail remains the durable review status surface:

- reviewers retain the existing axis order;
- running, completed, and failed states retain their existing marks;
- restored tabs open their real session transcripts;
- selection continues to prefer an active reviewer when the previous
  selection is unavailable.

No synthetic tabs are created from review-gate labels because they would not
have a corresponding transcript.

### Lower content

Do not render `ReviewGateSummary` or `VerificationSummary` in regular or cycle
current-run presentations. Keep the summaries in the sealed record
presentation, where the live rail is an archive rather than the sole current
status surface.

During review, the rail and selected transcript communicate reviewer status
and detail. During harness verification, `VerificationStage` remains inside
the live activity frame and is the only command-level status surface.

Artifacts and bounded logs remain directly below the current-run preview.

### Accessibility

The existing vertical tablist semantics, keyboard navigation, selected state,
and status-bearing accessible labels remain unchanged. Removing duplicated
summaries reduces repeated screen-reader content without removing the
interactive status surface.

## Error Handling

Session discovery continues to degrade to the last successfully loaded cohort.
If iteration metadata is missing, the renderer does not merge terminal
sessions from unrelated batches. Existing live-preview, artifact, and log
errors remain unchanged.

## Verification

Renderer tests will cover:

- initial hydration restoring terminal and running sessions from the current
  iteration;
- excluding reviewer sessions from earlier iterations;
- retaining current in-memory cohort behavior as statuses change;
- absence of the lower review-gate summary;
- absence of the lower verification summary while the verification command
  list remains visible in live activity;
- current keyboard and transcript-selection behavior.

Required repository gates:

- Fast suite: `make test-fast`
- Static analysis: `go vet ./...`
- Build: `go build ./...`

The change affects embedded desktop launch behavior only indirectly and does
not alter packaging or server lifecycle, so the E2E smoke and Go E2E tiers are
not required unless implementation changes expand beyond this renderer scope.
