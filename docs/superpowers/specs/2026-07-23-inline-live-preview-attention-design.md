# Inline Live Preview Attention Design

## Goal

Let an operator answer a feature's pending agent request directly in the feature
tab's embedded live preview. Opening the global Attention inbox or expanding the
live preview must not be required.

## Existing Behavior

The feature cockpit already reuses `AttentionDetail` in the full-screen live
preview. However, it chooses that content only when an Attention notification
creates an `attentionPreviewRequest`, and `CurrentRunInspection` renders the
content only in `LivePreviewOverlay`.

## Design

The feature cockpit will derive the first unresolved, actionable attention item
whose `featureId` matches the open feature. It will pass the corresponding
`AttentionDetail` to `CurrentRunInspection` independently of whether the user
arrived through an Attention notification.

`CurrentRunInspection` will render that shared response content in two places:

- directly beneath the embedded live-preview transcript and metrics; and
- in the existing full-screen live-preview footer.

The embedded surface will use the existing "Your response" label and
`AttentionDetail` controls. No new response form or submission path will be
introduced. If several requests are pending for one feature, resolving the first
will refresh attention state and reveal the next.

The global Attention notification remains a navigation shortcut. When used, it
continues to open the full-screen preview and targets the requested item.

## State and Data Flow

Both embedded and full-screen controls receive the same renderer-owned drafts,
busy state, draft-save callback, and submit callback. Edits in either
presentation are immediately reflected in the other because both are views of
the same state.

After a successful response, the existing submission path refreshes the global
attention snapshot and authoritative feature snapshot. The resolved panel then
disappears, or advances to the next pending request for that feature.

## Error Handling and Accessibility

Submission and draft-save failures continue through the existing announcement
and error-message paths. The embedded panel is a labelled `Agent request`
region, preserving the same accessible names and keyboard-operable form
controls as the full-screen panel.

## Verification

Renderer regression coverage will prove that:

1. a pending feature-linked request appears in the embedded live preview without
   an `attentionPreviewRequest`;
2. the response can be submitted from that embedded panel; and
3. the panel disappears after the refreshed attention snapshot no longer
   contains the request.

Run the focused renderer test, desktop static/build checks relevant to the
package, and the repository Fast suite (`make test-fast`) before handoff.
