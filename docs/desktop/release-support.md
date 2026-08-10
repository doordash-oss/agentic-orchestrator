# Desktop Release, Updates, and Diagnostics

Agentico desktop stable releases are assembled locally by the release operator
from an exact `vX.Y.Z` tag. Development packages remain unpublished. Release
credentials, the GitHub token, and the update-signing key stay on the release
operator's machine; the release flow does not depend on CI credentials.

## Cutting a Release

Releases are cut locally by the release operator from a clean exact release
tag. The maintainer workflow is intentionally one command after tagging:

```bash
git tag vX.Y.Z
make release GORELEASER_FLAGS='--release-notes notes.md'
```

Before running it, make sure the local machine has:

- a running Docker daemon with `buildx` available;
- at least 12 GiB of free disk space for the two Linux container builds and
  their caches;
- GoReleaser v2.10 or later, a GitHub token, and the local update-signing key
  described below.

`make release` is deliberately ordered so every desktop artifact is built and
validated before GoReleaser can create a GitHub release:

1. `npm run package:verify --workspace desktop` builds and verifies the native
   universal macOS DMG.
   Because HEAD sits exactly on a clean `vX.Y.Z` tag, package-build stamps the
   tag version into the app (`app.getVersion()`) and into
   `build-identity.json`'s `desktop_version`; any other build keeps the static
   development version from `desktop/package.json`.
2. `npm run package:linux:release --workspace desktop` runs two sequential
   Linux container builds: x64 first, then arm64. They use Docker/buildx and
   the immutable
   `electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9`
   builder image. The builds must remain sequential because they share desktop
   build output and caches.
3. `npm run release:artifacts:verify --workspace desktop -- packages` verifies
   the complete desktop inventory, architecture-specific package receipts, and
   embedded identities before publishing.
4. `goreleaser release` builds CLI archives, creates the GitHub release,
   uploads every desktop package, folds every desktop SHA-256 into
   `checksums.txt`, signs that manifest with the operator's Ed25519 key
   (`desktop/scripts/release-sign.mjs`), and pushes the `agentico` CLI cask to
   the tap. `npm run release:artifacts:verify --workspace desktop -- manifest`
   immediately verifies the signature and requires exactly one checksum entry
   for every desktop package.
5. `node desktop/scripts/publish-desktop-cask.mjs` renders
   `Casks/agentico-desktop.rb` from the DMG and pushes it to the tap
   (goreleaser's OSS cask pipe cannot checksum artifacts it did not build).

The resulting desktop assets are exactly:

- `Agentico-mac-universal.dmg`
- `Agentico-x64.AppImage`
- `Agentico-arm64.AppImage`
- `agentico_X.Y.Z_amd64.deb`
- `agentico_X.Y.Z_arm64.deb`

All five are uploaded to the GitHub release and have exactly one line in the
signed `checksums.txt`. The updater uses the manifest only after verifying its
Ed25519 signature. The native DMG and either AppImage support staged in-app
updates requiring an explicit restart action. DEB installations are
package-manager-managed: the app presents verified package-manager guidance
and the trusted release page, rather than replacing a DEB installation itself.

If a macOS or Linux build, package receipt, identity, or inventory check fails,
`make release` stops before GoReleaser and creates no GitHub release. If
GoReleaser has already created a partial release, or the post-publication
manifest check fails, treat it as partial publication: remove the GitHub
release and remote tag, fix the release configuration or signing material, and
rerun while retaining the local tag. Homebrew publication is last; a rejected
tap push can be retried independently.

### Release signing key

The updater trusts one Ed25519 public key embedded as `RELEASE_PUBLIC_KEY` in
`desktop/src/main/updates.ts`. The private half lives only with the release
operator at `~/.config/agentico-release/release-key.pem` (override with
`AGENTICO_RELEASE_SIGNING_KEY` / `AGENTICO_RELEASE_SIGNING_KEY_FILE`) and never
enters the repository. `release-sign.mjs sign` refuses keys whose public half
does not match the embedded trust root. To rotate: move the old key aside, run
`node desktop/scripts/release-sign.mjs keygen`, embed the printed public key in
`updates.ts`, and ship a release — apps older than that release will report
`signature: failed` for newer releases and fall back to manual guidance. The
committed fixture keypair in `test/e2e/helpers/update-fixtures.ts` is trusted
only when `AGENTICO_UPDATE_FIXTURE` routes the feed to a local fixture.

### Interim unsigned macOS distribution (until Developer ID/notarization)

macOS artifacts currently carry only an ad-hoc signature, so Gatekeeper blocks them when
they carry the quarantine attribute. Both casks therefore strip
`com.apple.quarantine` in a post-install hook; installs via `curl`/`git` never
acquire the attribute in the first place. Once a signing/notarization pipeline
is available, configure the Developer ID and notarization credentials in the
release operator's environment, then delete the quarantine hooks from
`.goreleaser.yaml` and `desktop/scripts/lib/desktop-cask.mjs`. Nothing else in
the release flow changes.

### One-time tap migration (formula → cask)

The first release after the goreleaser `brews` → `homebrew_casks` migration
needs manual cleanup in `doordash-oss/homebrew-agentic-orchestrator`:

1. delete `Formula/agentico.rb` (goreleaser now writes `Casks/agentico.rb`),
2. add a top-level `tap_migrations.json` so existing installs move over on
   their next `brew upgrade`:

   ```json
   {
     "agentico": "doordash-oss/agentic-orchestrator"
   }
   ```

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

`release:verify` performs local static release checks. The production release
gate is the single `make release` command above: it builds all five desktop
artifacts, verifies their identities before publication, and verifies the
signed checksum manifest afterward.
