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

package observe

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Emitter writes Event structs as JSONL lines to a per-feature file.
type Emitter struct {
	stateDir string
	mu       sync.Mutex
}

// NewEmitter creates an Emitter that writes to <stateDir>/<featureID>/events.jsonl.
func NewEmitter(stateDir string) *Emitter {
	return &Emitter{stateDir: stateDir}
}

// Emit writes a single event as a JSON line to the feature's events.jsonl.
// Opens the file in append mode, writes, fsyncs, and closes per call.
//
// The feature directory is expected to already exist — it is created by the
// feature store when the feature is first saved. Emit intentionally does not
// create the directory: if the feature has been deleted out from under an
// in-flight phase runner, the open fails with ENOENT and the event is
// silently dropped rather than resurrecting the deleted directory.
func (e *Emitter) Emit(evt Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	path := filepath.Join(e.stateDir, evt.FeatureID, "events.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	if err := json.NewEncoder(f).Encode(evt); err != nil {
		return err
	}
	return f.Sync()
}
