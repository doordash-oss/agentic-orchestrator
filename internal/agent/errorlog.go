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
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// LogPhaseError appends an error message to a per-feature, per-phase error log
// at <active-run-dir>/<phase>/error.log. Silent on filesystem failures —
// the logger is best-effort diagnostics, not authoritative state. The feature
// must carry a valid ActiveRun so the log is scoped to the current run (and
// thus preserved verbatim if the run is later sealed by a rewind).
func LogPhaseError(stateDir string, f *feature.Feature, phase, msg string) {
	if stateDir == "" || f == nil || f.ID == "" || phase == "" {
		return
	}
	dir := filepath.Join(ActiveRunDir(stateDir, f), phase)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	fh, err := os.OpenFile(filepath.Join(dir, "error.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = fh.Close() }()
	_, _ = fmt.Fprintf(fh, "%s %s\n", time.Now().Format(time.RFC3339), msg)
}
