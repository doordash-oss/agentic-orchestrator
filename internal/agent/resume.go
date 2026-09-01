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
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	sessionruntime "github.com/doordash-oss/agentic-orchestrator/internal/session"
	"gopkg.in/yaml.v3"
)

// ResumeSidecarFile is the durable resume record colocated with phase_complete.
const ResumeSidecarFile = "resume.yaml"

const (
	resumePromptTemplateText = "Your previous process terminated unexpectedly mid-turn; this session resumes that conversation. {{.PhaseContext}}"
	implementResumeContext   = "Reassess the repository and your artifacts: if the iteration's work is already complete, write any missing required artifacts, run the artifact preflight, and emit the structured root outcome; otherwise update progress and continue from where you left off."
	autoResumeConsecutiveCap = 3
	autoResumeAbsoluteCap    = 10
)

// AutoResumeDecision is the provider-agnostic policy result used by every
// resume state-machine adapter.
type AutoResumeDecision struct {
	Governed bool
	Retry    bool
	Wait     time.Duration
	Reason   string
}

// AutoResumeProcess is one provider process observed by AutoResumeEngine.
type AutoResumeProcess struct {
	Session ports.SessionView
	Status  string
	ID      string
}

// AutoResumeAttempt is the result of building, starting, waiting for, and
// accounting one provider continuation.
type AutoResumeAttempt struct {
	Process  AutoResumeProcess
	Rejected bool
	Reason   string
}

// AutoResumeCallbacks adapt phase-specific process construction and
// accounting to the shared resume transition engine.
type AutoResumeCallbacks struct {
	Failed         func(AutoResumeProcess) bool
	SupportsResume func(AutoResumeProcess) bool
	HasCompleted   func(AutoResumeProcess) bool
	ResumeID       func(AutoResumeProcess) string
	WaitBackoff    func(AutoResumeProcess, time.Duration) bool
	Account        func(AutoResumeProcess) error
	Resume         func(AutoResumeProcess, string, int) (AutoResumeAttempt, error)
	FreshFallback  func(AutoResumeProcess, string, int) (AutoResumeAttempt, error)
	Interrupted    func(AutoResumeProcess) bool
}

// AutoResumeResult reports the terminal process selected by AutoResumeEngine.
type AutoResumeResult struct {
	Process     AutoResumeProcess
	Failure     string
	Interrupted bool
}

// AutoResumeState owns attempt accounting shared by the full lifecycle engine
// and adapters that combine provider continuation with another retry protocol.
type AutoResumeState struct {
	totalAttempts           int
	consecutiveIdleAttempts int
}

// Decision classifies the current failed process against shared retry caps.
func (s *AutoResumeState) Decision(sess ports.SessionView, supportsResume bool) AutoResumeDecision {
	return DecideAutoResumeAttempt(sess, supportsResume, s.totalAttempts, s.consecutiveIdleAttempts)
}

// Dispatched charges one provider continuation against the absolute cap.
func (s *AutoResumeState) Dispatched() {
	s.totalAttempts++
}

// Rejected removes an establishment rejection from the attempt budget.
func (s *AutoResumeState) Rejected() {
	if s.totalAttempts > 0 {
		s.totalAttempts--
	}
}

// Observe records whether a failed continuation made enough progress to reset
// the consecutive-idle cap.
func (s *AutoResumeState) Observe(sess ports.SessionView, resumed bool) {
	if !resumed {
		return
	}
	if ResumeSessionMadeProgress(sess) {
		s.consecutiveIdleAttempts = 0
		return
	}
	s.consecutiveIdleAttempts++
}

// AutoResumeEngine owns retry counters and every correctness-sensitive
// transition between failed, resumed, rejected, and fresh provider processes.
type AutoResumeEngine struct{}

// Run continues failed provider processes until one succeeds, policy stops
// retrying, dispatch is interrupted, or the owning artifact becomes complete.
func (AutoResumeEngine) Run(initial AutoResumeProcess, callbacks AutoResumeCallbacks) (AutoResumeResult, error) {
	current := initial
	var state AutoResumeState
	resumeOrdinal := 0
	freshOrdinal := 0

	for callbacks.Failed != nil && callbacks.Failed(current) && !autoResumeCompleted(callbacks, current) {
		supportsResume := callbacks.SupportsResume != nil && callbacks.SupportsResume(current)
		decision := state.Decision(current.Session, supportsResume)
		if !decision.Retry {
			return AutoResumeResult{Process: current, Failure: decision.Reason}, nil
		}
		resumeID := ""
		if callbacks.ResumeID != nil {
			resumeID = callbacks.ResumeID(current)
		}
		if resumeID == "" {
			return AutoResumeResult{Process: current}, nil
		}
		if callbacks.WaitBackoff == nil || !callbacks.WaitBackoff(current, decision.Wait) {
			return AutoResumeResult{Process: current, Interrupted: true}, nil
		}
		if callbacks.Interrupted != nil && callbacks.Interrupted(current) {
			return AutoResumeResult{Process: current, Interrupted: true}, nil
		}
		if callbacks.Account != nil {
			if err := callbacks.Account(current); err != nil {
				return AutoResumeResult{Process: current}, fmt.Errorf("accounting failed provider process: %w", err)
			}
		}

		state.Dispatched()
		resumeOrdinal++
		attempt, err := callbacks.Resume(current, resumeID, resumeOrdinal)
		if err != nil {
			return AutoResumeResult{Process: current}, err
		}
		if attempt.Rejected {
			state.Rejected()
			freshOrdinal++
			fallbackFrom := current
			if attempt.Process.Session != nil {
				fallbackFrom = attempt.Process
			}
			attempt, err = callbacks.FreshFallback(fallbackFrom, attempt.Reason, freshOrdinal)
			if err != nil {
				return AutoResumeResult{Process: current}, err
			}
		}
		current = attempt.Process
		if callbacks.Interrupted != nil && callbacks.Interrupted(current) {
			return AutoResumeResult{Process: current, Interrupted: true}, nil
		}
		if callbacks.Failed != nil && callbacks.Failed(current) {
			state.Observe(current.Session, true)
		}
	}
	return AutoResumeResult{Process: current}, nil
}

func autoResumeCompleted(callbacks AutoResumeCallbacks, process AutoResumeProcess) bool {
	return callbacks.HasCompleted != nil && callbacks.HasCompleted(process)
}

// DecideAutoResumeAttempt applies the shared transient classification, retry
// caps, provider hint, and backoff policy.
func DecideAutoResumeAttempt(sess ports.SessionView, supportsResume bool, totalAttempts, consecutiveIdleAttempts int) AutoResumeDecision {
	if !supportsResume || sess == nil {
		return AutoResumeDecision{}
	}
	classification := sessionruntime.ClassifyFailure(sess)
	switch classification.Tier {
	case sessionruntime.BudgetExhausted:
		return AutoResumeDecision{
			Governed: true,
			Reason:   "provider budget/quota exhausted; resume when the limit resets",
		}
	case sessionruntime.Permanent:
		return AutoResumeDecision{Governed: true}
	}
	if consecutiveIdleAttempts >= autoResumeConsecutiveCap {
		return AutoResumeDecision{
			Governed: true,
			Reason:   "automatic resume exhausted after 3 consecutive attempts without observable progress",
		}
	}
	if totalAttempts >= autoResumeAbsoluteCap {
		return AutoResumeDecision{
			Governed: true,
			Reason:   "automatic resume exhausted after the absolute ceiling of 10 attempts",
		}
	}
	wait := ResumeBackoff(totalAttempts)
	if classification.RetryHint > wait {
		wait = classification.RetryHint
	}
	return AutoResumeDecision{Governed: true, Retry: true, Wait: wait}
}

// ResumeBackoff returns the shared exponential-ish resume delay.
func ResumeBackoff(attempt int) time.Duration {
	switch attempt {
	case 0:
		return 5 * time.Second
	case 1:
		return 20 * time.Second
	default:
		return 60 * time.Second
	}
}

// ResumeEligibilityReason is the closed set of stable reason codes shared by
// resume read models and mutation dispatch.
type ResumeEligibilityReason string

const (
	ResumeReasonNoRecord        ResumeEligibilityReason = "no_resume_record"
	ResumeReasonModelChanged    ResumeEligibilityReason = "model_changed"
	ResumeReasonRunSealed       ResumeEligibilityReason = "run_sealed"
	ResumeReasonSessionRejected ResumeEligibilityReason = "session_rejected"
	ResumeReasonRecordCompleted ResumeEligibilityReason = "record_completed"
	ResumeReasonPositionChanged ResumeEligibilityReason = "position_changed"
	ResumeReasonUnsupported     ResumeEligibilityReason = "resume_unsupported"
)

// ResumeEligibility is a side-effect-free verdict over persisted resume
// identity and the feature's current launch identity.
type ResumeEligibility struct {
	Eligible bool
	Reason   ResumeEligibilityReason
	Message  string
}

// ErrResumeAlreadyClaimed reports that another goroutine owns the feature's
// bookkeeping-plus-dispatch resume window.
var ErrResumeAlreadyClaimed = errors.New("resume already claimed")

var resumePromptTemplate = template.Must(template.New("resume").Parse(resumePromptTemplateText))

var activeResumeClaims = struct {
	sync.Mutex
	units map[resumeClaimKey]struct{}
}{
	units: make(map[resumeClaimKey]struct{}),
}

type resumeClaimKey struct {
	featureID string
	childKey  string
}

// ResumeParentContext identifies the parent unit that owns a child session.
// It is explicit because composite phases do not all use Feature.CurrentPhase
// and CurrentIteration as their durable phase/iteration coordinates.
type ResumeParentContext struct {
	PhaseKey  string
	Iteration int
}

// ResumeRecord describes the provider and orchestrator identity needed to
// continue one resumable unit. It is descriptive state only; phase_complete
// remains the sole completion authority.
type ResumeRecord struct {
	ProviderSessionID     string     `yaml:"provider_session_id,omitempty"`
	Provider              string     `yaml:"provider,omitempty"`
	ResolvedModel         string     `yaml:"resolved_model,omitempty"`
	PhaseKey              string     `yaml:"phase_key"`
	ChildKey              string     `yaml:"child_key,omitempty"`
	Iteration             int        `yaml:"iteration,omitempty"`
	RunNumber             int        `yaml:"run_number"`
	OrchestratorSessionID string     `yaml:"orchestrator_session_id"`
	CreatedAt             time.Time  `yaml:"created_at"`
	UpdatedAt             time.Time  `yaml:"updated_at"`
	Resumed               bool       `yaml:"resumed"`
	ResumeCount           int        `yaml:"resume_count"`
	FreshFallbackCount    int        `yaml:"fresh_fallback_count"`
	FreshFallbackReason   string     `yaml:"fresh_fallback_reason,omitempty"`
	PendingResume         bool       `yaml:"pending_resume,omitempty"`
	Completed             bool       `yaml:"completed"`
	CompletedAt           *time.Time `yaml:"completed_at,omitempty"`
	Rejected              bool       `yaml:"rejected"`
	RejectionReason       string     `yaml:"rejection_reason,omitempty"`
	RejectedAt            *time.Time `yaml:"rejected_at,omitempty"`
}

// ResumeCoordinator owns durable identity, claims, prompting, and retry policy.
// Phase adapters retain their provider-specific launch and accounting hooks.
type ResumeCoordinator struct {
	dir           string
	childKey      string
	parentContext *ResumeParentContext
	mu            sync.Mutex
}

// ResumeClaim owns the in-memory single-resumer slot until Release clears the
// durable pending intent and frees the slot.
type ResumeClaim struct {
	coordinator *ResumeCoordinator
	claimKey    resumeClaimKey
	once        sync.Once
	err         error
}

// DispatchStarted releases the in-memory claim after dispatch has accepted
// the work while leaving the durable intent for the resumed loop to consume.
func (c *ResumeClaim) DispatchStarted() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		releaseResumeClaim(c.claimKey)
	})
}

// NewResumeCoordinator returns a coordinator for the unit stored under dir.
func NewResumeCoordinator(dir string) *ResumeCoordinator {
	return &ResumeCoordinator{dir: dir}
}

// NewChildResumeCoordinator returns a coordinator scoped to one child of a
// resumable parent unit. Child identity participates in both eligibility and
// the in-memory single-resumer claim.
func NewChildResumeCoordinator(dir, childKey string, parentContext ...ResumeParentContext) *ResumeCoordinator {
	coordinator := &ResumeCoordinator{
		dir:      dir,
		childKey: strings.TrimSpace(childKey),
	}
	if len(parentContext) > 0 {
		context := parentContext[0]
		context.PhaseKey = strings.TrimSpace(context.PhaseKey)
		coordinator.parentContext = &context
	}
	return coordinator
}

// ChildKey returns the coordinator's normalized child identity. Parent-unit
// coordinators return an empty key.
func (c *ResumeCoordinator) ChildKey() string {
	if c == nil {
		return ""
	}
	return c.childKey
}

// Prompt renders the shared resume template with phase-specific context.
func (c *ResumeCoordinator) Prompt(phaseContext string) string {
	return renderResumePrompt(phaseContext)
}

// Eligibility evaluates the coordinator's current durable record without
// probing a live session or mutating the sidecar.
func (c *ResumeCoordinator) Eligibility(current *feature.Feature, currentModel string, registry *llm.Registry) ResumeEligibility {
	return c.evaluateEligibility(current, c.Snapshot(), currentModel, registry)
}

// Initialize writes the identity for a fresh provider process. A replay of an
// incomplete iteration replaces stale provider identity; crash-resume dispatch
// does not call Initialize and therefore mutates the same record.
func (c *ResumeCoordinator) Initialize(record ResumeRecord) error {
	return c.update(func(current *ResumeRecord) {
		if current.FreshFallbackCount > record.FreshFallbackCount {
			record.FreshFallbackCount = current.FreshFallbackCount
			record.FreshFallbackReason = current.FreshFallbackReason
		}
		if c.childKey != "" {
			record.ChildKey = c.childKey
		}
		*current = record
	})
}

// CaptureProviderInit fills provider identity learned from the init message.
func (c *ResumeCoordinator) CaptureProviderInit(info ports.ProviderInitInfo) error {
	return c.update(func(record *ResumeRecord) {
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
func (c *ResumeCoordinator) CaptureProviderSnapshot(sessionID, provider, model string) error {
	return c.CaptureProviderInit(ports.ProviderInitInfo{
		SessionID: sessionID,
		Provider:  provider,
		Model:     model,
	})
}

// MarkResumed records one successfully launched continuation.
func (c *ResumeCoordinator) MarkResumed(at time.Time) error {
	return c.update(func(record *ResumeRecord) {
		record.MarkResumed(at)
	})
}

// MarkCompleted records successful completion while retaining the sidecar.
func (c *ResumeCoordinator) MarkCompleted(at time.Time) error {
	return c.update(func(record *ResumeRecord) {
		record.MarkCompleted(at)
	})
}

// MarkRejected records a rejected continuation without changing identity.
func (c *ResumeCoordinator) MarkRejected(reason string, at time.Time) error {
	return c.update(func(record *ResumeRecord) {
		record.MarkRejected(reason, at)
	})
}

// MarkFreshFallback records that provider continuity was skipped and the
// interrupted unit will be relaunched with a fresh provider identity.
func (c *ResumeCoordinator) MarkFreshFallback(reason string, at time.Time) error {
	return c.update(func(record *ResumeRecord) {
		record.FreshFallbackCount++
		record.FreshFallbackReason = reason
		record.PendingResume = false
		record.UpdatedAt = at
	})
}

// ClearPending clears an accepted intent when dispatch cannot start.
func (c *ResumeCoordinator) ClearPending(at time.Time) error {
	return c.update(func(record *ResumeRecord) {
		record.PendingResume = false
		record.UpdatedAt = at
	})
}

// Claim atomically reserves the feature for one resumer, re-reads and
// evaluates the durable record against the feature's claim-time position, and
// stamps the pending-resume intent before dispatch work can begin.
func (c *ResumeCoordinator) Claim(featureID string, current *feature.Feature, currentModel string, registry *llm.Registry, at time.Time) (*ResumeClaim, ResumeEligibility, error) {
	if c == nil {
		return nil, ineligibleResume(ResumeReasonNoRecord), nil
	}
	featureID = strings.TrimSpace(featureID)
	if featureID == "" {
		return nil, ResumeEligibility{}, fmt.Errorf("claiming resume: empty feature ID")
	}
	claimKey := resumeClaimKey{featureID: featureID, childKey: c.ChildKey()}
	if !tryAcquireResumeClaim(claimKey) {
		return nil, ResumeEligibility{}, ErrResumeAlreadyClaimed
	}
	release := true
	defer func() {
		if release {
			releaseResumeClaim(claimKey)
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()
	record, err := ReadResumeRecord(c.dir)
	if err != nil {
		return nil, ResumeEligibility{}, fmt.Errorf("claiming resume: %w", err)
	}
	eligibility := c.evaluateEligibility(current, record, currentModel, registry)
	if !eligibility.Eligible {
		if record != nil && record.PendingResume {
			record.PendingResume = false
			record.UpdatedAt = at
			if err := WriteResumeRecord(c.dir, *record); err != nil {
				return nil, ResumeEligibility{}, fmt.Errorf("clearing ineligible resume intent: %w", err)
			}
		}
		return nil, eligibility, nil
	}
	record.PendingResume = true
	record.UpdatedAt = at
	if err := WriteResumeRecord(c.dir, *record); err != nil {
		return nil, ResumeEligibility{}, fmt.Errorf("writing pending resume intent: %w", err)
	}

	release = false
	return &ResumeClaim{
		coordinator: c,
		claimKey:    claimKey,
	}, eligibility, nil
}

// Release clears the durable intent before making the feature available to a
// later claimant. It fails closed: the in-memory claim is only freed once the
// durable PendingResume flag is cleared, so a persistence error never leaves a
// stale durable intent owned by nobody while a second claimant is admitted.
// It is safe to call more than once; after a failure, later calls return the
// stored error and the in-memory claim stays held.
func (c *ResumeClaim) Release(at time.Time) error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		if c.coordinator != nil {
			c.coordinator.mu.Lock()
			record, err := ReadResumeRecord(c.coordinator.dir)
			if err == nil && record != nil && record.PendingResume {
				record.PendingResume = false
				record.UpdatedAt = at
				err = WriteResumeRecord(c.coordinator.dir, *record)
			}
			c.coordinator.mu.Unlock()
			if err != nil {
				c.err = fmt.Errorf("clearing pending resume intent: %w", err)
			}
		}
		if c.err == nil {
			releaseResumeClaim(c.claimKey)
		}
	})
	return c.err
}

// Reject records a rejected continuation and clears the durable intent in one
// write while the claim is still owned, freeing the in-memory claim only after
// that mutation is durable. Persisting the rejection ahead of the claim
// release is structural: no second claimant can be admitted, or stamp its own
// pending intent, in an interval where the rejection is not yet durable. On
// persistence failure the claim stays held so the unpersisted rejection cannot
// interleave with a later claimant. It is safe to call more than once; after a
// failure, later calls return the stored error and the in-memory claim stays
// held.
func (c *ResumeClaim) Reject(reason string, at time.Time) error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		if c.coordinator == nil {
			releaseResumeClaim(c.claimKey)
			return
		}
		c.coordinator.mu.Lock()
		record, err := ReadResumeRecord(c.coordinator.dir)
		if err == nil {
			if record == nil {
				record = &ResumeRecord{}
			}
			record.MarkRejected(reason, at)
			err = WriteResumeRecord(c.coordinator.dir, *record)
		}
		c.coordinator.mu.Unlock()
		if err != nil {
			c.err = fmt.Errorf("recording resume rejection: %w", err)
		}
		if c.err == nil {
			releaseResumeClaim(c.claimKey)
		}
	})
	return c.err
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

func (c *ResumeCoordinator) update(mutate func(*ResumeRecord)) error {
	if c == nil {
		return fmt.Errorf("updating resume record: nil coordinator")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	record, err := ReadResumeRecord(c.dir)
	if err != nil {
		return fmt.Errorf("reading resume record for update: %w", err)
	}
	if record == nil {
		record = &ResumeRecord{}
	}
	mutate(record)
	if err := WriteResumeRecord(c.dir, *record); err != nil {
		return fmt.Errorf("writing resume record update: %w", err)
	}
	return nil
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

func (c *ResumeCoordinator) evaluateEligibility(current *feature.Feature, record *ResumeRecord, currentModel string, registry *llm.Registry) ResumeEligibility {
	if c == nil || c.ChildKey() == "" {
		return EvaluateResumeEligibility(current, record, currentModel, registry)
	}
	if c.parentContext != nil {
		return EvaluateChildResumeEligibility(
			current, record, currentModel, registry, *c.parentContext, c.ChildKey(),
		)
	}
	return EvaluateResumeEligibility(current, record, currentModel, registry, c.ChildKey())
}

// EvaluateResumeEligibility compares only provider-session identity. Mutable
// launch parameters such as effort, sandboxing, and prompt templates are not
// inputs and therefore cannot make an otherwise matching record ineligible.
func EvaluateResumeEligibility(current *feature.Feature, record *ResumeRecord, currentModel string, registry *llm.Registry, childKey ...string) ResumeEligibility {
	context := ResumeParentContext{}
	expectedChildKey := ""
	if current != nil {
		context.PhaseKey = ResumePhaseKey(current)
	}
	if len(childKey) > 0 {
		expectedChildKey = strings.TrimSpace(childKey[0])
		if current != nil {
			context.Iteration = current.CurrentIteration
		}
	}
	return evaluateResumeEligibility(current, record, currentModel, registry, context, expectedChildKey)
}

// EvaluateChildResumeEligibility evaluates a child against the dispatching
// composite unit's explicit durable position.
func EvaluateChildResumeEligibility(current *feature.Feature, record *ResumeRecord, currentModel string, registry *llm.Registry, parent ResumeParentContext, childKey string) ResumeEligibility {
	parent.PhaseKey = strings.TrimSpace(parent.PhaseKey)
	return evaluateResumeEligibility(
		current, record, currentModel, registry, parent, strings.TrimSpace(childKey),
	)
}

// EvaluateCompositeResumeEligibility reports whether at least one incomplete
// child under unitDir can resume at the composite unit's current durable
// position. Each child is evaluated with its own recorded identity.
func EvaluateCompositeResumeEligibility(
	unitDir string,
	current *feature.Feature,
	registry *llm.Registry,
	parent ResumeParentContext,
	modelForChild func(string) string,
) ResumeEligibility {
	best := ineligibleResume(ResumeReasonNoRecord)
	rootSidecar := filepath.Join(unitDir, ResumeSidecarFile)
	_ = filepath.WalkDir(unitDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != ResumeSidecarFile || path == rootSidecar {
			return nil
		}
		record, readErr := ReadResumeRecord(filepath.Dir(path))
		if readErr != nil || record == nil || strings.TrimSpace(record.ChildKey) == "" {
			return nil
		}
		model := ""
		if modelForChild != nil {
			model = modelForChild(record.ChildKey)
		}
		eligibility := EvaluateChildResumeEligibility(
			current, record, model, registry, parent, record.ChildKey,
		)
		if eligibility.Eligible {
			best = eligibility
			return filepath.SkipAll
		}
		if best.Reason == ResumeReasonNoRecord || best.Reason == ResumeReasonRecordCompleted {
			best = eligibility
		}
		return nil
	})
	return best
}

func evaluateResumeEligibility(current *feature.Feature, record *ResumeRecord, currentModel string, registry *llm.Registry, parent ResumeParentContext, expectedChildKey string) ResumeEligibility {
	if record == nil {
		return ineligibleResume(ResumeReasonNoRecord)
	}
	if record.Completed {
		return ineligibleResume(ResumeReasonRecordCompleted)
	}
	if record.Rejected {
		return ineligibleResume(ResumeReasonSessionRejected)
	}
	if strings.TrimSpace(record.ProviderSessionID) == "" {
		return ineligibleResume(ResumeReasonNoRecord)
	}
	if current == nil || record.RunNumber != activeRunNumber(current) {
		return ineligibleResume(ResumeReasonRunSealed)
	}
	if parent.PhaseKey == "" || record.PhaseKey != parent.PhaseKey {
		return ineligibleResume(ResumeReasonPositionChanged)
	}
	if record.ChildKey != expectedChildKey {
		return ineligibleResume(ResumeReasonPositionChanged)
	}
	if expectedChildKey != "" && record.Iteration != parent.Iteration {
		return ineligibleResume(ResumeReasonPositionChanged)
	}
	if registry == nil {
		return ineligibleResume(ResumeReasonModelChanged)
	}

	recordedProvider, recordedModel, err := registry.ResolveModel(record.Provider + ":" + record.ResolvedModel)
	if err != nil {
		return ineligibleResume(ResumeReasonModelChanged)
	}
	configuredProvider, configuredModel, err := registry.ResolveModel(currentModel)
	if err != nil ||
		recordedProvider.Name() != configuredProvider.Name() ||
		recordedModel != configuredModel {
		return ineligibleResume(ResumeReasonModelChanged)
	}
	resumer, ok := recordedProvider.(sessionResumeProvider)
	if !ok || !resumer.SupportsSessionResume() {
		return ineligibleResume(ResumeReasonUnsupported)
	}
	return ResumeEligibility{Eligible: true}
}

func ineligibleResume(reason ResumeEligibilityReason) ResumeEligibility {
	return ResumeEligibility{
		Reason:  reason,
		Message: resumeEligibilityMessage(reason),
	}
}

func resumeEligibilityMessage(reason ResumeEligibilityReason) string {
	switch reason {
	case ResumeReasonNoRecord:
		return "no resume record"
	case ResumeReasonModelChanged:
		return "model changed"
	case ResumeReasonRunSealed:
		return "run sealed"
	case ResumeReasonSessionRejected:
		return "session previously rejected"
	case ResumeReasonRecordCompleted:
		return "record completed"
	case ResumeReasonPositionChanged:
		return "feature position changed"
	case ResumeReasonUnsupported:
		return "provider does not support session resume"
	default:
		return "resume unavailable"
	}
}

// ResumePhaseKey returns the durable phase identity for the feature's current
// resumable unit. It is independent of the unit's iteration or attempt number.
func ResumePhaseKey(current *feature.Feature) string {
	if current == nil {
		return ""
	}
	switch current.CurrentPhase {
	case feature.PhaseKnowledgeBase:
		return feature.PhaseKnowledgeBase.DirName()
	case feature.PhaseInquire:
		return "inquire"
	case feature.PhaseResearch:
		return "research"
	case feature.PhaseDesign:
		return "design"
	case feature.PhasePlan:
		if current.CurrentRoadmapPhase > 0 {
			if key := strings.TrimSpace(current.ActiveTimingKey); strings.HasSuffix(key, "-plan") {
				return key
			}
			return fmt.Sprintf("phase-%d-plan", current.CurrentRoadmapPhase)
		}
		return "roadmap-plan"
	case feature.PhaseImplement:
		if key := strings.TrimSpace(current.ActiveTimingKey); key == "implement" || strings.HasSuffix(key, "-impl") {
			return key
		}
		if current.CurrentRoadmapPhase > 0 {
			return fmt.Sprintf("phase-%d-impl", current.CurrentRoadmapPhase)
		}
		return "implement"
	default:
		return ""
	}
}

// ResumeUnitDir resolves the directory that owns resume.yaml for the feature's
// current resumable unit. Planning sidecars belong to the next logical attempt;
// failed or interrupted attempts are retried because
// LatestCompletedPlanAttempt deliberately excludes them.
func ResumeUnitDir(stateDir string, current *feature.Feature) (string, bool) {
	if current == nil || strings.TrimSpace(current.ID) == "" {
		return "", false
	}

	runDir := ActiveRunDir(stateDir, current)
	// No cycle/refactor path prefix applies: those subsystems were deleted and
	// their prefixes are treated as empty.
	baseDir := runDir
	switch current.CurrentPhase {
	case feature.PhaseInquire, feature.PhaseResearch, feature.PhaseDesign:
		return filepath.Join(baseDir, current.CurrentPhase.DirName()), true
	case feature.PhasePlan:
		var planDir string
		if current.CurrentRoadmapPhase > 0 {
			planDir = filepath.Join(
				baseDir,
				fmt.Sprintf("phase-%02d", current.CurrentRoadmapPhase),
				"plan",
			)
		} else {
			planDir = filepath.Join(baseDir, "roadmap")
		}
		attempt := LatestCompletedPlanAttempt(planDir) + 1
		return filepath.Join(planDir, fmt.Sprintf("attempt-%02d", attempt)), true
	case feature.PhaseImplement:
		if current.CurrentIteration <= 0 {
			return "", false
		}
		return filepath.Join(
			ActiveImplementDir(stateDir, current),
			fmt.Sprintf("iteration-%02d", current.CurrentIteration),
		), true
	case feature.PhaseReview, feature.PhaseFinalReview:
		if current.ReviewIteration <= 0 {
			return "", false
		}
		reviewDir := filepath.Join(runDir, feature.PhaseFinalReview.DirName())
		return filepath.Join(
			reviewDir,
			fmt.Sprintf("iteration-%02d", current.ReviewIteration),
		), true
	default:
		return "", false
	}
}

func tryAcquireResumeClaim(key resumeClaimKey) bool {
	activeResumeClaims.Lock()
	defer activeResumeClaims.Unlock()
	if _, exists := activeResumeClaims.units[key]; exists {
		return false
	}
	activeResumeClaims.units[key] = struct{}{}
	return true
}

func releaseResumeClaim(key resumeClaimKey) {
	activeResumeClaims.Lock()
	delete(activeResumeClaims.units, key)
	activeResumeClaims.Unlock()
}

// MarkResumed increments the continuation count without changing identity.
func (r *ResumeRecord) MarkResumed(at time.Time) {
	r.Resumed = true
	r.ResumeCount++
	r.PendingResume = false
	r.UpdatedAt = at
}

// MarkCompleted stamps successful completion without changing identity.
func (r *ResumeRecord) MarkCompleted(at time.Time) {
	r.Completed = true
	r.CompletedAt = timePtr(at)
	r.PendingResume = false
	r.UpdatedAt = at
}

// MarkRejected records why a resume attempt was rejected without changing identity.
func (r *ResumeRecord) MarkRejected(reason string, at time.Time) {
	r.Rejected = true
	r.RejectionReason = reason
	r.RejectedAt = timePtr(at)
	r.PendingResume = false
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
