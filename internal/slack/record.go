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

package slack

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"gopkg.in/yaml.v3"
)

// recordFilename is the durable, Slack-owned companion of a feature record.
// It lives in the feature's state directory next to feature.yaml and never
// contains the token.
const recordFilename = "slack.yaml"

const recordVersion = 1

// featureRecord is the per-feature Slack state: one entry per destination
// the integration has ever posted to, root card timestamp included.
type featureRecord struct {
	Version      int                          `yaml:"version"`
	Destinations map[string]destinationRecord `yaml:"destinations"`
	TagCounter   int                          `yaml:"tag_counter,omitempty"`
	Pending      []pendingInputRecord         `yaml:"pending_inputs,omitempty"`
}

type pendingInputRecord struct {
	Identity        string            `yaml:"identity"`
	SourceFeatureID string            `yaml:"source_feature_id"`
	Kind            string            `yaml:"kind"`
	RequestID       string            `yaml:"request_id,omitempty"`
	QuestionIndex   int               `yaml:"question_index,omitempty"`
	GatePath        string            `yaml:"gate_path,omitempty"`
	Iteration       int               `yaml:"iteration,omitempty"`
	WaitingSince    time.Time         `yaml:"waiting_since,omitempty"`
	ReviewID        string            `yaml:"review_id,omitempty"`
	ReviewMode      string            `yaml:"review_mode,omitempty"`
	TargetPhase     string            `yaml:"target_phase,omitempty"`
	ArtifactID      string            `yaml:"artifact_id,omitempty"`
	RunNumber       int               `yaml:"run_number,omitempty"`
	SourceRevision  string            `yaml:"source_revision,omitempty"`
	Tag             string            `yaml:"tag,omitempty"`
	PostedAt        time.Time         `yaml:"posted_at,omitempty"`
	MessageTS       map[string]string `yaml:"message_timestamps,omitempty"`
	FileIDs         map[string]string `yaml:"file_ids,omitempty"`
}

// destinationRecord is one resolved destination the integration reached at
// least once. Entries survive recipient removal so re-adding a recipient
// continues the same thread.
type destinationRecord struct {
	Kind        string              `yaml:"kind"`
	SlackID     string              `yaml:"slack_id"`
	DisplayName string              `yaml:"display_name"`
	ChannelID   string              `yaml:"channel_id,omitempty"`
	RootTS      string              `yaml:"root_ts,omitempty"`
	Ledger      []string            `yaml:"ledger,omitempty"`
	Failure     *destinationFailure `yaml:"failure,omitempty"`
}

type destinationFailure struct {
	Code          errcat.Code `yaml:"code"`
	SlackError    string      `yaml:"slack_error"`
	Count         int         `yaml:"count"`
	FirstFailedAt time.Time   `yaml:"first_failed_at"`
	LastFailedAt  time.Time   `yaml:"last_failed_at"`
}

// destinationKey identifies a destination across restarts: recipient kind
// plus Slack ID.
func destinationKey(kind, slackID string) string {
	return kind + ":" + slackID
}

// ledgerAppend records one accepted message timestamp. Ledger order is
// post order for the destination, root card included.
func (d *destinationRecord) ledgerAppend(ts string) {
	if ts == "" {
		return
	}
	for _, existing := range d.Ledger {
		if existing == ts {
			return
		}
	}
	d.Ledger = append(d.Ledger, ts)
}

func (d *destinationRecord) recordFailure(code errcat.Code, slackError string, at time.Time) {
	at = at.UTC()
	if d.Failure == nil {
		d.Failure = &destinationFailure{
			FirstFailedAt: at,
		}
	}
	d.Failure.Code = code
	d.Failure.SlackError = slackError
	d.Failure.Count++
	d.Failure.LastFailedAt = at
}

// recordPath is the durable file backing a feature's in-memory record.
func recordPath(stateDir, featureID string) string {
	return filepath.Join(stateDir, featureID, recordFilename)
}

// loadFeatureRecord reads the durable record from disk. A missing record is
// a fresh one, not an error: the caller creates the card on first contact.
func loadFeatureRecord(stateDir, featureID string) (*featureRecord, error) {
	path := recordPath(stateDir, featureID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &featureRecord{Version: recordVersion, Destinations: map[string]destinationRecord{}}, nil
		}
		return nil, err
	}
	var record featureRecord
	if err := yaml.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parsing Slack record: %w", err)
	}
	if record.Destinations == nil {
		record.Destinations = map[string]destinationRecord{}
	}
	return &record, nil
}

// persistFeatureRecord writes the record atomically: a temp file in the
// target directory, then rename, mirroring the observe summary's write
// shape.
func persistFeatureRecord(stateDir, featureID string, record *featureRecord) error {
	dir := filepath.Join(stateDir, featureID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(record)
	if err != nil {
		return err
	}
	path := recordPath(stateDir, featureID)
	tmp, err := os.CreateTemp(dir, ".slack-*.yaml.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
