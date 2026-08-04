# Desktop Release, Updates, and Diagnostics

Agentico desktop stable releases are assembled from a protected `vX.Y.Z` tag.
Local development packages remain unsigned and unpublished; protected CI tag
runs are the only place Developer ID, notarization, and GPG signing material is
accepted.

## Cutting a Release

Releases are cut locally by the release operator — no CI credentials — from a
clean checkout of the release tag:

```bash
git tag vX.Y.Z && git push origin vX.Y.Z
make release            # GORELEASER_FLAGS='--release-notes notes.md' to add notes
```

`make release` runs three steps that must stay in this order:

1. `npm run package:build --workspace desktop` — builds the universal DMG.
   Because HEAD sits exactly on a clean `vX.Y.Z` tag, package-build stamps the
   tag version into the app (`app.getVersion()`) and into
   `build-identity.json`'s `desktop_version`; any other build keeps the static
   development version from `desktop/package.json`.
2. `goreleaser release` — builds the CLI archives, creates the GitHub release,
   uploads the DMG (`release.extra_files`), folds the DMG's SHA-256 into
   `checksums.txt` (`checksum.extra_files`), signs `checksums.txt` with the
   operator's Ed25519 key (`desktop/scripts/release-sign.mjs`), and pushes the
   `agentico` CLI cask to the tap.
3. `node desktop/scripts/publish-desktop-cask.mjs` — renders
   `Casks/agentico-desktop.rb` from the DMG and pushes it to the tap
   (goreleaser's OSS cask pipe cannot checksum artifacts it did not build).

The in-app updater consumes exactly this shape: it picks the highest stable
SemVer release, selects `Agentico-mac-universal.dmg` (or the arch-matched
AppImage/deb), and verifies the package hash against the
`agentico-ed25519`-signed `checksums.txt` before offering an install.

### Release signing key

The updater trusts one Ed25519 public key embedded as `RELEASE_PUBLIC_KEY` in
`desktop/src/main/updates.ts`. The private half lives only with the release
operator at `~/.config/agentico-release/release-key.pem` (override with
`AGENTICO_RELEASE_SIGNING_KEY` / `AGENTICO_RELEASE_SIGNING_KEY_FILE`) and never
enters the repo or CI. `release-sign.mjs sign` refuses keys whose public half
does not match the embedded trust root. To rotate: move the old key aside, run
`node desktop/scripts/release-sign.mjs keygen`, embed the printed public key in
`updates.ts`, and ship a release — apps older than that release will report
`signature: failed` for newer releases and fall back to manual guidance. The
committed fixture keypair in `test/e2e/helpers/update-fixtures.ts` is trusted
only when `AGENTICO_UPDATE_FIXTURE` routes the feed to a local fixture.

### Interim unsigned distribution (until Developer ID/notarization)

macOS artifacts carry only an ad-hoc signature, so Gatekeeper blocks them when
they carry the quarantine attribute. Both casks therefore strip
`com.apple.quarantine` in a post-install hook; installs via `curl`/`git` never
acquire the attribute in the first place. Once a signing/notarization pipeline
exists: provide the Developer ID + notarization credentials to the packaging
step (see `release-credentials-check.mjs`), and delete the quarantine hooks
from `.goreleaser.yaml` and `desktop/scripts/lib/desktop-cask.mjs`. Nothing
else in the release flow changes.

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

`release:verify` performs local static release checks outside protected tag
mode. With `AGENTICO_RELEASE_STRICT=1` or a tag-triggered GitHub Actions run, it
also requires signing credentials and signed native artifacts.
