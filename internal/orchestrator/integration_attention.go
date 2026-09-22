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

package orchestrator

import (
	"errors"
	"fmt"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// This file owns the single integration-attention parking boundary. Every
// condition that parks a child pass's integration classifies here into one
// stored canonical record on the transaction journal: a needs_action catalog
// code, one repositories context block listing every affected repository
// with its branch, conflict files, dirty files, and SHAs, and raw
// diagnostics as one line per repository.

// integrationRepoContext is the structured per-repository context a parking
// site contributes to the attention record's repositories block. Each site
// fills the fields it knows; the rest stay empty. Per-ref detail (branch,
// anchor, candidate, observed SHAs of the individual layer refs) lives in
// the raw diagnostics lines, which name the ref.
type integrationRepoContext struct {
	Name          string
	Branch        string
	ConflictFiles []string
	DirtyFiles    []string
	ChildHeadSHA  string
	CandidateSHA  string
	ObservedSHA   string
	// CommitSHA and Attempts carry the exhausted conflict-resolution facts
	// for the rebase-conflict park: the commit whose replay conflicted and
	// the resolution attempt count.
	CommitSHA string
	Attempts  int
}

// block converts the context into the record's repositories-block entry.
func (c integrationRepoContext) block() errcat.CodeRepository {
	return errcat.CodeRepository{
		Name:          c.Name,
		Branch:        c.Branch,
		ConflictFiles: c.ConflictFiles,
		DirtyFiles:    c.DirtyFiles,
		ChildHeadSHA:  c.ChildHeadSHA,
		CandidateSHA:  c.CandidateSHA,
		ObservedSHA:   c.ObservedSHA,
		CommitSHA:     c.CommitSHA,
		Attempts:      c.Attempts,
	}
}

// repoContextFromEntry derives the block context from a journal entry's
// durable progress state: name, the top ref's branch and SHAs — or, for an
// append-preparation entry before staging records its created refs, the
// previous top's branch and tip — and the child head. Conflict and dirty
// files are contributed by the parking site, which observed them.
func repoContextFromEntry(entry *feature.RepoTransactionEntry) integrationRepoContext {
	if entry == nil {
		return integrationRepoContext{}
	}
	ctx := integrationRepoContext{
		Name:         entry.Repo,
		ChildHeadSHA: entry.ChildHeadSHA,
	}
	if top := entry.TopRef(); top != nil {
		ctx.Branch = top.Branch
		ctx.CandidateSHA = top.CandidateSHA
		ctx.ObservedSHA = top.ObservedSHA
	} else if prev := entry.PreviousTopRef(); prev != nil {
		ctx.Branch = prev.Branch
		ctx.ObservedSHA = prev.TipSHA
	}
	return ctx
}

// integrationFinding is one affected repository's classification at a
// parking boundary: the repositories-block context, the catalog code, and
// the raw one-line diagnostics.
type integrationFinding struct {
	ctx         integrationRepoContext
	code        errcat.Code
	diagnostics string
}

// entryFinding attaches a classification to the repository context recorded
// in the transaction journal.
func entryFinding(entry *feature.RepoTransactionEntry, code errcat.Code, diagnostics string) integrationFinding {
	return integrationFinding{
		ctx:         repoContextFromEntry(entry),
		code:        code,
		diagnostics: diagnostics,
	}
}

// refUpdateFinding classifies every failed transactional ref update at the
// workflow boundary, including the compare-and-swap race subtype.
func refUpdateFinding(entry *feature.RepoTransactionEntry, ref string, err error) integrationFinding {
	var casErr *git.RefCASMismatchError
	if errors.As(err, &casErr) {
		return entryFinding(entry, errcat.IntegrationRefRace,
			fmt.Sprintf("ref %s expected %s observed %s", ref, casErr.Expected, casErr.Observed))
	}
	return entryFinding(entry, errcat.IntegrationCandidateFailed,
		fmt.Sprintf("updating ref %s for repo %s: %v", ref, entry.Repo, err))
}

// line renders the finding's raw diagnostics as one per-repository line.
func (f integrationFinding) line() string {
	if f.ctx.Name == "" {
		return f.diagnostics
	}
	return f.ctx.Name + ": " + f.diagnostics
}

// findingsRecord builds the stored attention record from a finding list: the
// first finding's code classifies the park (the existing precedence — drift
// before dirty during preparation, the first failing repository during apply
// and rollback), every finding's repository joins the block, and the raw
// diagnostics carry one line per repository.
func findingsRecord(findings []integrationFinding) *errcat.FailureRecord {
	if len(findings) == 0 {
		return nil
	}
	code := findings[0].code
	repos := make([]errcat.CodeRepository, 0, len(findings))
	lines := make([]string, 0, len(findings))
	for _, finding := range findings {
		repos = append(repos, finding.ctx.block())
		lines = append(lines, finding.line())
	}
	return &errcat.FailureRecord{
		Code: code,
		Context: &errcat.RecordContext{
			Repositories: repos,
		},
		Diagnostics: strings.Join(lines, "\n"),
	}
}

// parkIntegrationAttention stores the finding list's canonical record on the
// journal, persists the journal, and emits the relationship integration
// event carrying the rendered canonical error. The caller sets the journal
// phase first — attention for every park, applied for a closure-time
// worktree sync failure whose recovery semantics stay unchanged — plus any
// per-entry apply states.
func (o *Orchestrator) parkIntegrationAttention(child *feature.Feature, journal *feature.TransactionJournal, findings []integrationFinding) error {
	record := findingsRecord(findings)
	if record == nil {
		return fmt.Errorf("parking integration attention without findings")
	}
	journal.Attention = record
	if err := o.persistTransaction(child.ID, journal); err != nil {
		return fmt.Errorf("recording integration attention: %w", err)
	}
	return o.emitTransactionAttention(child, record)
}

// dirtyFileList flattens a categorized cleanliness report into the record's
// dirty_files list, matching the launch-time dirty-parent error's
// repositories block.
func dirtyFileList(staged, unstaged, untracked []string) []string {
	files := make([]string, 0, len(staged)+len(unstaged)+len(untracked))
	files = append(files, staged...)
	files = append(files, unstaged...)
	files = append(files, untracked...)
	return files
}

// joinFileList renders a file list for one raw-diagnostics line.
func joinFileList(files []string) string {
	return strings.Join(files, ", ")
}

// emitTransactionAttention notifies consumers that the child's transaction
// is parked, carrying the rendered canonical error so the read model and SSE
// layer never re-synthesize attention text.
func (o *Orchestrator) emitTransactionAttention(child *feature.Feature, record *errcat.FailureRecord) error {
	rendered := errcat.RenderRecord(*record)
	o.emitEvent(ports.Event{
		Type:           ports.RelationshipIntegrationChanged,
		FeatureID:      child.ID,
		ParentID:       child.Parent.ParentID,
		ChildID:        child.ID,
		Message:        "child integration needs attention: " + rendered.Title,
		CanonicalError: &rendered,
	})
	return nil
}
