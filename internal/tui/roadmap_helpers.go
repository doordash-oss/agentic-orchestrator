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

package tui

import "fmt"

func fallbackRoadmapPhaseType(phase, total int) string {
	if total == 1 {
		return "collapsed"
	}
	if phase == 1 {
		return "tracer-bullet"
	}
	return "tdd-fill-in"
}

func roadmapResetBoundaryLabel(phase int) string {
	if phase <= 1 {
		return "base branch"
	}
	return fmt.Sprintf("end of roadmap Phase %d", phase-1)
}

func roadmapPhaseEffect(phase, total int) string {
	return fmt.Sprintf("Preserve: %s; redo: Phase %d; discard: %s",
		roadmapPhaseRangeLabel(1, phase-1),
		phase,
		roadmapPhaseRangeLabel(phase+1, total),
	)
}

func roadmapPhaseRangeLabel(start, end int) string {
	if start > end {
		return "none"
	}
	if start == end {
		return fmt.Sprintf("Phase %d", start)
	}
	return fmt.Sprintf("Phases %d-%d", start, end)
}
