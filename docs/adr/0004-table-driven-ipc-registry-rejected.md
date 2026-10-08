# Table-driven IPC channel registry

Status: rejected

## Context

The desktop's `ipc.ts` has 646 exports and 125 channels. Adding a channel edits
about seven production places and six test places. A generated, table-driven
registry was considered to reduce that.

## Decision

Do not introduce a table-driven registry. Every place a new channel touches is
already compiler-checked, and the renderer-facing surface (135 keys pinned by
the security fixture) would not shrink. The registry would be a new generic
layer that still needs escape hatches for the four wrapped channels.

## Consequences

IPC channels stay hand-declared. The small cleanups found during the review
(align three wire shapes, one preload subscribe helper, rename two service
methods) are left for a separate desktop pass.
