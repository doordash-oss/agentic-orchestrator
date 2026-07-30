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
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"text/template"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"gopkg.in/yaml.v3"
)

// ResumeSidecarFile is the durable resume record colocated with phase_complete.
const ResumeSidecarFile = "resume.yaml"

const (
	resumePromptTemplateText = "Your previous process terminated unexpectedly mid-turn; this session resumes that conversation. {{.PhaseContext}}"
	implementResumeContext   = "Reassess the repository and your artifacts: if the iteration's work is already complete, write any missing required artifacts and the completion marker per your instructions; otherwise update progress and continue from where you left off."
)

var resumePromptTemplate = template.Must(template.New("resume").Parse(resumePromptTemplateText))

// ResumeRecord describes the provider and orchestrator identity needed to
// continue one resumable unit. It is descriptive state only; phase_complete
// remains the sole completion authority.
type ResumeRecord struct {
	ProviderSessionID     string     `yaml:"provider_session_id,omitempty"`
	Provider              string     `yaml:"provider,omitempty"`
	ResolvedModel         string     `yaml:"resolved_model,omitempty"`
	PhaseKey              string     `yaml:"phase_key"`
	Iteration             int        `yaml:"iteration,omitempty"`
	RunNumber             int        `yaml:"run_number"`
	OrchestratorSessionID string     `yaml:"orchestrator_session_id"`
	CreatedAt             time.Time  `yaml:"created_at"`
	UpdatedAt             time.Time  `yaml:"updated_at"`
	Resumed               bool       `yaml:"resumed"`
	ResumeCount           int        `yaml:"resume_count"`
	Completed             bool       `yaml:"completed"`
	CompletedAt           *time.Time `yaml:"completed_at,omitempty"`
	Rejected              bool       `yaml:"rejected"`
	RejectionReason       string     `yaml:"rejection_reason,omitempty"`
	RejectedAt            *time.Time `yaml:"rejected_at,omitempty"`
}

// ResumeCoordinator owns durable record access and the shared resume prompt.
// Resume dispatch is implemented alongside the implement loop because it also
// owns that loop's session construction contract.
type ResumeCoordinator struct {
	dir string
	mu  sync.Mutex
}

// NewResumeCoordinator returns a coordinator for the unit stored under dir.
func NewResumeCoordinator(dir string) *ResumeCoordinator {
	return &ResumeCoordinator{dir: dir}
}

// Prompt renders the shared resume template with phase-specific context.
func (c *ResumeCoordinator) Prompt(phaseContext string) string {
	return renderResumePrompt(phaseContext)
}

// Initialize writes the identity for a fresh provider process. A replay of an
// incomplete iteration replaces stale provider identity; crash-resume dispatch
// does not call Initialize and therefore mutates the same record.
func (c *ResumeCoordinator) Initialize(record ResumeRecord) {
	c.update(func(current *ResumeRecord) {
		*current = record
	})
}

// CaptureProviderInit fills provider identity learned from the init message.
func (c *ResumeCoordinator) CaptureProviderInit(info ports.ProviderInitInfo) {
	c.update(func(record *ResumeRecord) {
		if info.SessionID != "" {
			record.ProviderSessionID = info.SessionID
		}
		if record.Provider == "" {
			record.Provider = info.Provider
		}
		if record.ResolvedModel == "" && info.Model != "" {
			record.ResolvedModel = info.Model
		}
		record.UpdatedAt = time.Now()
	})
}

// CaptureProviderSnapshot is a fallback for session handles used by tests and
// legacy adapters that expose identity without an init callback.
func (c *ResumeCoordinator) CaptureProviderSnapshot(sessionID, provider, model string) {
	c.CaptureProviderInit(ports.ProviderInitInfo{
		SessionID: sessionID,
		Provider:  provider,
		Model:     model,
	})
}

// MarkResumed records one successfully launched continuation.
func (c *ResumeCoordinator) MarkResumed(at time.Time) {
	c.update(func(record *ResumeRecord) {
		record.MarkResumed(at)
	})
}

// MarkCompleted records successful completion while retaining the sidecar.
func (c *ResumeCoordinator) MarkCompleted(at time.Time) {
	c.update(func(record *ResumeRecord) {
		record.MarkCompleted(at)
	})
}

// MarkRejected records a rejected continuation without changing identity.
func (c *ResumeCoordinator) MarkRejected(reason string, at time.Time) {
	c.update(func(record *ResumeRecord) {
		record.MarkRejected(reason, at)
	})
}

// Snapshot returns the current record, or nil when it is missing or unreadable.
func (c *ResumeCoordinator) Snapshot() *ResumeRecord {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	record, err := ReadResumeRecord(c.dir)
	if err != nil {
		log.Printf("resume coordinator: read %s: %v", c.dir, err)
		return nil
	}
	return record
}

func (c *ResumeCoordinator) update(mutate func(*ResumeRecord)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	record, err := ReadResumeRecord(c.dir)
	if err != nil {
		log.Printf("resume coordinator: read %s: %v", c.dir, err)
		return
	}
	if record == nil {
		record = &ResumeRecord{}
	}
	mutate(record)
	if err := WriteResumeRecord(c.dir, *record); err != nil {
		log.Printf("resume coordinator: write %s: %v", c.dir, err)
	}
}

func renderResumePrompt(phaseContext string) string {
	var out bytes.Buffer
	if err := resumePromptTemplate.Execute(&out, struct {
		PhaseContext string
	}{PhaseContext: phaseContext}); err != nil {
		return ""
	}
	return out.String()
}

// MarkResumed increments the continuation count without changing identity.
func (r *ResumeRecord) MarkResumed(at time.Time) {
	r.Resumed = true
	r.ResumeCount++
	r.UpdatedAt = at
}

// MarkCompleted stamps successful completion without changing identity.
func (r *ResumeRecord) MarkCompleted(at time.Time) {
	r.Completed = true
	r.CompletedAt = timePtr(at)
	r.UpdatedAt = at
}

// MarkRejected records why a resume attempt was rejected without changing identity.
func (r *ResumeRecord) MarkRejected(reason string, at time.Time) {
	r.Rejected = true
	r.RejectionReason = reason
	r.RejectedAt = timePtr(at)
	r.UpdatedAt = at
}

// ReadResumeRecord reads resume.yaml from dir. Missing and unparseable files
// degrade to no record so sidecar damage cannot block the owning workflow.
func ReadResumeRecord(dir string) (*ResumeRecord, error) {
	data, err := os.ReadFile(filepath.Join(dir, ResumeSidecarFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading resume sidecar: %w", err)
	}

	var record ResumeRecord
	if err := yaml.Unmarshal(data, &record); err != nil {
		return nil, nil
	}
	return &record, nil
}

// WriteResumeRecord atomically replaces resume.yaml using a same-directory
// temporary file followed by rename.
func WriteResumeRecord(dir string, record ResumeRecord) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating resume sidecar dir: %w", err)
	}
	data, err := yaml.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshalling resume sidecar: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".resume-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("creating resume sidecar temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing resume sidecar temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing resume sidecar temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, ResumeSidecarFile)); err != nil {
		return fmt.Errorf("renaming resume sidecar temp file: %w", err)
	}
	cleanup = false
	return nil
}

func timePtr(t time.Time) *time.Time {
	return &t
}
