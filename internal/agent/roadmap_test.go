// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"strings"
	"testing"
)

func TestParseRoadmapMultiPhase(t *testing.T) {
	roadmap := `# Feature — Implementation Roadmap

## Overview

Building a new feature.

## Phase 1: First Vertical Slice

### Goal
Build the end-to-end skeleton.

### Proves
- The request can cross the UI, API, and parser boundary.

### Scope
Wire the narrow path.

### Key Tests
- End-to-end smoke test for the narrow path.

### Stub Inventory

| Component | Stub Behavior | Retired In |
|-----------|--------------|------------|
| Parser | Returns empty | Phase 2 |

## Phase 2: Parser Behavior

### Goal
Replace parser stub with real implementation.

### Proves
- Real parser behavior works.

### Scope
Replace parser behavior.

### Retires Stubs
- Parser stub from Phase 1
- Validator stub from Phase 1

### Key Tests
- Parse valid input
- Parse invalid input

## Phase 3: Polish

### Goal
Final polish and edge cases.

## Overall Exit Criteria
- Relevant tests pass
`

	phases, err := ParseRoadmap(roadmap)
	if err != nil {
		t.Fatalf("ParseRoadmap: %v", err)
	}
	if len(phases) != 3 {
		t.Fatalf("expected 3 phases, got %d", len(phases))
	}

	// Phase 1
	if phases[0].Number != 1 {
		t.Errorf("phase 1 number = %d", phases[0].Number)
	}
	if phases[0].Type != "tracer-bullet" {
		t.Errorf("phase 1 type = %q, want tracer-bullet", phases[0].Type)
	}
	if !strings.Contains(phases[0].Name, "First Vertical Slice") {
		t.Errorf("phase 1 name = %q, expected to contain 'First Vertical Slice'", phases[0].Name)
	}
	if phases[0].Goal == "" {
		t.Error("phase 1 goal should not be empty")
	}

	// Phase 2
	if phases[1].Number != 2 {
		t.Errorf("phase 2 number = %d", phases[1].Number)
	}
	if phases[1].Type != "tdd-fill-in" {
		t.Errorf("phase 2 type = %q, want tdd-fill-in", phases[1].Type)
	}
	if len(phases[1].StubsToRetire) != 2 {
		t.Errorf("phase 2 stubs to retire = %d, want 2", len(phases[1].StubsToRetire))
	}

	// Phase 3
	if phases[2].Number != 3 {
		t.Errorf("phase 3 number = %d", phases[2].Number)
	}
	if phases[2].Type != "tdd-fill-in" {
		t.Errorf("phase 3 type = %q, want tdd-fill-in", phases[2].Type)
	}
}

func TestParseRoadmapSinglePhase(t *testing.T) {
	roadmap := `# Simple Feature — Roadmap

## Phase 1: Implementation

### Goal
Implement the whole feature.

## Overall Exit Criteria
- Tests pass
`

	phases, err := ParseRoadmap(roadmap)
	if err != nil {
		t.Fatalf("ParseRoadmap: %v", err)
	}
	if len(phases) != 1 {
		t.Fatalf("expected 1 phase, got %d", len(phases))
	}
	if phases[0].Type != "collapsed" {
		t.Errorf("single phase type = %q, want collapsed", phases[0].Type)
	}
}

func TestParseRoadmapNoPhases(t *testing.T) {
	roadmap := `# Feature — Roadmap

## Overview

This roadmap has no phases defined.

## Exit Criteria
- Tests pass
`

	_, err := ParseRoadmap(roadmap)
	if err == nil {
		t.Error("expected error for roadmap with no phases")
	}
	if !strings.Contains(err.Error(), "no phases found") {
		t.Errorf("error = %v, expected 'no phases found'", err)
	}
}

func TestParseRoadmapCollapsedWithSuffix(t *testing.T) {
	// LLMs sometimes produce "Phase 1/1 (Collapsed):" style headers
	roadmap := `# Feature — Roadmap

## Phase 1/1 (Collapsed): Ghost CTA Entry + Welcome Panel

### Goal
Implement the full empty state experience.

## Overall Exit Criteria
- Relevant tests pass
`

	phases, err := ParseRoadmap(roadmap)
	if err != nil {
		t.Fatalf("ParseRoadmap: %v", err)
	}
	if len(phases) != 1 {
		t.Fatalf("expected 1 phase, got %d", len(phases))
	}
	if phases[0].Number != 1 {
		t.Errorf("phase number = %d, want 1", phases[0].Number)
	}
	if phases[0].Type != "collapsed" {
		t.Errorf("phase type = %q, want collapsed", phases[0].Type)
	}
	if phases[0].Name != "Ghost CTA Entry + Welcome Panel" {
		t.Errorf("phase name = %q", phases[0].Name)
	}
	if phases[0].Goal == "" {
		t.Error("phase goal should not be empty")
	}
}

func TestParseRoadmapMultiPhaseWithSuffix(t *testing.T) {
	// Headers like "Phase 1/3:" should also work
	roadmap := `# Feature — Roadmap

## Phase 1/3: Skeleton

### Goal
Build skeleton.

## Phase 2/3: Fill In

### Goal
Fill in stubs.

## Phase 3/3: Polish

### Goal
Final polish.
`

	phases, err := ParseRoadmap(roadmap)
	if err != nil {
		t.Fatalf("ParseRoadmap: %v", err)
	}
	if len(phases) != 3 {
		t.Fatalf("expected 3 phases, got %d", len(phases))
	}
	if phases[0].Type != "tracer-bullet" {
		t.Errorf("phase 1 type = %q, want tracer-bullet", phases[0].Type)
	}
	if phases[1].Name != "Fill In" {
		t.Errorf("phase 2 name = %q", phases[1].Name)
	}
}

func TestParseRoadmapMalformedHeaders(t *testing.T) {
	// Headers without "Phase N:" pattern should not match
	roadmap := `# Feature

## Step 1: Do something
## Step 2: Do more
`

	_, err := ParseRoadmap(roadmap)
	if err == nil {
		t.Error("expected error for roadmap with malformed headers")
	}
}

func TestExtractSection(t *testing.T) {
	content := `
### Goal
Build the skeleton.

### Smoke Test
Run the E2E test.

### Next Section
Something else.
`

	goal := extractSection(content, "Goal")
	if goal != "Build the skeleton." {
		t.Errorf("goal = %q", goal)
	}

	smoke := extractSection(content, "Smoke Test")
	if smoke != "Run the E2E test." {
		t.Errorf("smoke = %q", smoke)
	}

	missing := extractSection(content, "NonExistent")
	if missing != "" {
		t.Errorf("missing section should be empty, got %q", missing)
	}
}

func TestExtractListSection(t *testing.T) {
	content := `
### Retires Stubs
- Parser stub
- Validator stub
- Handler stub

### Next
`

	items := extractListSection(content, "Retires Stubs")
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0] != "Parser stub" {
		t.Errorf("item 0 = %q", items[0])
	}
	if items[2] != "Handler stub" {
		t.Errorf("item 2 = %q", items[2])
	}
}
