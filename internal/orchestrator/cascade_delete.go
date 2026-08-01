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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

type cascadeDeleteStore interface {
	BeginCascadeDelete(parentID string, now time.Time) (*feature.CascadeDeleteIntent, error)
	LoadCascadeDelete(parentID string) (*feature.CascadeDeleteIntent, error)
	SaveCascadeDelete(parentID string, intent *feature.CascadeDeleteIntent) error
}

func (o *Orchestrator) cascadeOwnsRelationship(parentID string) (bool, error) {
	journals, ok := o.deps.Store.(cascadeDeleteStore)
	if !ok {
		return false, nil
	}
	_, err := journals.LoadCascadeDelete(parentID)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("loading cascade ownership for %s: %w", parentID, err)
}

type cascadeWorktreeRemover interface {
	RemoveRef(worktreePath, mainRepo, branch string) error
}

// ReconcileCascadeDeletes resumes every discoverable durable delete intent.
// Attention is a convergent outcome, not a startup failure; material journal
// read/write failures fail recovery closed.
func (o *Orchestrator) ReconcileCascadeDeletes() error {
	if o.deps.Store == nil {
		return nil
	}
	journals, ok := o.deps.Store.(cascadeDeleteStore)
	if !ok {
		return nil
	}
	features, err := o.deps.Store.List()
	if err != nil {
		return fmt.Errorf("listing cascade delete intents: %w", err)
	}
	for _, f := range features {
		if f == nil || f.IsChild() {
			continue
		}
		if _, err := journals.LoadCascadeDelete(f.ID); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("loading cascade delete for %s: %w", f.ID, err)
		}
		if _, err := o.DeleteCascade(f.ID); err != nil {
			return fmt.Errorf("resuming cascade delete for %s: %w", f.ID, err)
		}
	}
	return nil
}

// DeleteCascade resumes or begins the single durable delete operation for a
// parent relationship. The relationship write lock excludes child creation
// and guarded relationship mutations for the operation's complete lifetime.
func (o *Orchestrator) DeleteCascade(featureID string) (feature.CascadeDeleteResult, error) {
	o.relationshipMu.Lock()
	defer o.relationshipMu.Unlock()

	// Release any KB locks the feature still owns before its durable record
	// disappears, so queued KB waiters are woken rather than orphaned.
	if o.deps.Lifecycle != nil {
		if f, err := o.deps.Lifecycle.Get(featureID); err == nil {
			o.releaseKBLocksForFeature(f)
		}
	}

	journals, ok := o.deps.Store.(cascadeDeleteStore)
	if !ok {
		// Keep small unit-test and embedding implementations source-compatible.
		// Production wiring always supplies *feature.Store.
		if err := o.deleteWithoutCascade(featureID); err != nil {
			return feature.CascadeDeleteResult{}, err
		}
		return completedCascadeResult(featureID), nil
	}

	var before *feature.CascadeDeleteIntent
	if existing, loadErr := journals.LoadCascadeDelete(featureID); loadErr == nil {
		before = existing
	}
	intent, err := journals.BeginCascadeDelete(featureID, time.Now())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return completedCascadeResult(featureID), nil
		}
		return feature.CascadeDeleteResult{}, fmt.Errorf("persisting cascade delete intent: %w", err)
	}
	defer o.emitCascadeProgressIfChanged(journals, before, intent)
	result := cascadeResult(intent)

	if intent.Step == feature.CascadeStepIntentPersisted {
		if err := o.quiesceCascadeSessions(intent); err != nil {
			return o.parkCascadeCleanup(journals, intent, "session_quiesce_failed", err)
		}
		intent.Step = feature.CascadeStepSessionsQuiesced
		if err := journals.SaveCascadeDelete(featureID, intent); err != nil {
			return result, fmt.Errorf("recording quiesced cascade sessions: %w", err)
		}
	}

	if intent.Step == feature.CascadeStepSessionsQuiesced {
		if err := o.settleCascadeAttention(intent); err != nil {
			return o.parkCascadeCleanup(journals, intent, "attention_settle_failed", err)
		}
		intent.Step = feature.CascadeStepAttentionSettled
		if err := journals.SaveCascadeDelete(featureID, intent); err != nil {
			return result, fmt.Errorf("recording settled cascade attention: %w", err)
		}
	}

	if intent.Step == feature.CascadeStepAttentionSettled ||
		intent.Status == feature.CascadeDeleteAttentionRequired {
		safe, classifyErr := o.classifyCascadeRefs(journals, intent)
		if classifyErr != nil {
			return cascadeResult(intent), classifyErr
		}
		if !safe {
			return cascadeResult(intent), nil
		}
		intent.Step = feature.CascadeStepRefsSafe
		intent.Status = feature.CascadeDeleteCleanupPending
		if err := journals.SaveCascadeDelete(featureID, intent); err != nil {
			return result, fmt.Errorf("recording safe cascade refs: %w", err)
		}
	}

	if intent.Step == feature.CascadeStepRefsSafe {
		if err := o.cleanupCascadeResources(journals, intent); err != nil {
			return o.parkCascadeCleanup(journals, intent, "resource_cleanup_failed", err)
		}
		intent.Step = feature.CascadeStepResourcesCleaned
		if err := journals.SaveCascadeDelete(featureID, intent); err != nil {
			return result, fmt.Errorf("recording cascade resource cleanup: %w", err)
		}
	}

	if intent.Step == feature.CascadeStepResourcesCleaned {
		for _, childID := range intent.ChildIDs {
			if err := o.deps.Store.Delete(childID); err != nil {
				return o.parkCascadeCleanup(journals, intent, "child_record_delete_failed",
					fmt.Errorf("%s: %w", childID, err))
			}
			markCascadeRecordDone(intent, childID)
			if err := journals.SaveCascadeDelete(featureID, intent); err != nil {
				return result, fmt.Errorf("recording child deletion %s: %w", childID, err)
			}
		}
		intent.Step = feature.CascadeStepChildrenDeleted
		if err := journals.SaveCascadeDelete(featureID, intent); err != nil {
			return result, fmt.Errorf("recording cascade child deletions: %w", err)
		}
	}

	// Parent deletion is deliberately last. It atomically removes feature.yaml
	// and the cascade journal together, leaving no post-delete state to revive.
	if err := o.deps.Store.Delete(featureID); err != nil {
		return o.parkCascadeCleanup(journals, intent, "parent_record_delete_failed", err)
	}
	for _, childID := range intent.ChildIDs {
		o.emitEvent(ports.Event{
			Type:      ports.RelationshipCascadeDeleted,
			FeatureID: featureID,
			ParentID:  featureID,
			ChildID:   childID,
			Message:   "relationship cascade deleted",
		})
	}
	return completedCascadeResult(featureID), nil
}

func (o *Orchestrator) emitCascadeProgressIfChanged(
	journals cascadeDeleteStore,
	before, attempted *feature.CascadeDeleteIntent,
) {
	current, err := journals.LoadCascadeDelete(attempted.ParentID)
	if err != nil ||
		(current.Status != feature.CascadeDeleteCleanupPending &&
			current.Status != feature.CascadeDeleteAttentionRequired) ||
		reflect.DeepEqual(before, current) {
		return
	}
	for _, childID := range current.ChildIDs {
		o.emitEvent(ports.Event{
			Type:      ports.RelationshipCascadeProgress,
			FeatureID: current.ParentID,
			ParentID:  current.ParentID,
			ChildID:   childID,
			Message:   "relationship cascade " + string(current.Status),
		})
	}
}

func (o *Orchestrator) deleteWithoutCascade(featureID string) error {
	o.StopFeatureSessions(featureID)
	if o.deps.Lifecycle != nil {
		if err := o.deps.Lifecycle.Delete(featureID); err != nil {
			return fmt.Errorf("deleting feature: %w", err)
		}
		return nil
	}
	if o.deps.Store == nil {
		return errors.New("feature store is not configured")
	}
	return o.deps.Store.Delete(featureID)
}

func (o *Orchestrator) quiesceCascadeSessions(intent *feature.CascadeDeleteIntent) error {
	if o.deps.Sessions == nil {
		return nil
	}
	ids := append([]string{intent.ParentID}, intent.ChildIDs...)
	for _, id := range ids {
		sessions := o.deps.Sessions.FeatureSessions(id)
		for _, session := range sessions {
			if session != nil && session.IsActive() {
				if err := o.deps.Sessions.StopSession(session.ID()); err != nil {
					return fmt.Errorf("stopping session %s: %w", session.ID(), err)
				}
			}
		}
		for _, session := range sessions {
			if session != nil {
				session.Wait()
			}
		}
	}
	return nil
}

func (o *Orchestrator) settleCascadeAttention(intent *feature.CascadeDeleteIntent) error {
	ids := append([]string{intent.ParentID}, intent.ChildIDs...)
	for _, id := range ids {
		err := o.deps.Store.Modify(id, func(f *feature.Feature) error {
			if f.IsChild() && !f.IsActiveChild() {
				return nil
			}
			for i := range f.PermissionsQueue {
				f.PermissionsQueue[i].Pending = false
			}
			for i := range f.HelpQueue {
				f.HelpQueue[i].Pending = false
			}
			f.PendingNeedUserInputPath = ""
			f.ReviewingGate = false
			f.ValidatingPlan = false
			for _, cycle := range f.Run().RepoCycles {
				if cycle != nil {
					cycle.PendingNeedUserInputPath = ""
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
	}
	return nil
}

func (o *Orchestrator) classifyCascadeRefs(
	journals cascadeDeleteStore,
	intent *feature.CascadeDeleteIntent,
) (bool, error) {
	intent.Diagnostics = nil
	if len(intent.Refs) == 0 {
		return true, nil
	}
	cas, ok := o.deps.Worktrees.(refCASOperator)
	if !ok {
		intent.Status = feature.CascadeDeleteAttentionRequired
		intent.Diagnostics = append(intent.Diagnostics, feature.CascadeDiagnostic{
			Code: "ref_safety_unavailable", Message: "ref safety operations are not configured",
		})
		if err := journals.SaveCascadeDelete(intent.ParentID, intent); err != nil {
			return false, fmt.Errorf("recording unavailable ref safety: %w", err)
		}
		return false, nil
	}

	safe := true
	for i := range intent.Refs {
		ref := &intent.Refs[i]
		ref.Safe = false
		ref.Restored = false
		ref.Diagnostic = ""
		observed, err := cas.RefSHA(ref.RepoPath, ref.Ref)
		ref.ObservedSHA = observed
		if err != nil {
			safe = false
			o.addCascadeRefDiagnostic(intent, ref, "ref_read_failed", err.Error())
			continue
		}
		switch observed {
		case ref.AnchorSHA:
			ref.Safe = true
		case ref.CandidateSHA:
			if err := cas.UpdateRef(ref.RepoPath, ref.Ref, ref.CandidateSHA, ref.AnchorSHA); err != nil {
				latest, _ := cas.RefSHA(ref.RepoPath, ref.Ref)
				ref.ObservedSHA = latest
				safe = false
				o.addCascadeRefDiagnostic(intent, ref, "ref_restore_failed", err.Error())
				continue
			}
			ref.ObservedSHA = ref.AnchorSHA
			ref.Safe = true
			ref.Restored = true
		default:
			safe = false
			o.addCascadeRefDiagnostic(intent, ref, "external_ref_moved",
				"candidate-bearing parent ref moved externally; delete will not overwrite it")
		}
		if err := journals.SaveCascadeDelete(intent.ParentID, intent); err != nil {
			return false, fmt.Errorf("recording cascade ref classification: %w", err)
		}
	}
	if !safe {
		intent.Status = feature.CascadeDeleteAttentionRequired
		if err := journals.SaveCascadeDelete(intent.ParentID, intent); err != nil {
			return false, fmt.Errorf("recording cascade ref attention: %w", err)
		}
	}
	return safe, nil
}

func (o *Orchestrator) addCascadeRefDiagnostic(
	intent *feature.CascadeDeleteIntent,
	ref *feature.CascadeRef,
	code, message string,
) {
	ref.Diagnostic = message
	intent.Diagnostics = append(intent.Diagnostics, feature.CascadeDiagnostic{
		Code: code, Message: message, Repo: ref.Repo, Ref: ref.Ref,
		AnchorSHA: ref.AnchorSHA, CandidateSHA: ref.CandidateSHA,
		ObservedSHA: ref.ObservedSHA,
	})
}

func (o *Orchestrator) cleanupCascadeResources(
	journals cascadeDeleteStore,
	intent *feature.CascadeDeleteIntent,
) error {
	intent.Diagnostics = nil
	for i := range intent.Resources {
		resource := &intent.Resources[i]
		if resource.Done || resource.Kind == feature.CascadeResourceRecord {
			continue
		}
		var err error
		switch resource.Kind {
		case feature.CascadeResourceCopiedInput:
			if !pathWithin(filepath.Join(o.stateDir(), resource.OwnerID), resource.Path) {
				err = fmt.Errorf("refusing copied-input path outside feature state: %s", resource.Path)
			} else {
				err = os.RemoveAll(resource.Path)
			}
		case feature.CascadeResourceOverlay:
			expected := feature.ParentOverlayPath(o.stateDir(), intent.ParentID, resource.Repo)
			if filepath.Clean(resource.Path) != filepath.Clean(expected) {
				err = fmt.Errorf("refusing unexpected overlay path %s", resource.Path)
			} else {
				err = os.RemoveAll(resource.Path)
			}
		case feature.CascadeResourceWorktree:
			if remover, ok := o.deps.Worktrees.(cascadeWorktreeRemover); ok {
				err = remover.RemoveRef(resource.Path, resource.RepoPath, resource.Branch)
			} else if o.deps.Worktrees != nil {
				err = o.deps.Worktrees.Remove(resource.Path, true)
			} else {
				err = errors.New("worktree cleanup is not configured")
			}
		case feature.CascadeResourceBranch:
			if remover, ok := o.deps.Worktrees.(cascadeWorktreeRemover); ok {
				err = remover.RemoveRef(worktreePathForResource(intent, resource), resource.RepoPath, resource.Branch)
			} else if worktreeResourceDone(intent, resource) {
				// Standard Remove(path,true) already removed the paired branch.
			} else {
				err = errors.New("durable branch cleanup is not configured")
			}
		case feature.CascadeResourceKBWorkspace:
			expected := feature.ChildKBWorkspaceDir(o.stateDir(), resource.OwnerID, resource.Repo)
			if filepath.Clean(resource.Path) != filepath.Clean(expected) {
				err = fmt.Errorf("refusing unexpected KB workspace path %s", resource.Path)
			} else {
				err = os.RemoveAll(resource.Path)
			}
		case feature.CascadeResourcePromotion:
			if !pathWithin(filepath.Join(o.stateDir(), resource.OwnerID), resource.Path) {
				err = fmt.Errorf("refusing promotion path outside feature state: %s", resource.Path)
			} else {
				_ = os.Remove(resource.Path)
				err = nil
			}
		default:
			err = fmt.Errorf("unknown cascade resource kind %q", resource.Kind)
		}
		if err != nil {
			resource.Error = err.Error()
			return fmt.Errorf("%s: %w", resource.ID, err)
		}
		resource.Done = true
		resource.Error = ""
		if err := journals.SaveCascadeDelete(intent.ParentID, intent); err != nil {
			return fmt.Errorf("recording resource %s cleanup: %w", resource.ID, err)
		}
	}
	return nil
}

func (o *Orchestrator) parkCascadeCleanup(
	journals cascadeDeleteStore,
	intent *feature.CascadeDeleteIntent,
	code string,
	cause error,
) (feature.CascadeDeleteResult, error) {
	intent.Status = feature.CascadeDeleteCleanupPending
	intent.Diagnostics = []feature.CascadeDiagnostic{{Code: code, Message: cause.Error()}}
	if err := journals.SaveCascadeDelete(intent.ParentID, intent); err != nil {
		return cascadeResult(intent), fmt.Errorf("%v; persisting cleanup failure: %w", cause, err)
	}
	return cascadeResult(intent), nil
}

func cascadeResult(intent *feature.CascadeDeleteIntent) feature.CascadeDeleteResult {
	return feature.CascadeDeleteResult{
		OperationID: intent.OperationID, ParentID: intent.ParentID,
		Status: intent.Status, Diagnostics: intent.Diagnostics,
	}
}

func completedCascadeResult(parentID string) feature.CascadeDeleteResult {
	return feature.CascadeDeleteResult{
		OperationID: "cascade:" + parentID, ParentID: parentID,
		Status: feature.CascadeDeleteCompleted,
	}
}

func markCascadeRecordDone(intent *feature.CascadeDeleteIntent, ownerID string) {
	for i := range intent.Resources {
		if intent.Resources[i].Kind == feature.CascadeResourceRecord &&
			intent.Resources[i].OwnerID == ownerID {
			intent.Resources[i].Done = true
		}
	}
}

func worktreePathForResource(intent *feature.CascadeDeleteIntent, branch *feature.CascadeResource) string {
	for i := range intent.Resources {
		candidate := &intent.Resources[i]
		if candidate.Kind == feature.CascadeResourceWorktree &&
			candidate.OwnerID == branch.OwnerID && candidate.Repo == branch.Repo {
			return candidate.Path
		}
	}
	return filepath.Join(filepath.Dir(intent.Resources[len(intent.Resources)-1].Path), "absent-worktree")
}

func worktreeResourceDone(intent *feature.CascadeDeleteIntent, branch *feature.CascadeResource) bool {
	for i := range intent.Resources {
		candidate := &intent.Resources[i]
		if candidate.Kind == feature.CascadeResourceWorktree &&
			candidate.OwnerID == branch.OwnerID && candidate.Repo == branch.Repo {
			return candidate.Done
		}
	}
	return false
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	return err == nil && rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
