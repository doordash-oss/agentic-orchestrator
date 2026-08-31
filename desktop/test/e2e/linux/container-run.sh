#!/usr/bin/env bash
# Copyright 2026 DoorDash, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Runs inside the agentico-linux-evidence container (see Dockerfile).
#
# Clones the mounted git bundle, builds + verifies the native Linux packages
# (AppImage, deb, linux-unpacked), then drives the packaged Playwright
# journeys under xvfb three times:
#   1. the full journey suite against the verified linux-unpacked app
#   2. the first-launch creation journey against the extracted AppImage payload
#      (--appimage-extract is the AppImage-standard execution path on
#      FUSE-less hosts; the extracted tree is the built artifact, unmodified)
#   3. the first-launch creation journey against the dpkg-installed deb
#      (/opt/Agentico)
# Evidence (screenshots, traces, transcripts, logs) lands in /evidence.
set -euxo pipefail

BRANCH="${AGENTICO_BRANCH:?AGENTICO_BRANCH is required}"
SOURCE_HEAD="${AGENTICO_SOURCE_HEAD:?AGENTICO_SOURCE_HEAD is required}"

git clone --branch "$BRANCH" /repo.bundle /work/repo
cd /work/repo
ACTUAL_HEAD="$(git rev-parse HEAD)"
test "$ACTUAL_HEAD" = "$SOURCE_HEAD"
test -z "$(git status --porcelain --untracked-files=all)"

npm ci
npm run package:verify

DIST=/work/repo/desktop/dist
XVFB=(xvfb-run -a --server-args="-screen 0 1600x1000x24")

PACKAGE_REVISION="$(node -e '
  const fs = require("node:fs");
  const manifest = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  process.stdout.write(manifest.identity.server_revision);
' "$DIST/package-verification.json")"
test "$PACKAGE_REVISION" = "$SOURCE_HEAD"

run_journeys() { # <evidence-subdir> [playwright args…]
  local evidence="/evidence/$1"
  shift
  mkdir -p "$evidence"
  AGENTICO_E2E_EVIDENCE_DIR="$evidence" CI=1 dbus-run-session -- "${XVFB[@]}" \
    npm run test:e2e:packaged --workspace desktop -- "$@"

  local variant="${AGENTICO_E2E_VARIANT:-linux}"
  test -s "$evidence/behaviors/first-launch-$variant.md"
  test -s "$evidence/behaviors/first-launch-$variant-trace.zip"
  test -s "$evidence/screenshots/ready-to-start-light.png"
}

# 1. Full suite on the verified linux-unpacked app.
run_journeys unpacked

# 2. First-launch creation journey from the built AppImage.
APPIMAGE=$(ls "$DIST"/Agentico-*.AppImage)
(cd "$DIST" && "$APPIMAGE" --appimage-extract >/dev/null)
(
  export AGENTICO_E2E_EXECUTABLE="$DIST/squashfs-root/agentico"
  export AGENTICO_E2E_VARIANT=linux-appimage
  run_journeys appimage first-launch.spec.ts
)

# 3. First-launch creation journey from the installed deb.
DEB=$(ls "$DIST"/*.deb)
dpkg -i "$DEB" || apt-get install -f -y
test -x /opt/Agentico/agentico
(
  export AGENTICO_E2E_EXECUTABLE=/opt/Agentico/agentico
  export AGENTICO_E2E_VARIANT=linux-deb
  run_journeys deb first-launch.spec.ts
)

# Preserve the machine-readable run context for the evidence bundle.
{
  echo "branch: $BRANCH"
  echo "head: $ACTUAL_HEAD"
  echo "source-worktree: clean (enforced before bundling)"
  echo "clone-worktree: clean"
  echo "package-server-revision: $PACKAGE_REVISION"
  echo "platform: $(uname -m)"
  echo "uname: $(uname -a)"
  echo "node: $(node --version)  go: $(go version)"
  echo "appimage: $(basename "$APPIMAGE")  sha256: $(sha256sum "$APPIMAGE" | cut -d' ' -f1)"
  echo "deb: $(basename "$DEB")  sha256: $(sha256sum "$DEB" | cut -d' ' -f1)"
  dpkg-query -W agentico || true
} > /evidence/run-context.txt

cp "$DIST/package-verification.json" /evidence/package-verification.json
