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
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// On-disk layout under <state-dir>/supervisor:
//
//	settings.json                      committed {harness, model, effort}
//	conversation.json                  current conversation pointer
//	conversations/<id>/transcript.*    durable transcript and index
//	conversations/<id>/generations/<n> per-launch PID dir and logs
//
// Generation directories sit four levels below the state dir, deeper than
// the recovering session manager scans, so a surviving supervisor process
// is never restored as a synthetic session.
const (
	supervisorDirName   = "supervisor"
	settingsFileName    = "settings.json"
	conversationFile    = "conversation.json"
	conversationsDir    = "conversations"
	generationsDirName  = "generations"
	persistedFileFormat = 1
)

type persistedSettings struct {
	Format  int    `json:"format"`
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

type persistedConversation struct {
	Format         int    `json:"format"`
	ConversationID string `json:"conversation_id"`
	Generation     int64  `json:"generation"`
	StreamEpoch    string `json:"stream_epoch"`
}

func loadSettings(dir string) (Settings, error) {
	var stored persistedSettings
	found, err := readJSON(filepath.Join(dir, settingsFileName), &stored)
	if err != nil || !found {
		return Settings{}, err
	}
	return Settings{Harness: stored.Harness, Model: stored.Model, Effort: stored.Effort}, nil
}

func saveSettings(dir string, s Settings) error {
	return writeJSONAtomic(filepath.Join(dir, settingsFileName), persistedSettings{
		Format: persistedFileFormat, Harness: s.Harness, Model: s.Model, Effort: s.Effort,
	})
}

func loadConversation(dir string) (persistedConversation, bool, error) {
	var stored persistedConversation
	found, err := readJSON(filepath.Join(dir, conversationFile), &stored)
	if err != nil || !found {
		return persistedConversation{}, false, err
	}
	if stored.ConversationID == "" {
		return persistedConversation{}, false, nil
	}
	return stored, true, nil
}

func saveConversation(dir string, conv persistedConversation) error {
	conv.Format = persistedFileFormat
	return writeJSONAtomic(filepath.Join(dir, conversationFile), conv)
}

// readJSON decodes path into out, reporting found=false for a missing file.
func readJSON(path string, out any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return true, nil
}

func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// writeFileAtomic replaces path with data through a same-directory temp
// file that is fsynced before the rename; the directory is fsynced after so
// the rename itself survives a crash.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
