# Local Linux Desktop Release Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the existing local `/release-agentico` → `make release` workflow build, verify, sign, and publish macOS plus Linux x64/arm64 AppImage and DEB desktop artifacts.

**Architecture:** A pure release-artifact contract defines names, architecture mappings, Docker commands, and manifest validation. A local Docker orchestrator runs the existing package-and-verify pipeline sequentially for Linux x64 and arm64, captures per-platform verification receipts, and a prepublish gate validates the full desktop inventory before GoReleaser creates remote state. GoReleaser then signs and uploads every artifact, followed by a local signed-manifest check and the existing Homebrew publication.

**Implemented hardening addendum:** The final pipeline runs behind one audited
`release-run.mjs` wrapper in a detached worktree at captured committed HEAD.
Native npm packaging, Docker assembly, and GoReleaser use that workspace;
release Go builds disable ambient workspaces/overlays and require readonly
module resolution. Verified artifacts are copied into a canonical read-only
publication snapshot and rehashed before/after GoReleaser. The runner atomically
creates and verifies the signed desktop release envelope, fsyncs every snapshot
file and the snapshot directory, records `tag-reservation-started`, and reserves
the remote tag immediately before publication. It records
`goreleaser-started` only after reservation and immediately before GoReleaser.
The final GitHub
gate compares every required asset digest—including `checksums.txt` and its
signature—to local receipt-bound bytes before cask publication.

**Tech Stack:** Node.js 22 ESM scripts, Vitest, Docker/buildx, electron-builder 26, GoReleaser v2, Make, Ed25519 release signing.

## Global Constraints

- The maintainer entry point remains the local Claude `/release-agentico` skill with one `make release` invocation after confirmation.
- Publishing credentials and `~/.config/agentico-release/release-key.pem` remain local; no release credentials enter GitHub Actions.
- Every release contains exactly the universal DMG, x64/arm64 AppImages, and amd64/arm64 DEBs.
- Every desktop artifact appears exactly once in the Ed25519-signed `checksums.txt` and GitHub release.
- Docker uses `electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9`.
- Linux builds run sequentially because they share `desktop/out`, `desktop/resources`, and `desktop/dist`.
- A build, identity, architecture, inventory, or checksum failure aborts before the next outward-facing step.
- DEB remains package-manager-managed; only AppImage uses in-app replacement.
- All behavior changes follow strict red-green TDD.

---

### Task 1: Define the release artifact and Docker plan contract

**Files:**

- Create: `desktop/scripts/lib/release-artifacts.mjs`
- Create: `desktop/scripts/lib/release-artifacts.test.mjs`

**Interfaces:**

- Produces: `LINUX_BUILDER_IMAGE: string`
- Produces: `releaseVersionFromTag(tag: string): string`
- Produces: `expectedDesktopArtifacts(tag: string): readonly ArtifactExpectation[]`
- Produces: `resolvePackageTarget(platform: string, processArch: string, packageArch?: string): PackageTarget`
- Produces: `selectPackageArtifact(files: readonly string[], target: PackageTarget, format: string): string`
- Produces: `createLinuxDockerPlan(options): readonly DockerInvocation[]`
- Consumes: no production module; this is the pure boundary used by later scripts.

- [ ] **Step 1: Write failing literal-expectation tests**

The production bugs these tests catch are: dropping an architecture, swapping Debian architecture names, selecting the other architecture's package when both are present, using an unpinned container, or omitting the linked-worktree Git mount.

```js
import { describe, expect, it } from "vitest";
import {
  createLinuxDockerPlan,
  expectedDesktopArtifacts,
  releaseVersionFromTag,
  resolvePackageTarget,
  selectPackageArtifact,
} from "./release-artifacts.mjs";

it("defines the complete v0.150.0 desktop inventory", () => {
  expect(expectedDesktopArtifacts("v0.150.0").map(({ name }) => name)).toEqual([
    "Agentico-mac-universal.dmg",
    "Agentico-x64.AppImage",
    "Agentico-arm64.AppImage",
    "agentico_0.150.0_amd64.deb",
    "agentico_0.150.0_arm64.deb",
  ]);
});

it("selects only the requested Linux architecture when dist contains both", () => {
  const files = [
    "Agentico-x64.AppImage",
    "Agentico-arm64.AppImage",
    "agentico_0.150.0_amd64.deb",
    "agentico_0.150.0_arm64.deb",
  ];
  expect(
    selectPackageArtifact(files, { os: "linux", arch: "arm64" }, "AppImage"),
  ).toBe("Agentico-arm64.AppImage");
  expect(
    selectPackageArtifact(files, { os: "linux", arch: "x64" }, "deb"),
  ).toBe("agentico_0.150.0_amd64.deb");
});

it("builds sequential pinned Docker invocations with worktree Git metadata", () => {
  const plan = createLinuxDockerPlan({
    repoRoot: "/repo/worktree",
    gitCommonDir: "/repo/.git",
    volumePrefix: "agentico-release",
  });
  expect(plan.map(({ arch }) => arch)).toEqual(["x64", "arm64"]);
  expect(plan[0].args).toEqual(
    expect.arrayContaining([
      "--rm",
      "--platform",
      "linux/amd64",
      "-e",
      "AGENTICO_PACKAGE_ARCH=x64",
      "-v",
      "/repo/worktree:/repo/worktree",
      "-v",
      "/repo/.git:/repo/.git",
    ]),
  );
  expect(plan[0].args.join(" ")).toContain(
    "electronuserland/builder:22@sha256:b76a82a6c6a8a1dea1abbc93e394f54316744824b64e6a50d959f1e3ba8951a9",
  );
});
```

- [ ] **Step 2: Run the new test and verify RED**

Run: `npm test --workspace desktop -- --run scripts/lib/release-artifacts.test.mjs`

Expected: FAIL because `release-artifacts.mjs` does not exist.

- [ ] **Step 3: Implement the minimal pure contract**

Implement strict tag parsing, a frozen literal artifact list, exact target selectors, and Docker arguments. The plan's container command is:

```js
const x64Command = "npm ci && npm run package:verify --workspace desktop";
const arm64BuildCommand = "npm ci && npm run package:build --workspace desktop";
const arm64VerifyCommand =
  "npm ci --ignore-scripts && AGENTICO_VERIFY_ARTIFACTS_ONLY=1 node desktop/scripts/verify-package.mjs";
```

Both package assemblies use the same pinned amd64 builder image, with target
architecture supplied through `AGENTICO_PACKAGE_ARCH`. The arm64 package is
then verified in a distinct pinned native arm64 Node image with its own
`node_modules` volume and clean lockfile install. Named volumes shadow
`/repo/node_modules` and back Electron/electron-builder caches. Reject tags
outside `^v\d+\.\d+\.\d+$`, unknown package architectures, and zero/multiple
artifact matches.

- [ ] **Step 4: Run the focused test and verify GREEN**

Run: `npm test --workspace desktop -- --run scripts/lib/release-artifacts.test.mjs`

Expected: PASS with all artifact and Docker-plan tests green.

- [ ] **Step 5: Commit the contract**

```bash
git add desktop/scripts/lib/release-artifacts.mjs desktop/scripts/lib/release-artifacts.test.mjs
git commit -m "Define Linux desktop release contract" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 2: Make package verification target-aware

**Files:**

- Modify: `desktop/scripts/verify-package.mjs`
- Modify: `desktop/scripts/lib/release-artifacts.test.mjs`

**Interfaces:**

- Consumes: `resolvePackageTarget` and `selectPackageArtifact` from Task 1.
- Produces: `desktop/dist/package-verification-<os>-<arch>.json` in addition to the existing `package-verification.json` compatibility receipt.

- [ ] **Step 1: Add a failing cross-target verification test**

Extend the pure contract test with the bug that currently occurs when an x64 Linux container builds arm64 while both architectures exist:

```js
it("resolves an explicit arm64 package target independently of process.arch", () => {
  expect(resolvePackageTarget("linux", "x64", "arm64")).toEqual({
    os: "linux",
    arch: "arm64",
  });
  expect(resolvePackageTarget("linux", "arm64", "x64")).toEqual({
    os: "linux",
    arch: "x64",
  });
});

it("rejects duplicate target artifacts instead of choosing nondeterministically", () => {
  expect(() =>
    selectPackageArtifact(
      ["Agentico-arm64.AppImage", "Agentico-arm64.AppImage"],
      { os: "linux", arch: "arm64" },
      "AppImage",
    ),
  ).toThrow(/exactly one arm64 AppImage/);
});
```

- [ ] **Step 2: Verify RED against the initial Task 1 implementation**

Run: `npm test --workspace desktop -- --run scripts/lib/release-artifacts.test.mjs`

Expected: FAIL until explicit cross-target resolution and duplicate detection exist.

- [ ] **Step 3: Route `verify-package.mjs` through the target contract**

At process startup resolve:

```js
const packageTarget = resolvePackageTarget(
  process.platform,
  process.arch,
  process.env.AGENTICO_PACKAGE_ARCH,
);
```

Then:

- select the AppImage/DEB matching `packageTarget.arch`, not every matching suffix;
- call `unpackedExecutablePath(desktopDir, process.platform, packageTarget.arch)`;
- compare identity `os` and `arch` to `packageTarget`, not the container host;
- retain binary execution, `go version -m`, ASAR, fuse, and resource validation for each package independently;
- write both `package-verification.json` and `package-verification-${os}-${arch}.json` with the same receipt.

- [ ] **Step 4: Run package-layout, identity, and artifact tests**

Run:

```bash
npm test --workspace desktop -- --run scripts/lib/release-artifacts.test.mjs scripts/lib/package-layout.test.mjs scripts/lib/identity.test.mjs
```

Expected: PASS.

- [ ] **Step 5: Commit target-aware verification**

```bash
git add desktop/scripts/verify-package.mjs desktop/scripts/lib/release-artifacts.test.mjs
git commit -m "Verify cross-architecture desktop packages" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 3: Add the local Docker Linux release orchestrator

**Files:**

- Create: `desktop/scripts/package-linux-release.mjs`
- Create: `desktop/scripts/package-linux-release.test.mjs`
- Modify: `desktop/package.json`

**Interfaces:**

- Consumes: `LINUX_BUILDER_IMAGE`, `createLinuxDockerPlan`, and `releaseVersionFromTag` from Task 1.
- Produces CLI: `node desktop/scripts/package-linux-release.mjs [--print-plan]`.
- Produces npm command: `npm run package:linux:release --workspace desktop`.

- [ ] **Step 1: Write failing script behavior tests**

The script accepts dependency injection through an exported `runLinuxRelease(options)` function. Tests use a real temporary directory and fake command boundary; assertions target returned state and created receipts, not calls to the fake itself.

```js
it("rejects a dirty checkout before attempting a container build", () => {
  expect(() =>
    runLinuxRelease({
      repoRoot: tempRoot,
      gitStatus: " M Makefile",
      exactTag: "v0.150.0",
      freeBytes: 20 * 1024 ** 3,
      dockerAvailable: true,
      execute: () => {
        throw new Error("must not execute");
      },
    }),
  ).toThrow(/working tree is dirty/);
});

it("returns x64 then arm64 receipts only after both builds succeed", () => {
  const result = runLinuxRelease(validFixtureOptions());
  expect(result).toEqual({
    tag: "v0.150.0",
    completed: ["x64", "arm64"],
    receipts: [
      "package-verification-linux-x64.json",
      "package-verification-linux-arm64.json",
    ],
  });
});
```

Also cover Docker unavailable, less than 12 GiB free, non-exact tag, first-build failure preventing arm64, and `--print-plan` producing JSON without executing Docker.

- [ ] **Step 2: Run the script test and verify RED**

Run: `npm test --workspace desktop -- --run scripts/package-linux-release.test.mjs`

Expected: FAIL because the orchestrator does not exist.

- [ ] **Step 3: Implement preflight and sequential execution**

Production dependencies resolve:

```js
const repoRoot = git("rev-parse", "--show-toplevel");
const gitCommonDir = resolve(repoRoot, git("rev-parse", "--git-common-dir"));
const exactTag = git("describe", "--tags", "--exact-match");
const gitStatus = git("status", "--porcelain");
const freeBytes = statfsSync(repoRoot).bavail * statfsSync(repoRoot).bsize;
```

Check `docker info`, inspect/pull the pinned digest, then execute each plan item synchronously. After each successful `package:verify`, require its architecture-specific receipt and distributable files before moving to the next architecture. Never invoke GoReleaser or GitHub from this script.

- [ ] **Step 4: Run focused tests and print-plan smoke**

Run:

```bash
npm test --workspace desktop -- --run scripts/package-linux-release.test.mjs scripts/lib/release-artifacts.test.mjs
node desktop/scripts/package-linux-release.mjs --print-plan
```

Expected: tests PASS; print-plan emits two ordered invocations and starts no container.

- [ ] **Step 5: Commit the orchestrator**

```bash
git add desktop/package.json desktop/scripts/package-linux-release.mjs desktop/scripts/package-linux-release.test.mjs
git commit -m "Build Linux release packages locally" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 4: Gate the complete artifact set and signed checksum manifest

**Files:**

- Create: `desktop/scripts/verify-release-artifacts.mjs`
- Create: `desktop/scripts/verify-release-artifacts.test.mjs`
- Modify: `desktop/scripts/lib/release-artifacts.mjs`
- Modify: `desktop/package.json`

**Interfaces:**

- Produces: `validateArtifactInventory({ tag, revision, files, receipts, sizes }): string[]`.
- Produces: `validateChecksumManifest(text: string, expectedNames: readonly string[]): string[]`.
- Produces CLI: `verify-release-artifacts.mjs packages [--linux-only]|manifest`.

- [ ] **Step 1: Write failing inventory and checksum tests**

```js
it("accepts one verified receipt and nonempty file for every desktop artifact", () => {
  expect(validateArtifactInventory(completeV0150Fixture())).toEqual([]);
});

it("rejects an arm64 receipt carrying the x64 identity", () => {
  const fixture = completeV0150Fixture();
  fixture.receipts["package-verification-linux-arm64.json"].identity.arch =
    "x64";
  expect(validateArtifactInventory(fixture)).toContain(
    "package-verification-linux-arm64.json identity arch=x64, expected arm64",
  );
});

it("requires exactly one checksum line per desktop artifact", () => {
  const expected = expectedDesktopArtifacts("v0.150.0").map(({ name }) => name);
  const manifest = expected
    .map((name) => `${"a".repeat(64)}  ${name}`)
    .join("\n");
  expect(validateChecksumManifest(manifest, expected)).toEqual([]);
  expect(
    validateChecksumManifest(
      manifest.replace(/.*Agentico-arm64.AppImage\n/, ""),
      expected,
    ),
  ).toContain("checksums.txt has no entry for Agentico-arm64.AppImage");
});
```

Tests also cover wrong desktop/server version, revision, OS, zero bytes, missing/duplicate artifacts, malformed checksum hashes, and duplicate checksum entries.

- [ ] **Step 2: Run the test and verify RED**

Run: `npm test --workspace desktop -- --run scripts/verify-release-artifacts.test.mjs`

Expected: FAIL because validation is absent.

- [ ] **Step 3: Implement package and manifest modes**

`packages` reads the exact tag/revision, `desktop/dist`, and these receipts:

```text
package-verification-darwin-universal.json
package-verification-linux-x64.json
package-verification-linux-arm64.json
```

It exits nonzero with all accumulated failures. `manifest` reads root
`dist/checksums.txt`, validates all expected names, and executes:

```bash
node desktop/scripts/release-sign.mjs verify dist/checksums.txt
```

`packages --linux-only` requires the two Linux receipts and four Linux
artifacts while omitting the DMG; it exists only for the package-only rehearsal
and does not weaken the default release gate.

Both modes write a JSON evidence file under `desktop/dist` without weakening a failure exit.

- [ ] **Step 4: Run focused tests**

Run: `npm test --workspace desktop -- --run scripts/verify-release-artifacts.test.mjs scripts/lib/release-artifacts.test.mjs`

Expected: PASS.

- [ ] **Step 5: Commit the release gates**

```bash
git add desktop/package.json desktop/scripts/lib/release-artifacts.mjs desktop/scripts/verify-release-artifacts.mjs desktop/scripts/verify-release-artifacts.test.mjs
git commit -m "Gate complete desktop release artifacts" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 5: Wire all desktop artifacts into Make and GoReleaser

**Files:**

- Modify: `Makefile`
- Modify: `.goreleaser.yaml`
- Modify: `desktop/scripts/audit-release.mjs`
- Modify: `desktop/scripts/audit-release.test.mjs`

**Interfaces:**

- Consumes: package and manifest verification CLIs from Task 4.
- Produces: unchanged external command `make release` with the expanded internal pipeline.

- [ ] **Step 1: Add a failing behavioral audit test**

Export `auditGoReleaserDesktopArtifacts(configText, expectedNames)` and test omissions independently of the real config:

```js
it("rejects a release config that uploads an AppImage without signing it", () => {
  const config = `
checksum:
  extra_files:
    - glob: desktop/dist/Agentico-mac-universal.dmg
release:
  extra_files:
    - glob: desktop/dist/Agentico-mac-universal.dmg
    - glob: desktop/dist/Agentico-x64.AppImage
`;
  expect(
    auditGoReleaserDesktopArtifacts(config, [
      "Agentico-mac-universal.dmg",
      "Agentico-x64.AppImage",
    ]),
  ).toContain("GoReleaser checksum.extra_files omits Agentico-x64.AppImage");
});
```

Add the real `.goreleaser.yaml` audit to `runReleaseAudit`, so `npm run audit:release` is the consumer-level configuration gate.

- [ ] **Step 2: Run the audit test and verify RED**

Run: `npm test --workspace desktop -- --run scripts/audit-release.test.mjs`

Expected: FAIL because GoReleaser artifact auditing does not exist.

- [ ] **Step 3: Expand GoReleaser and `make release`**

Set both `checksum.extra_files` and `release.extra_files` to the five exact
artifact paths plus the signed desktop envelope. Keep `make release` as the one
audited command:

```make
node desktop/scripts/release-run.mjs
```

The macOS verification step writes/copies `package-verification-darwin-universal.json`; add that receipt behavior in `verify-package.mjs` if Task 2 does not already cover it.

- [ ] **Step 4: Run audit and release-script suites**

Run:

```bash
npm test --workspace desktop -- --run scripts/audit-release.test.mjs scripts/verify-release-artifacts.test.mjs scripts/package-linux-release.test.mjs
npm run audit:release --workspace desktop
```

Expected: PASS, with the real GoReleaser configuration accepted.

- [ ] **Step 5: Commit pipeline integration**

```bash
git add Makefile .goreleaser.yaml desktop/scripts/audit-release.mjs desktop/scripts/audit-release.test.mjs desktop/scripts/verify-package.mjs
git commit -m "Publish Linux desktop release artifacts" -m "Co-authored-by: Codex <noreply@openai.com>"
```

---

### Task 6: Preserve the one-command maintainer workflow in documentation and skill

**Files:**

- Modify: `docs/desktop/release-support.md`
- Modify outside repository: `/Users/ivar.lazzaro/.claude/skills/release-agentico/SKILL.md`

**Interfaces:**

- Consumes: the final `make release` behavior from Task 5.
- Produces: unchanged `/release-agentico` invocation with Docker preflight and expanded verification.

- [ ] **Step 1: Update repository source-of-truth documentation**

Document:

- Docker/buildx and at least 12 GiB free as local prerequisites;
- the native DMG plus sequential x64/arm64 Linux container builds;
- five desktop packages plus the signed desktop release envelope;
- unchanged `git tag ... && make release` command;
- failure boundaries before GoReleaser and after partial publication;
- AppImage self-update versus DEB package-manager guidance.

Remove the stale statement that protected CI tag runs are the only accepted signing environment; the documented operator-driven local model is authoritative.

- [ ] **Step 2: Update the local `/release-agentico` skill**

Add Docker daemon/image/disk checks to Preflight. Keep the confirmation and command exactly:

```bash
git tag vX.Y.Z
AGENTICO_RELEASE_NOTES_FILE=/absolute/path/to/notes.md make release
```

Update Verify to require both AppImages, both DEBs, and one checksum line per desktop artifact. Update timing guidance to note the two sequential Linux builds. Do not add any new maintainer command.

- [ ] **Step 3: Self-review docs and skill consistency**

Run:

```bash
rg -n 'DMG|AppImage|DEB|Docker|make release|checksums.txt' \
  docs/desktop/release-support.md \
  /Users/ivar.lazzaro/.claude/skills/release-agentico/SKILL.md
```

Expected: both documents describe the same five artifacts, local credentials, and single `make release` entry point; no protected-CI-only claim remains.

- [ ] **Step 4: Commit repository documentation**

```bash
git add docs/desktop/release-support.md
git commit -m "Document multi-platform desktop releases" -m "Co-authored-by: Codex <noreply@openai.com>"
```

The local skill edit is intentionally not part of the repository commit; report it separately in the handoff.

---

### Task 7: Run local multi-platform rehearsal and required verification

**Files:**

- Modify only if a test exposes a defect; every defect begins with a failing regression test.

**Interfaces:**

- Verifies every interface produced by Tasks 1–6.

- [ ] **Step 1: Run repository static and unit gates**

Run:

```bash
npm run check
npm test
npm run test:security
make test-fast
go vet ./...
go build ./...
```

Expected: all commands exit 0.

- [ ] **Step 2: Run release audit gates**

Run:

```bash
npm run audit:release
npm run release:verify
```

Expected: both commands exit 0; non-tag mode may retain its existing credential warning.

- [ ] **Step 3: Rehearse both Linux container builds without publishing**

Use an isolated local clone so the valid rehearsal tag cannot affect this
repository or another agent's worktree:

```bash
rehearsal_root=$(mktemp -d /tmp/agentico-linux-release-rehearsal.XXXXXX)
git clone --local . "$rehearsal_root/repo"
git -C "$rehearsal_root/repo" tag v999.0.0
node "$rehearsal_root/repo/desktop/scripts/package-linux-release.mjs"
node "$rehearsal_root/repo/desktop/scripts/verify-release-artifacts.mjs" packages --linux-only
```

Expected: the exact production Docker invocations build and verify all four
Linux artifacts and both receipts. Neither GoReleaser, GitHub, nor Homebrew is
invoked. After recording the evidence, remove only the explicit
`$rehearsal_root` directory.

- [ ] **Step 4: Inspect final diff and commit state**

Run:

```bash
git diff --check
git status --short
git log --oneline fix/desktop-in-app-updates..HEAD
```

Expected: no whitespace errors, only intended files, and all implementation commits carry the Codex trailer.

- [ ] **Step 5: Prepare handoff**

Report the worktree path, branch, commits, verification tiers, Docker rehearsal evidence, local skill update, and any release-platform limitation that remains. Do not publish or create a PR without an explicit user request.
