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

# Native-Linux packaged evidence run, from any Docker-capable host.
#
# Exports the current HEAD as a git bundle (worktree-safe), then runs
# container-run.sh in the agentico-linux-evidence image on a real Linux
# kernel: npm ci, package:verify (AppImage + deb + linux-unpacked),
# and the packaged Playwright journeys under xvfb against the unpacked app,
# the extracted AppImage payload, and the dpkg-installed deb.
#
# Usage: run-linux-evidence.sh <output-dir>
set -euo pipefail

OUT_DIR="${1:?usage: run-linux-evidence.sh <output-dir>}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
IMAGE="${AGENTICO_LINUX_IMAGE:-agentico-linux-evidence:latest}"
PLATFORM="${AGENTICO_LINUX_PLATFORM:-linux/arm64}"
BRANCH="$(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)"
SOURCE_HEAD="$(git -C "$REPO_ROOT" rev-parse HEAD)"

WORKTREE_STATUS="$(git -C "$REPO_ROOT" status --porcelain --untracked-files=all)"
if [[ -n "$WORKTREE_STATUS" ]]; then
  echo "refusing to generate evidence from a dirty worktree:" >&2
  printf '%s\n' "$WORKTREE_STATUS" >&2
  echo "commit or remove every tracked and untracked change, then rerun" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

BUNDLE_DIR="$(mktemp -d)"
trap 'rm -rf "$BUNDLE_DIR"' EXIT
git -C "$REPO_ROOT" bundle create "$BUNDLE_DIR/repo.bundle" HEAD "$BRANCH" --tags

docker build --platform "$PLATFORM" -t "$IMAGE" "$SCRIPT_DIR"

docker run --rm --platform "$PLATFORM" --shm-size=1g \
  -e AGENTICO_BRANCH="$BRANCH" \
  -e AGENTICO_SOURCE_HEAD="$SOURCE_HEAD" \
  -v "$BUNDLE_DIR/repo.bundle":/repo.bundle:ro \
  -v "$SCRIPT_DIR/container-run.sh":/container-run.sh:ro \
  -v "$OUT_DIR":/evidence \
  "$IMAGE" bash /container-run.sh
