---
description: Artifact contract for frontend-design HTML and PNG mockup bundles
license: Apache-2.0
provenance: agentic-orchestrator-original
---

# Design Mockups

This skill is a companion contract for `@skills/frontend-design/`. Before creating or revising mockups, locate `frontend-design` in the system prompt's Additional Skills catalog, read its listed `SKILL.md`, and follow it for visual direction, design process, content, accessibility, responsiveness, implementation quality, and self-critique.

Do not introduce a separate visual methodology here. `frontend-design` owns how the interface is designed; this skill only adds the artifact and validation contract required by the Design workflow.

The bundle is design evidence, not production implementation.

## Output Files

Write the bundle only under the shared Design artifact root (`{artifact_dir}`) supplied by the role contract:

| Artifact | Path | Requirement | Purpose |
|----------|------|-------------|---------|
| HTML prototype | `{artifact_dir}/mockups/prototype.html` | required | self-contained rendering with one addressable fragment per state |
| PNG states | `{artifact_dir}/mockups/states/<state-id>.png` | one or more required | real browser-rendered image paired with each prototype state |
| manifest | `{artifact_dir}/mockups/manifest.yaml` | required | exact machine-readable inventory consumed by the orchestrator |

Do not write to the repository worktree, source directories, generic temporary directories, `{attempt_dir}`, or any path outside `{artifact_dir}`. Temporary browser profiles, downloaded assets, and intermediate screenshots must also remain inside `{artifact_dir}/mockups/.work/`; remove `.work/` before completion.

## Bundle Contract

### Self-contained HTML

Every manifest-referenced HTML file must:

- contain all HTML, CSS, and JavaScript needed to render its declared state
- work from `file://` without a build, package install, local server, or network access
- use inline CSS and JavaScript
- embed fonts and raster/vector assets as data URLs when they are necessary
- render deterministically at the viewport required by the design
- include visible keyboard focus and reduced-motion behavior when interaction or motion is represented

Do not use external URLs, CDNs, remote fonts, framework imports, repository assets by relative path, or generated placeholders.

### Real PNG states

Each PNG must be captured from the manifest-referenced HTML prototype in a real browser at the state fragment and viewport declared by the manifest. It must show the complete state after deterministic rendering has settled.

Never satisfy this requirement with:

- an empty, transparent, single-color, or text-only placeholder image
- a renamed SVG or non-PNG payload
- a hand-authored canvas that does not render the HTML state
- a screenshot of an error page, missing asset, loading skeleton left unintentionally, or browser chrome
- one image reused for states whose visible outcomes differ

Inspect every PNG after capture. Confirm it is decodable, has the declared pixel dimensions, contains the intended state, and has no clipping, overlap, missing assets, or accidental transparency.

### Manifest schema

Write valid UTF-8 YAML using this exact shape:

```yaml
schema_version: 1
design_artifact: ../design.md
html: prototype.html
responsive_expectations:
  - "Desktop at >=1024px preserves the two-column composition."
binding_decisions:
  - "Primary action remains visible without scrolling."
illustrative_details:
  - "Example account names and values are non-binding."
states:
  - id: checkout-error-mobile
    title: Checkout validation error
    source: prototype.html#checkout-error-mobile
    png: states/checkout-error-mobile.png
    viewport:
      width: 390
      height: 844
      device_scale_factor: 1
    design_sections:
      - "## User Experience"
    description: Required mobile error state after submit.
```

Contract rules:

- `schema_version` is the integer `1`; unknown fields are rejected.
- `design_artifact` resolves to the design markdown in the shared Design root.
- `html` names the single self-contained prototype shared by every state.
- `responsive_expectations`, `binding_decisions`, and `illustrative_details` make visual authority explicit. Use an empty illustrative list when every represented detail is binding.
- `states` is non-empty. IDs, source fragments, and PNG paths are unique.
- Each `source` is the manifest `html` path plus a non-empty fragment that deterministically selects the state.
- Each `png` is a relative `.png` path from `mockups/manifest.yaml`.
- `viewport` records capture width, height, and device scale factor. Use positive integers matching the actual PNG capture.
- `design_sections` names the design headings this state realizes; `description` states the observable state and trigger.
- Every path resolves to a non-empty regular file and remains inside the shared Design artifact root after symlink resolution.
- Entries collectively cover every state and viewport explicitly required by the design; add no speculative flows.

## Completion Standard

The `frontend-design` result satisfies its own quality standard, and the HTML, PNGs, and `manifest.yaml` agree exactly. Every required state is represented, every PNG is a real inspected browser rendering, and the bundle contains no placeholders, external dependencies, `.work/` directory, or files outside the shared Design artifact root.
