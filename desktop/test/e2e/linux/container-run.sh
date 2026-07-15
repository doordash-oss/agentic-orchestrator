#!/usr/bin/env bash
# Runs inside the agentico-linux-evidence container (see Dockerfile).
#
# Clones the mounted git bundle, builds + verifies the native Linux packages
# (AppImage, deb, linux-unpacked), then drives the packaged Playwright
# journeys under xvfb three times:
#   1. the full journey suite against the verified linux-unpacked app
#   2. the first-launch tracer bullet against the extracted AppImage payload
#      (--appimage-extract is the AppImage-standard execution path on
#      FUSE-less hosts; the extracted tree is the built artifact, unmodified)
#   3. the first-launch tracer bullet against the dpkg-installed deb
#      (/opt/Agentico)
# Evidence (screenshots, traces, transcripts, logs) lands in /evidence.
set -euxo pipefail

BRANCH="${AGENTICO_BRANCH:?AGENTICO_BRANCH is required}"

git clone --branch "$BRANCH" /repo.bundle /work/repo
cd /work/repo

npm ci
npm run package:verify

DIST=/work/repo/desktop/dist
XVFB=(xvfb-run -a --server-args="-screen 0 1600x1000x24")

run_journeys() { # <evidence-subdir> [playwright args…]
  local evidence="/evidence/$1"
  shift
  mkdir -p "$evidence"
  AGENTICO_E2E_EVIDENCE_DIR="$evidence" CI=1 dbus-run-session -- "${XVFB[@]}" \
    npm run test:e2e:packaged --workspace desktop -- "$@"
}

# 1. Full suite on the verified linux-unpacked app.
run_journeys unpacked

# 2. First-launch tracer bullet from the built AppImage.
APPIMAGE=$(ls "$DIST"/Agentico-*.AppImage)
(cd "$DIST" && "$APPIMAGE" --appimage-extract >/dev/null)
(
  export AGENTICO_E2E_EXECUTABLE="$DIST/squashfs-root/agentico"
  export AGENTICO_E2E_VARIANT=linux-appimage
  run_journeys appimage first-launch.spec.ts
)

# 3. First-launch tracer bullet from the installed deb.
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
  echo "head: $(git rev-parse HEAD)"
  echo "uname: $(uname -a)"
  echo "node: $(node --version)  go: $(go version)"
  echo "appimage: $(basename "$APPIMAGE")  sha256: $(sha256sum "$APPIMAGE" | cut -d' ' -f1)"
  echo "deb: $(basename "$DEB")  sha256: $(sha256sum "$DEB" | cut -d' ' -f1)"
  dpkg-query -W agentico || true
} > /evidence/run-context.txt

cp "$DIST/package-verification.json" /evidence/package-verification.json
