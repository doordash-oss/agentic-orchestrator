# Local Linux Desktop Release Design

## Context

Agentico's operator-driven release flow currently builds a universal macOS DMG,
then asks GoReleaser to publish that DMG with the CLI archives and a signed
checksum manifest. Electron-builder is already configured to produce AppImage
and DEB packages, and CI builds and verifies Linux x64 and arm64 packages, but
successful CI packages are not retained and the local release never builds or
publishes them.

The maintainer entry point must remain unchanged: invoke the local Claude
`/release-agentico` skill, approve the version and notes, and let the skill call
`make release`. Publishing credentials and the Ed25519 update-signing key remain
on the maintainer's machine; no release credentials move into GitHub Actions.

## Goals

- Publish macOS, Linux x64, and Linux arm64 desktop packages in every release.
- Publish both AppImage and DEB formats for each Linux architecture.
- Include every desktop package in the Ed25519-signed `checksums.txt` consumed
  by the in-app updater.
- Fail before GoReleaser creates remote state when a platform build, package
  identity, architecture check, or artifact inventory check fails.
- Keep `/release-agentico` and its single `make release` command as the complete
  maintainer workflow.
- Preserve the existing local signing and GitHub/Homebrew publishing model.

## Non-goals

- Restoring a tag-triggered GitHub release workflow.
- Adding GitHub-hosted release secrets.
- Making DEB packages self-update in app; DEB remains package-manager-managed.
- Adding additional Linux formats such as RPM, Flatpak, or Snap.
- Changing the CLI archive matrix or the Homebrew publication model.

## Release Architecture

`make release` remains the sole repository release entry point. It performs the
following ordered operations from a clean exact `vX.Y.Z` tag:

1. Preflight Docker and the pinned Linux builder image.
2. Build the universal macOS DMG natively with the existing package script.
3. Build Linux x64 AppImage and DEB packages in a Linux container.
4. Build Linux arm64 AppImage and DEB packages in a second, sequential Linux
   container invocation.
5. Verify the complete desktop artifact inventory and embedded identities.
6. Run GoReleaser, which builds CLI archives, creates one checksum manifest over
   the CLI archives and all desktop packages, signs that manifest, and uploads
   the complete release.
7. Publish the existing macOS desktop Homebrew cask.

Linux builds are sequential because electron-vite, `prepare-server.mjs`, and
electron-builder share `desktop/out`, `desktop/resources`, and `desktop/dist`.
Artifact filenames are architecture-specific, so completed x64 outputs remain
in `desktop/dist` while arm64 outputs are added. The final native-package build
may overwrite intermediate unpacked directories, but it must not overwrite any
distributable artifact.

## Container Execution

The Linux release orchestrator uses the official Node 22 electron-builder image
pinned by immutable digest:

`electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9`

The source checkout and Git common directory are mounted at their existing
absolute paths so both normal checkouts and linked worktrees retain working Git
metadata. A named container volume shadows the host `node_modules`; each
container runs `npm ci` against `package-lock.json`, avoiding host/container
binary contamination. Electron and electron-builder caches use named volumes.

The orchestrator passes `AGENTICO_PACKAGE_ARCH=x64` and `arm64` respectively to
the existing `package-build.mjs`. That script already binds the chosen Electron
architecture to the corresponding `GOARCH` with `CGO_ENABLED=0`, and stamps the
exact tag version into Electron metadata and `build-identity.json`.

The script must reject an unavailable Docker daemon, an image digest mismatch,
a dirty checkout, a non-release tag, unsupported host OS, or a failed container
command. These checks happen before GoReleaser publishes anything.

## Required Artifact Inventory

For release `vX.Y.Z`, the desktop artifact set is exactly:

- `Agentico-mac-universal.dmg`
- `Agentico-x64.AppImage`
- `Agentico-arm64.AppImage`
- `agentico_X.Y.Z_amd64.deb`
- `agentico_X.Y.Z_arm64.deb`

The verifier rejects missing artifacts, duplicate architecture matches, zero-byte
files, unexpected release versions in versioned filenames, and mismatches
between filename architecture and embedded build identity. It verifies:

- `desktop_version` equals `X.Y.Z`;
- `server_version` equals `vX.Y.Z`;
- `server_revision` equals tagged `HEAD`;
- identity `os` is `linux` for Linux packages;
- identity `arch` is `x64` or `arm64` as selected;
- Electron fuse policy remains hardened;
- the bundled server and Electron executable architectures match the package.

AppImage and DEB packages from the same architecture build are checked
independently. Verification must not assume that validating the DEB validates
the AppImage.

## Signed Manifest and Publication

GoReleaser's `checksum.extra_files` and `release.extra_files` list all five
desktop artifacts. The existing Ed25519 signer continues signing GoReleaser's
single `checksums.txt`; no additional signing key or signature format is added.

A post-GoReleaser local manifest check confirms that every required desktop
artifact has exactly one checksum line and that `checksums.txt.sig` verifies
against the public key embedded in the updater. This catches configuration drift
before the Homebrew cask step and gives `/release-agentico` a deterministic
failure to report if the published release is incomplete.

## Maintainer Experience

The maintainer continues to invoke `/release-agentico`. The skill continues its
current version selection, release-note drafting, confirmation, tagging, and
single `make release` invocation. Its preflight gains Docker daemon availability
and disk-space checks; its verification gains the four Linux desktop assets and
their signed-checksum entries.

The repository documentation remains the source of truth. The local
`release-agentico/SKILL.md` is updated to mirror the new prerequisites, expected
runtime, artifact list, and recovery guidance, but it does not add a new manual
step or command.

## Failure and Recovery

- A macOS or Linux build/verification failure aborts before GoReleaser and
  creates no GitHub release. The local tag can be retained while the cause is
  fixed, matching existing recovery behavior.
- A GoReleaser failure uses the existing partial-release cleanup procedure.
- A missing checksum entry or invalid signature after GoReleaser is treated as
  a partial release: remove the GitHub release and remote tag, fix the release
  configuration, and rerun while retaining the local tag.
- Homebrew publication remains last and idempotent; a rejected tap push can be
  retried independently.

## Testing and Verification

- Pure unit tests cover expected artifact names, architecture mapping, tag and
  identity validation, Docker command construction, worktree Git mounts, and
  manifest completeness.
- Release-audit tests assert that GoReleaser checksums and uploads all required
  desktop artifacts.
- Script tests exercise preflight failures without starting containers and use
  a plan/dry-run interface to inspect exact Docker invocations.
- Existing CI continues native x64 and arm64 package verification and packaged
  journeys, providing native-platform coverage for the same package script used
  by the local containers.
- Before handoff, run the repository fast suite, desktop static/unit/security
  gates, release audit/verification, and a local Docker packaging rehearsal for
  both Linux architectures without publishing.
