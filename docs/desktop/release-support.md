# Desktop Release, Updates, and Diagnostics

Agentico desktop stable releases are assembled from a protected `vX.Y.Z` tag.
Local development packages remain unsigned and unpublished; protected CI tag
runs are the only place Developer ID, notarization, and GPG signing material is
accepted.

## Pre-upgrade Migration

Before upgrading an installation whose runtime data lives under
`~/.agentic-workflow/`, rename that directory to `~/.agentic-orchestrator/`.
Headless installations may instead keep a custom location by passing explicit
`--config` and `--state-dir` flags. Current releases do not check the legacy
runtime parent automatically.

## Update Behavior

- The Electron main process owns update checks, release feed access, staged
  metadata, explicit install actions, diagnostics, filesystem access, and
  restart control.
- The renderer receives only a bounded `UpdateState` over validated IPC.
- The app checks the stable GitHub Releases feed after runtime readiness and on
  a six-hour bounded schedule; manual checks share the same state machine.
- macOS and AppImage updates can be prepared for explicit restart. `Install
When Idle` never interrupts workflows or AMA implicitly. `Stop Work and
Install Now` requires a separate confirmation.
- DEB installs do not self-replace. Settings shows verified package-manager
  guidance and the trusted release page instead.
- `agentico update` is non-mutating. It opens desktop Settings > Updates when
  the registered app is available, otherwise it prints install-method guidance.
  `agentico update --check` only reads stable release metadata.

## Diagnostics

Diagnostics are local-only and bounded:

- retained for seven days,
- capped at 25 MiB,
- capped at ten crash metadata records,
- redacted before retention,
- no crash dumps, process memory, prompt bodies, transcript bodies, arbitrary
  paths, upload, export, or support-bundle API.

Settings > Diagnostics shows recent redacted entries, retention limits, crash
metadata, Reveal Folder, and Clear Diagnostics. Clear removes only Agentico's
app-owned diagnostics directory and does not touch runtime workflow state.

## Verification Commands

```bash
npm run audit:release --workspace desktop
npm run test:performance --workspace desktop
npm run release:credentials:check --workspace desktop
npm run release:verify --workspace desktop
```

`release:verify` performs local static release checks outside protected tag
mode. With `AGENTICO_RELEASE_STRICT=1` or a tag-triggered GitHub Actions run, it
also requires signing credentials and signed native artifacts.
