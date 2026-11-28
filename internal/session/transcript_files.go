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

package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// Session IDs repeat across resumes and rewinds, so every start writes its
// own segment file named by ID hash, run and start time.
const transcriptFilePrefix = "session-transcript-"

func transcriptIDHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%x", sum[:8])
}

// LegacyTranscriptPath is the single per-ID transcript file written before
// segments, which the latest session with that ID overwrote.
func LegacyTranscriptPath(dir, id string) string {
	return filepath.Join(dir, transcriptFilePrefix+transcriptIDHash(id)+".jsonl")
}

func transcriptSegmentPrefix(id string, run int) string {
	return fmt.Sprintf("%s%s-run%d-", transcriptFilePrefix, transcriptIDHash(id), run)
}

func createTranscriptSegment(dir, id string, run int, now time.Time) (string, error) {
	stamp := now.UnixNano()
	for range 100 {
		path := filepath.Join(dir, fmt.Sprintf("%s%019d.jsonl", transcriptSegmentPrefix(id, run), stamp))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return path, f.Close()
		}
		if !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("creating transcript segment: %w", err)
		}
		stamp++
	}
	return "", errors.New("creating transcript segment: no free name")
}

// TranscriptSegments lists the transcript segments session id wrote in run,
// oldest first.
func TranscriptSegments(dir, id string, run int) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing transcript segments: %w", err)
	}
	prefix := transcriptSegmentPrefix(id, run)
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".jsonl") {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// ReadPersistedTranscript reads one persisted transcript file into message-log rows.
func ReadPersistedTranscript(path string) ([]llm.SDKMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	var messages []llm.SDKMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record persistedTranscriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			log.Printf("session-manager: skip malformed transcript row in %s: %v", path, err)
			continue
		}
		msg := record.Message.sdkMessage()
		switch {
		case record.Index == len(messages):
			messages = append(messages, msg)
		case record.Index >= 0 && record.Index < len(messages):
			messages[record.Index] = msg
		default:
			log.Printf("session-manager: skip out-of-order transcript row %d in %s", record.Index, path)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading transcript: %w", err)
	}
	return messages, nil
}
