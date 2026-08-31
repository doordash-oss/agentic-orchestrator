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

package feature

import (
	"fmt"
	"strings"
)

// DiffSummaryBudget is the single byte cap on a preserved child diff summary,
// enforced both when a closure persists the summary and when the server maps
// an already-persisted record into an API response.
const DiffSummaryBudget = 256 * 1024

const diffSummaryMarkerFmt = "[diff truncated: %d bytes omitted]"

// BoundDiffSummary caps s at DiffSummaryBudget bytes. Oversized input is cut
// on a line boundary and terminated by a marker stating the omitted byte
// count; input within budget passes through unchanged.
func BoundDiffSummary(s string) string {
	if len(s) <= DiffSummaryBudget {
		return s
	}
	// Reserve room for the marker plus digit growth of the omitted count.
	limit := DiffSummaryBudget - (len(diffSummaryMarkerFmt) + 20)
	kept := ""
	if idx := strings.LastIndexByte(s[:limit], '\n'); idx >= 0 {
		kept = s[:idx+1]
	}
	return kept + fmt.Sprintf(diffSummaryMarkerFmt, len(s)-len(kept))
}

// ComposeBoundedDiffSummary prefixes a raw diff with a per-file stat header
// (like `git diff --stat`) and bounds the whole via BoundDiffSummary, so the
// header survives even when the body is truncated.
func ComposeBoundedDiffSummary(raw string) string {
	header := diffStatHeader(raw)
	if header == "" {
		return BoundDiffSummary(raw)
	}
	return BoundDiffSummary(header + "\n" + raw)
}

type diffFileStat struct {
	path string
	ins  int
	del  int
}

func diffStatHeader(raw string) string {
	var files []diffFileStat
	var cur *diffFileStat
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			path := line[strings.LastIndex(line, " ")+1:]
			path = strings.TrimPrefix(path, "b/")
			files = append(files, diffFileStat{path: path})
			cur = &files[len(files)-1]
		case cur == nil:
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			cur.ins++
		case strings.HasPrefix(line, "-"):
			cur.del++
		}
	}
	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	totalIns, totalDel := 0, 0
	for _, f := range files {
		fmt.Fprintf(&sb, " %s | %d+ %d-\n", f.path, f.ins, f.del)
		totalIns += f.ins
		totalDel += f.del
	}
	fmt.Fprintf(&sb, " %d %s, %d %s, %d %s\n",
		len(files), plural(len(files), "file changed", "files changed"),
		totalIns, plural(totalIns, "insertion(+)", "insertions(+)"),
		totalDel, plural(totalDel, "deletion(-)", "deletions(-)"))
	return sb.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
