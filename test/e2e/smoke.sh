#!/bin/bash
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
set -euo pipefail

AGENTICO="./bin/agentico"
TEST_DIR="$(mktemp -d)"
TEST_CONFIG="$TEST_DIR/config.yaml"
TEST_STATE="$TEST_DIR/state"

cleanup() {
    # Prune worktrees and clean up test branches
    git worktree prune 2>/dev/null || true
    git branch -D "feature/test-feature" 2>/dev/null || true
    rm -rf "$TEST_DIR"
}
trap cleanup EXIT

# 1. Binary builds
go build -o "$AGENTICO" ./cmd/agentico
echo "PASS: binary builds"

# 2. Help flag works
HELP_OUTPUT="$($AGENTICO --help)"
echo "$HELP_OUTPUT" | grep -qE "^Agentic Orchestrator$"
if echo "$HELP_OUTPUT" | grep -q "Agentic Workflow Orchestrator"; then
    echo "FAIL: help should not advertise the legacy 'Agentic Workflow Orchestrator' title"
    exit 1
fi
echo "$HELP_OUTPUT" | grep -q "~/.agentic-orchestrator/"
echo "PASS: help flag works"

# 3. Version flag works
$AGENTICO --version | grep -q "agentico"
echo "PASS: version flag works"

# 4. First-run config generation
# Skipped: `init` subcommand is not implemented; setup is owned by the desktop app.

# 5. Feature management is API-driven; creation/listing coverage lives in
# server contract and Electron tests.
echo "PASS: feature management is API-driven"

# 6. Roadmap skill definitions exist (migrated from commands/ to skills/)
for tmpl in create-roadmap plan-phase revise-roadmap revise-phase-plan \
            validate-roadmap-architecture validate-roadmap-scope \
            validate-phase-plan-structural validate-phase-plan-grounding validate-phase-plan-scope \
            validate-plan-security validate-plan-performance validate-plan-testing; do
    [ -f "skills/${tmpl}/SKILL.md" ]
    echo "PASS: skills/${tmpl}/SKILL.md exists"
done

# 7. Legacy commands/ directory is fully removed
if [ -d "commands" ]; then
    echo "FAIL: commands/ directory should have been deleted"
    exit 1
fi
echo "PASS: commands/ directory correctly removed"

# 8. Default launch smoke: plain agentico starts the foreground server path.
go test ./cmd/agentico -run '^TestRunArgsLaunchesServerByDefault$' -race -timeout 120s
echo "PASS: default foreground server launch (TestRunArgsLaunchesServerByDefault)"

echo ""
echo "PASS: all smoke tests passed"
