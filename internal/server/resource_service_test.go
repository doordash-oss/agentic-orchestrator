package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func seedResourceFeature(t *testing.T) (*feature.Store, *feature.Feature) {
	t.Helper()
	store := feature.NewStore(t.TempDir())
	f := &feature.Feature{
		ID:           "feat-res-test",
		Name:         "Resource Test",
		Status:       feature.StatusImplementing,
		CurrentPhase: feature.PhaseImplement,
		ActiveRun:    1,
		RunCount:     1,
		Models: config.ModelConfig{
			Inquiry:        "codex:gpt-5.6-sol",
			Research:       "codex:gpt-5.6-terra",
			Planning:       "codex:gpt-5.6-sol",
			Implementation: "opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2",
			Review:         "claude:fable",
			Utilities:      "claude:sonnet",
			KBBuild:        "opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2",
		},
		Inquireness:        "medium",
		Pipeline:           feature.PipelineMoonshot,
		InputNotifications: "default",
	}
	f.SetRun(&feature.Run{RunNumber: 1})
	if err := store.Save(f); err != nil {
		t.Fatalf("save feature: %v", err)
	}
	return store, f
}

func seedResourceTree(t *testing.T, runtimeDir string) {
	t.Helper()
	skillsDir := filepath.Join(runtimeDir, "skills")
	guidelinesDir := filepath.Join(runtimeDir, "guidelines")
	skillContent := "---\nname: test-skill\ndescription: A test skill\n---\n# Test Skill\n\nBody text.\n"
	skillPath := filepath.Join(skillsDir, "test-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(skillPath, []byte(skillContent), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	companionContent := "# User Guide\n\nCompanion file.\n"
	companionPath := filepath.Join(skillsDir, "test-skill", "user-guide.md")
	if err := os.WriteFile(companionPath, []byte(companionContent), 0o644); err != nil {
		t.Fatalf("write companion: %v", err)
	}
	guideContent := "---\nname: go\ndescription: Go guidelines\n---\n# Go Guidelines\n\nContent.\n"
	guidePath := filepath.Join(guidelinesDir, "go", "index.md")
	if err := os.MkdirAll(filepath.Dir(guidePath), 0o755); err != nil {
		t.Fatalf("mkdir guideline: %v", err)
	}
	if err := os.WriteFile(guidePath, []byte(guideContent), 0o644); err != nil {
		t.Fatalf("write guideline: %v", err)
	}
}

func newTestResourceService(t *testing.T) (*resourceService, *feature.Store, string) {
	t.Helper()
	return newTestResourceServiceWithRegistry(t, nil)
}

func findTestSkillID(t *testing.T, svc *resourceService) string {
	t.Helper()
	catalogue, err := svc.Catalogue("skill")
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	for _, e := range catalogue.Resources {
		if strings.HasSuffix(e.Label, "SKILL.md") || strings.Contains(e.Label, "test-skill") {
			return e.ID
		}
	}
	t.Fatal("could not find skill entry in catalogue")
	return ""
}

func TestResourceCatalogueIncludesAllKinds(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	resp, err := svc.Catalogue("")
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	kinds := map[ResourceKind]bool{}
	for _, e := range resp.Resources {
		kinds[e.Kind] = true
		if e.ID == "" || e.Kind == "" || e.Label == "" || e.Revision == "" {
			t.Fatalf("entry missing required field: %+v", e)
		}
	}
	for _, kind := range []ResourceKind{ResourceKindFeatureConfig, ResourceKindRuntimeConfig, ResourceKindSkill, ResourceKindGuideline} {
		if !kinds[kind] {
			t.Fatalf("catalogue missing kind %s", kind)
		}
	}
}

func TestResourceCatalogueKindFilter(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	resp, err := svc.Catalogue("skill")
	if err != nil {
		t.Fatalf("Catalogue: %v", err)
	}
	for _, e := range resp.Resources {
		if e.Kind != ResourceKindSkill {
			t.Fatalf("expected only skill entries, got %s", e.Kind)
		}
	}
	if len(resp.Resources) == 0 {
		t.Fatal("expected at least one skill entry")
	}
}

func TestResourceReadReturnsContent(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	skillID := findTestSkillID(t, svc)
	resp, err := svc.Read(skillID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if resp.Text == "" {
		t.Fatal("Read returned empty text")
	}
	if resp.Revision == "" {
		t.Fatal("Read returned empty revision")
	}
	if resp.ContentType != Markdown {
		t.Fatalf("expected markdown content type, got %s", resp.ContentType)
	}
}

func TestResourceValidateSkillFrontmatter(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	skillID := findTestSkillID(t, svc)
	invalidText := "# Missing frontmatter\n\nNo frontmatter here."
	resp, err := svc.Validate(skillID, invalidText)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resp.Valid {
		t.Fatal("expected invalid for missing frontmatter")
	}
	if len(resp.Findings) == 0 {
		t.Fatal("expected findings for missing frontmatter")
	}
}

func TestResourceWriteWithCorrectRevision(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	skillID := findTestSkillID(t, svc)
	readResp, err := svc.Read(skillID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	newText := "---\nname: test-skill\ndescription: An updated test skill\n---\n# Updated\n\nNew body.\n"
	writeResp, err := svc.Write(skillID, readResp.Revision, newText)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if writeResp.Type != Saved {
		t.Fatalf("expected saved, got %s", writeResp.Type)
	}
	if writeResp.Revision == "" {
		t.Fatal("Write returned empty revision")
	}
	readAgain, err := svc.Read(skillID)
	if err != nil {
		t.Fatalf("Read after write: %v", err)
	}
	if readAgain.Text != newText {
		t.Fatal("Read after write did not return the written content")
	}
}

func TestResourceWriteStaleRevisionReturnsConflict(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	skillID := findTestSkillID(t, svc)
	writeResp, err := svc.Write(skillID, "sha256:stale-revision", "---\nname: test-skill\ndescription: Stale\n---\n# Stale\n")
	if err != nil {
		t.Fatalf("Write with stale revision should not error: %v", err)
	}
	if writeResp.Type != Conflict {
		t.Fatalf("expected conflict, got %s", writeResp.Type)
	}
	if writeResp.CurrentRevision == "" {
		t.Fatal("conflict response missing current_revision")
	}
	if writeResp.CurrentText == "" {
		t.Fatal("conflict response missing current_text")
	}
}

func TestResourceWriteConcurrentSerialization(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	skillID := findTestSkillID(t, svc)
	readResp, err := svc.Read(skillID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	const writers = 8
	start := make(chan struct{})
	results := make(chan ResourceWriteResponse, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			text := "---\nname: test-skill\ndescription: Writer " + string(rune('A'+i)) + "\n---\n# Writer\n"
			resp, err := svc.Write(skillID, readResp.Revision, text)
			if err != nil {
				results <- ResourceWriteResponse{Type: "error"}
				return
			}
			results <- resp
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	saved := 0
	conflicted := 0
	for resp := range results {
		switch resp.Type {
		case Saved:
			saved++
		case Conflict:
			conflicted++
		default:
			t.Fatalf("unexpected write response type: %s", resp.Type)
		}
	}
	if saved != 1 {
		t.Fatalf("expected 1 saved, got %d", saved)
	}
	if conflicted != writers-1 {
		t.Fatalf("expected %d conflicts, got %d", writers-1, conflicted)
	}
}

func TestResourceWriteValidationFailure(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	skillID := findTestSkillID(t, svc)
	readResp, err := svc.Read(skillID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	invalidText := "# Missing frontmatter entirely"
	_, err = svc.Write(skillID, readResp.Revision, invalidText)
	if err == nil {
		t.Fatal("expected validation error for invalid frontmatter")
	}
	var validationErr *ValidationFailedError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected ValidationFailedError, got %T: %v", err, err)
	}
	if len(validationErr.Findings) == 0 {
		t.Fatal("expected validation findings in error")
	}
}

func TestResourceReadUnknownIDReturnsError(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	_, err := svc.Read("r-nonexistent12345")
	if err == nil {
		t.Fatal("expected error for unknown resource id")
	}
	var notFound *ResourceNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected ResourceNotFoundError, got %T: %v", err, err)
	}
}

func TestResourceCatalogueExcludesStampFiles(t *testing.T) {
	svc, _, _ := newTestResourceService(t)
	skillsDir := filepath.Join(svc.runtime.RuntimeDir, "skills")
	stampPath := filepath.Join(skillsDir, "test-skill", ".agentico-stamp")
	if err := os.WriteFile(stampPath, []byte("stamp"), 0o644); err != nil {
		t.Fatalf("write stamp file: %v", err)
	}
	tmpPath := filepath.Join(skillsDir, "test-skill", "draft.tmp")
	if err := os.WriteFile(tmpPath, []byte("temp"), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	for _, e := range mustCatalogue(t, svc, "skill") {
		if strings.Contains(e.Label, "stamp") || strings.Contains(e.Label, ".tmp") {
			t.Fatalf("catalogue should exclude stamp/temp files, got %s", e.Label)
		}
	}
}

func mustCatalogue(t *testing.T, svc *resourceService, kind string) []ResourceEntry {
	t.Helper()
	resp, err := svc.Catalogue(kind)
	if err != nil {
		t.Fatalf("Catalogue(%s): %v", kind, err)
	}
	return resp.Resources
}

func TestResourceWriteResponseJSON(t *testing.T) {
	saved := ResourceWriteResponse{
		APIVersion: "v1",
		Type:       Saved,
		ID:         "r-test",
		Revision:   "sha256:abc",
		Effect:     NextSession,
	}
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed["type"] != "saved" {
		t.Fatalf("expected type=saved, got %v", parsed["type"])
	}
	if parsed["revision"] != "sha256:abc" {
		t.Fatalf("expected revision, got %v", parsed["revision"])
	}

	conflict := ResourceWriteResponse{
		APIVersion:       "v1",
		Type:             Conflict,
		ID:               "r-test",
		ExpectedRevision: "sha256:old",
		CurrentRevision:  "sha256:new",
		CurrentText:      "current content",
	}
	data2, err := json.Marshal(conflict)
	if err != nil {
		t.Fatalf("marshal conflict: %v", err)
	}
	var parsed2 map[string]interface{}
	if err := json.Unmarshal(data2, &parsed2); err != nil {
		t.Fatalf("unmarshal conflict: %v", err)
	}
	if parsed2["type"] != "conflict" {
		t.Fatalf("expected type=conflict, got %v", parsed2["type"])
	}
	if parsed2["current_text"] != "current content" {
		t.Fatalf("expected current_text, got %v", parsed2["current_text"])
	}
}

func newTestResourceServiceWithRegistry(t *testing.T, registry *llm.Registry) (*resourceService, *feature.Store, string) {
	t.Helper()
	store, f := seedResourceFeature(t)
	runtimeDir := t.TempDir()
	seedResourceTree(t, runtimeDir)
	cfg := config.NewDefault()
	cfg.Defaults.Models = f.Models
	cfg.Defaults.Inquireness = "medium"
	cfg.Defaults.Pipeline = "moonshot"
	cfg.WorkspaceRoots = []string{}
	svc := newResourceService(
		store,
		func() *config.Config { return cfg },
		registry,
		nil,
		RuntimeIdentity{RuntimeDir: runtimeDir, StateDir: filepath.Join(runtimeDir, "state"), Config: filepath.Join(runtimeDir, "config.yaml")},
	)
	return svc, store, f.ID
}

func findFeatureConfigID(t *testing.T, svc *resourceService, featureID string) string {
	t.Helper()
	cat, err := svc.Catalogue("feature_config")
	if err != nil {
		t.Fatalf("Catalogue(feature_config): %v", err)
	}
	for _, e := range cat.Resources {
		if e.FeatureID == featureID {
			return e.ID
		}
	}
	t.Fatalf("could not find feature_config entry for %s", featureID)
	return ""
}

func TestResourceValidateModelEligibility(t *testing.T) {
	registry := llm.NewRegistry()
	registry.Register(fakeProvider{name: "claude", models: []string{"sonnet", "opus"}})
	registry.Register(fakeProvider{name: "codex", models: []string{"gpt-5.6-sol"}})

	svc, _, featureID := newTestResourceServiceWithRegistry(t, registry)
	configID := findFeatureConfigID(t, svc, featureID)

	// Read the current config text.
	readResp, err := svc.Read(configID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Construct a config where every model field is valid against the
	// fake registry.  Validating it must yield zero model_unavailable
	// findings.  This discriminates the false-positive path: the old
	// naive prefix strip could flag valid provider:model values when
	// the stripped name did not appear in the flattened cross-provider
	// set (e.g. aliases, colon-tagged catalogue ids).
	validText := readResp.Text
	validText = strings.ReplaceAll(validText, "codex:gpt-5.6-terra", "codex:gpt-5.6-sol")
	validText = strings.ReplaceAll(validText, "opencode:portkey/@fireworks/accounts/fireworks/models/glm-5p2", "claude:sonnet")
	validText = strings.ReplaceAll(validText, "claude:fable", "claude:sonnet")
	validResp, err := svc.Validate(configID, validText)
	if err != nil {
		t.Fatalf("Validate (valid): %v", err)
	}
	for _, f := range validResp.Findings {
		if f.Code == "model_unavailable" {
			t.Fatalf("expected zero model_unavailable findings for valid config, got: %+v", f)
		}
	}

	// A model that exists under a different provider (codex:sonnet —
	// sonnet is a claude model) must be flagged.  The old code discarded
	// the provider half and checked the stripped name against a flattened
	// cross-provider set, so "sonnet" would be found and the wrong-
	// provider pairing would pass silently.  The fix resolves through the
	// per-provider catalogue and catches it.
	wrongProviderText := strings.Replace(validText, "review: claude:sonnet", "review: codex:sonnet", 1)
	if wrongProviderText == validText {
		t.Fatal("test setup: could not replace review model in valid text")
	}
	wpResp, err := svc.Validate(configID, wrongProviderText)
	if err != nil {
		t.Fatalf("Validate (wrong-provider): %v", err)
	}
	var wpFound bool
	for _, f := range wpResp.Findings {
		if f.Code == "model_unavailable" && f.Field == "models.review" {
			wpFound = true
			break
		}
	}
	if !wpFound {
		t.Fatalf("expected model_unavailable for codex:sonnet (wrong provider), got: %+v", wpResp.Findings)
	}

	// Swap the review model to one not in any provider's catalogue.
	invalidText := strings.Replace(readResp.Text, "claude:fable", "claude:bogus-model", 1)
	if invalidText == readResp.Text {
		t.Fatal("test setup: could not replace review model in config text")
	}
	resp, err := svc.Validate(configID, invalidText)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if resp.Valid {
		t.Fatal("expected invalid for unavailable model")
	}
	var found bool
	for _, f := range resp.Findings {
		if f.Code == "model_unavailable" && f.Field == "models.review" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected model_unavailable finding for models.review, got: %+v", resp.Findings)
	}
}
