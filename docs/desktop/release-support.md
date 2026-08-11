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
AGENTICO_RELEASE_NOTES_FILE=/absolute/path/to/notes.md make release
```

A relative notes path is resolved from the operator checkout where `make
release` is invoked, before the detached workspace is created.

Before running it, make sure the local machine has:

- macOS, a running Docker daemon, and at least 12 GiB free on the checkout's
  filesystem for the two Linux container builds and their caches;
- GoReleaser v2.10 or later, an exported `GITHUB_TOKEN` with repository
  release/tag access, and the local update-signing key
  described below.

The audited runner behind `make release` enforces these requirements before any
package is built: it requires an exact clean `vX.Y.Z` tag at HEAD (including
untracked source files), captures the full commit/tag evidence under ignored
per-worktree Git metadata (`git rev-parse --git-path`), and creates a detached
release worktree at that captured commit. The worktree starts with tracked
committed files only, so ignored operator files such as `go.work` or
`skills/.env` cannot enter the build. It checks the token and the local Ed25519 private key against
the updater's embedded public key, verifies Docker, and inspects or pulls only
the two digest-pinned images. It uses
`electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9`
for packaging and
`node:22.22.2-bookworm@sha256:62e4daa6819762bbd3072af77cc282ab72c631c4aed30dd7980192babaf385b3`
to verify the arm64 package. Image pulls and the checksum-pinned Go toolchain
downloads are network dependencies; a daemon, authentication, or download
failure stops before publication.
The preflight loads and validates that local signing key before any build. It
also compiles the inode-bound cleanup helper from the captured committed
workspace, hashes it, and records its path and digest in the byte-digested
preflight evidence before any build or publication subprocess runs.
GoReleaser subsequently consumes the validated operator key to sign
`checksums.txt`.

`make release` is deliberately ordered so every desktop artifact is built and
validated before GoReleaser can create a GitHub release. The optional
`AGENTICO_RELEASE_NOTES_FILE` is deliberately the sole operator-controlled
GoReleaser input: raw GoReleaser flags are not accepted, so a shell variable
cannot bypass the release checks.

1. `npm ci` runs unconditionally inside the detached release worktree after
   preflight, so the captured commit's root lockfile is the
   authoritative native dependency installation. `npm run package:verify --workspace desktop` then builds and verifies the native
   universal macOS DMG.
   Because HEAD sits exactly on a clean `vX.Y.Z` tag, package-build stamps the
   tag version into the app (`app.getVersion()`) and into
   `build-identity.json`'s `desktop_version`; any other build keeps the static
   development version from `desktop/package.json`.
2. `npm run package:linux:release --workspace desktop` runs two sequential
   Linux container builds: x64 first, then arm64. They use Docker/buildx and
   the immutable
   `electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9`
   builder image. The builds remain sequential. The native arm64 verifier uses
   a separate per-run `node_modules` volume and runs `npm ci --ignore-scripts`
   against the captured lockfile before reading the package; it never trusts
   the builder's dependency tree.
3. `npm run release:artifacts:verify --workspace desktop -- packages` verifies
   the complete desktop inventory, architecture-specific package receipts, and
   embedded identities before publishing.
4. The Linux build mounts the detached committed source and shared Git metadata
   read-only, with only ignored build-output directories writable. After each
   Docker packaging or arm64 verification container, and immediately before
   GoReleaser, the release gate rechecks the captured full HEAD, exact tag,
   and tracked/untracked cleanliness. Any source/provenance drift stops the
   release before publishing. All Go builds run with `GOWORK=off`,
   `GOFLAGS=-mod=readonly`, and no inherited overlays. Native builds receive a
   minimal allowlisted environment, an isolated HOME/npm cache, and no
   `NODE_OPTIONS` or ambient npm configuration; Docker orchestration receives
   a separate allowlist containing only Docker routing and evidence state.
   After all five receipts pass, the runner creates canonical UTF-8
   `desktop-release.json`, signs its exact bytes as
   `desktop-release.json.sig`, verifies both against the embedded updater trust
   root and live artifact bytes, then copies those two files, the exact five
   packages, and rewritten canonical receipts
   into a read-only publication snapshot and rehashes it before and after
   GoReleaser. Before any reservation or publication call, it durably records
   `goreleaser-started`; only then does it atomically create the remote
   lightweight tag at the captured commit. If the tag already exists, it may
   continue only when dereferencing lightweight or annotated tag objects yields
   that exact commit. GoReleaser then builds CLI archives and creates the
   GitHub release with `target_commitish: "{{ .Commit }}"`, so the tag is
   pinned to the local HEAD captured when `make release` began. It uploads
   every desktop package and both signed envelope files, folds every published
   desktop-file SHA-256 into
   `checksums.txt`, signs that manifest with the operator's Ed25519 key
   (`desktop/scripts/release-sign.mjs`), and pushes the `agentico` CLI cask to
   the tap. `npm run release:artifacts:verify --workspace desktop -- manifest`
   immediately verifies the signature and requires exactly one checksum entry
   for every desktop package and signed envelope file.
5. The release gate then verifies, through the GitHub API, that the remote tag
   dereferences (including annotated tags) to the captured local HEAD, that
   the release is published and stable rather than a draft, and that all five
   desktop packages, `desktop-release.json`, `desktop-release.json.sig`,
   `checksums.txt`, and `checksums.txt.sig` are attached.
   Every required asset must carry a well-formed GitHub SHA-256 digest matching
   the local receipt-bound publication bytes; the checksum manifest and
   signature digests must likewise match their local files.
   Only then does `node desktop/scripts/publish-desktop-cask.mjs` render
   `Casks/agentico-desktop.rb` from the DMG and pushes it to the tap
   (goreleaser's OSS cask pipe cannot checksum artifacts it did not build).

The resulting desktop assets are exactly:

- `Agentico-mac-universal.dmg`
- `Agentico-x64.AppImage`
- `Agentico-arm64.AppImage`
- `agentico_X.Y.Z_amd64.deb`
- `agentico_X.Y.Z_arm64.deb`
- `desktop-release.json`
- `desktop-release.json.sig`

All seven are uploaded to the GitHub release and have exactly one line in the
signed `checksums.txt`. The updater consumes `desktop-release.json` only after
verifying its detached Ed25519 signature. The native DMG and either AppImage support staged in-app
updates requiring an explicit restart action. DEB installations are
package-manager-managed: the app presents verified package-manager guidance
and the trusted release page, rather than replacing a DEB installation itself.

Before the publication checkpoint, failures remove the detached workspace and
temporary evidence. At `goreleaser-started`, the runner durably retains that
workspace, byte-bound evidence, immutable publication snapshot, and resume
record under Git metadata until the remote-byte and desktop-cask gates succeed.
Recovery depends on where the failure occurred:

- **Before the durable `goreleaser-started` checkpoint.** No remote mutation was
  attempted. Fix the local Docker, credential, package, receipt, inventory, or
  signing condition and rerun the same `make release` while retaining the local
  tag.
- **At `goreleaser-started` without a complete digest-correct release.** The
  process may have crossed a remote mutation boundary even when GitHub now
  appears absent or only the reserved tag exists. Preserve the snapshot and
  evidence, and do not blindly rerun GoReleaser. Inspect GitHub and both tap
  casks, explicitly clean up a confirmed partial publication, then choose a new
  patch tag or perform documented manual recovery. The runner fails closed
  because a local process cannot atomically prove whether a prior remote
  attempt occurred.
- **Transient verifier failure after GoReleaser.** Authentication, DNS, or
  network failure while checking GitHub does _not_ prove that the release is
  partial. Do not delete anything. Restore credentials/connectivity and rerun
  the same `make release` against the unchanged tag. The audited runner detects
  its retained resume record, revalidates the operator tag and commit, detached
  workspace identity, evidence, publication snapshot, receipts, and signed
  manifest, then resumes at remote verification without rebuilding or invoking
  GoReleaser again. Any mismatch fails closed for inspection; do not supply tag
  or commit overrides or change remote state merely to force a retry.
- **Confirmed repository/configuration or remote-state defect.** Only a
  verified wrong commit, draft/prerelease state, missing/incomplete asset,
  digest mismatch, bad signature, or equivalent defect needs destructive
  reconciliation. First inspect and remove the partial GitHub release and the
  reserved remote tag. Also
  inspect the tap's `Casks/agentico.rb`: GoReleaser may have pushed it before
  the failure, so restore or revert that generated cask commit and push the
  reconciliation before retrying. Commit and verify the repository fix, then
  either delete and recreate the local `vX.Y.Z` tag on that fixed commit (only
  after the remote tag is gone) or cut a new patch tag. Never retry an old tag
  that still points at the broken commit.

The final `agentico-desktop` cask publication is different: if the signed
manifest and remote publication verification passed and only
`node desktop/scripts/publish-desktop-cask.mjs` fails, do not remove the
already-complete release or CLI cask. Inspect the tap failure and rerun the
same `make release` from the unchanged tag. The retained, validated resume
record skips builds and GoReleaser, rechecks the current remote tag, release
state, and every published digest immediately before the cask, then retries the
desktop cask. Successful cleanup removes the detached workspace, its evidence,
and the resume record. Git worktree administration and the compiled helper /
isolated build-home directory are left as safe stale Git metadata instead of
running a repository-global prune. A maintainer may inspect them with
`git worktree list --porcelain` and remove only confirmed stale entries and the
matching `.git/agentico-release-cleanup-helpers/<run-id>` directory afterward.

The release isolation model assumes the committed lockfiles, pinned container
images, package scripts, and release operator account are trusted. It prevents
ambient configuration and accidental path substitution from becoming release
inputs; it does not claim to defend against an arbitrary concurrent process
running as the same OS user, which can replace files and credentials available
to that account.

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
npm run release:verify --workspace desktop
```

`release:verify` performs local static release checks. The production release
gate is the single `make release` command above: it builds all five desktop
packages, generates and verifies the signed desktop envelope before
publication, and verifies the signed checksum manifest afterward. The release credential that matters to the
updater is the local Ed25519 key described in [Release signing key](#release-signing-key).
`npm run release:credentials:check --workspace desktop` is a backward-compatible
alias for the same complete local preflight; maintainers normally invoke only
the release skill, which invokes the single `make release` command.
