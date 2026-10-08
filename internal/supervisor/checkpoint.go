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

package supervisor

import (
	"encoding/json"
	"fmt"
	"strings"
)

// CheckpointSelection is the latest checkpoint a destination can rebuild.
// Records contains only records after the checkpoint's covered head. When no
// checkpoint is representable, Checkpoint is nil and Records is the input.
type CheckpointSelection struct {
	Checkpoint *Record
	Data       CheckpointData
	Records    []Record
}

func SelectCheckpoint(records []Record, harness string) (CheckpointSelection, error) {
	selection := CheckpointSelection{Records: records}
	for i := len(records) - 1; i >= 0; i-- {
		rec := &records[i]
		if rec.Kind != KindCheckpoint {
			continue
		}
		var data CheckpointData
		if err := json.Unmarshal(rec.Data, &data); err != nil {
			return CheckpointSelection{}, &ConversionError{Seq: rec.Seq, Reason: fmt.Sprintf("decode checkpoint payload: %v", err)}
		}
		if (data.NativeBaseline == nil || data.NativeBaseline.Harness != harness) && strings.TrimSpace(data.Summary) == "" {
			continue
		}
		selection.Checkpoint, selection.Data = rec, data
		selection.Records = make([]Record, 0, len(records)-i)
		for _, item := range records {
			if item.Seq > data.CoversThroughSeq && item.Kind != KindCheckpoint {
				selection.Records = append(selection.Records, item)
			}
		}
		return selection, nil
	}
	return selection, nil
}

// CheckpointNote is the readable baseline used on another harness.
func CheckpointNote(data CheckpointData) string {
	harness := "another harness"
	if data.NativeBaseline != nil && data.NativeBaseline.Harness != "" {
		harness = data.NativeBaseline.Harness
	}
	return "Agentico note: Summary of the earlier conversation, compacted on " + harness + ": " + data.Summary
}
