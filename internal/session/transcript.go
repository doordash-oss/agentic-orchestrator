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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// claudeProjectsDirName encodes a working directory path into the directory
// name used by the Claude CLI under ~/.claude/projects/. The CLI replaces
// path separators and dots with hyphens.
func claudeProjectsDirName(workDir string) string {
	return regexp.MustCompile(`[/.]`).ReplaceAllString(workDir, "-")
}

// TranscriptErrorDetail reads the provider's transcript file to extract
// the last error message. This catches errors like "Request too large" that
// the CLI writes to its transcript but not to the stream-json stdout protocol.
//
// Best-effort: returns "" if no protocol is set or the transcript can't be found.
func (s *Session) TranscriptErrorDetail() string {
	if s.protocol == nil {
		return ""
	}
	path := s.protocol.TranscriptPath()
	if path == "" {
		return ""
	}
	return readTranscriptError(path)
}

// readTranscriptError scans a Claude CLI transcript JSONL file for the last
// assistant message with isApiErrorMessage=true, or a result with an error
// subtype.
//
// Transcripts can be very large (50MB+) because tool results may contain
// base64-encoded file content. We read backwards from the end to find the
// last few complete JSONL lines, avoiding any need to parse huge mid-file lines.
func readTranscriptError(path string) string {
	lines := readLastLines(path, 5)

	var lastError string
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		if !strings.Contains(line, "isApiErrorMessage") &&
			!strings.Contains(line, `"subtype":"error"`) {
			continue
		}

		var entry transcriptEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		if entry.IsAPIErrorMessage && entry.Message.Role == "assistant" {
			for _, block := range entry.Message.Content {
				if block.Type == "text" && block.Text != "" {
					lastError = block.Text
				}
			}
		}

		if entry.Type == "result" && entry.Subtype == "error" && entry.Result != "" {
			lastError = entry.Result
		}
	}

	if len(lastError) > 200 {
		lastError = lastError[:200] + "…"
	}
	return lastError
}

// readLastLines reads the last n complete lines from a file by reading a
// fixed-size tail. The error/result lines we're looking for are small (1-2KB),
// but preceding lines (tool results with base64 content) can be 24MB+.
// Reading the last 64KB captures the short trailing lines without loading
// the entire file.
func readLastLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return nil
	}

	const tailSize = 64 * 1024
	readSize := info.Size()
	if readSize > tailSize {
		readSize = tailSize
	}
	offset := info.Size() - readSize

	buf := make([]byte, readSize)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return nil
	}

	// If we didn't read from the start, skip the first (possibly partial) line.
	data := string(buf)
	if offset > 0 {
		if idx := strings.IndexByte(data, '\n'); idx >= 0 {
			data = data[idx+1:]
		}
	}

	allLines := strings.Split(data, "\n")

	var result []string
	for _, l := range allLines {
		if l != "" {
			result = append(result, l)
		}
	}
	if len(result) > n {
		result = result[len(result)-n:]
	}
	return result
}

// transcriptEntry is a minimal struct for parsing the Claude CLI's JSONL
// transcript entries. Only the fields needed for error extraction are included.
type transcriptEntry struct {
	Type              string        `json:"type"`
	Subtype           string        `json:"subtype,omitempty"`
	IsAPIErrorMessage bool          `json:"isApiErrorMessage,omitempty"`
	Result            string        `json:"result,omitempty"`
	Message           transcriptMsg `json:"message"`
	Error             string        `json:"error,omitempty"`
}

type transcriptMsg struct {
	Role    string              `json:"role"`
	Content []transcriptContent `json:"content"`
}

type transcriptContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ErrorDetail returns the best available error detail for a failed session,
// checking the in-memory message log first and falling back to the CLI's
// transcript file on disk.
func (s *Session) ErrorDetail() string {
	if detail := s.messageLog.LastErrorDetail(); detail != "" {
		if !looksLikeNormalChat(detail) {
			return detail
		}
	}
	return s.TranscriptErrorDetail()
}

// ErrorDetailFromOutput extracts the error from a CLI transcript by parsing
// the Claude session ID from the phase output file and looking up the
// transcript on disk. This works without a live Session object, making it
// usable for features that failed in a previous app run.
func ErrorDetailFromOutput(outputPath, workDir string) string {
	sessionID := extractSessionIDFromOutput(outputPath)
	if sessionID == "" || workDir == "" {
		return ""
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	path := filepath.Join(home, ".claude", "projects",
		claudeProjectsDirName(workDir),
		sessionID+".jsonl")

	return readTranscriptError(path)
}

// extractSessionIDFromOutput reads the first line of output.txt and extracts
// the Claude session ID from the "[init] session=..." line.
func extractSessionIDFromOutput(outputPath string) string {
	f, err := os.Open(outputPath)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return ""
	}

	line := string(buf[:n])
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}

	const prefix = "session="
	start := strings.Index(line, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.IndexByte(line[start:], ' ')
	if end < 0 {
		return strings.TrimSpace(line[start:])
	}
	return line[start : start+end]
}

// looksLikeNormalChat returns true if the text appears to be normal assistant
// chat rather than an error message. Used to avoid surfacing things like
// "I'll start by reading the files…" as error details.
func looksLikeNormalChat(text string) bool {
	lower := strings.ToLower(text)
	normalPrefixes := []string{
		"i'll ", "i will ", "let me ", "i'm going to ",
		"sure", "here", "okay", "ok,",
	}
	for _, p := range normalPrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// ExitCodeDetail returns a human-readable string from the process exit code.
// Returns "" if the process is still running or exited cleanly.
func (s *Session) ExitCodeDetail() string {
	s.mu.Lock()
	proc := s.process
	s.mu.Unlock()

	if proc == nil || proc.ProcessState == nil {
		return ""
	}
	if proc.ProcessState.Success() {
		return ""
	}
	return fmt.Sprintf("process exited with code %d", proc.ProcessState.ExitCode())
}
