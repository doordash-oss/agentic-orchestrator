#!/usr/bin/env bash
# Native-Linux packaged evidence run, from any Docker-capable host.
#
# Exports the current HEAD as a git bundle (worktree-safe), then runs
# container-run.sh in the agentico-linux-evidence image on a real Linux
# (arm64) kernel: npm ci, package:verify (AppImage + deb + linux-unpacked),
# and the packaged Playwright journeys under xvfb against the unpacked app,
# the extracted AppImage payload, and the dpkg-installed deb.
#
# Usage: run-linux-evidence.sh <output-dir>
set -euo pipefail

OUT_DIR="${1:?usage: run-linux-evidence.sh <output-dir>}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
IMAGE="${AGENTICO_LINUX_IMAGE:-agentico-linux-evidence:latest}"
BRANCH="$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)"

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

BUNDLE_DIR="$(mktemp -d)"
trap 'rm -rf "$BUNDLE_DIR"' EXIT
git -C "$REPO_ROOT" bundle create "$BUNDLE_DIR/repo.bundle" HEAD "$BRANCH" --tags

docker build --platform linux/arm64 -t "$IMAGE" "$SCRIPT_DIR"

docker run --rm --platform linux/arm64 --shm-size=1g \
  -e AGENTICO_BRANCH="$BRANCH" \
  -v "$BUNDLE_DIR/repo.bundle":/repo.bundle:ro \
  -v "$SCRIPT_DIR/container-run.sh":/container-run.sh:ro \
  -v "$OUT_DIR":/evidence \
  "$IMAGE" bash /container-run.sh
