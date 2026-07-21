package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

const (
	maxResourceBytes       = 1024 * 1024
	maxResourceCatalogue   = 5000
	skillDirName           = "skills"
	guidelineDirName       = "guidelines"
)

type resourceDescriptor struct {
	id          string
	kind        ResourceKind
	key         string
	label       string
	contentType ResourceContentType
	effect      ResourceEffect
	validatable bool
	hierarchy   []string
	featureID   string
	filePath    string
}

type ValidationFailedError struct {
	Message  string
	Findings []ResourceFinding
}

func (e *ValidationFailedError) Error() string {
	if e == nil {
		return "content failed validation"
	}
	if e.Message == "" {
		return "content failed validation"
	}
	return e.Message
}

type ResourceNotFoundError struct {
	Message string
}

func (e *ResourceNotFoundError) Error() string {
	if e == nil || e.Message == "" {
		return "resource not found"
	}
	return e.Message
}

type resourceService struct {
	store     FeatureReader
	cfg       func() *config.Config
	registry  *llm.Registry
	mutations MutationTarget
	runtime   RuntimeIdentity
	locks     *resourceLockSet
	now       func() time.Time
}

type resourceLockSet struct {
	locks sync.Map
}

func newResourceLockSet() *resourceLockSet {
	return &resourceLockSet{}
}

func (s *resourceLockSet) lock(key string) func() {
	if s == nil {
		return func() {}
	}
	value, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func newResourceService(
	store FeatureReader,
	cfgFn func() *config.Config,
	registry *llm.Registry,
	mutations MutationTarget,
	runtime RuntimeIdentity,
) *resourceService {
	return &resourceService{
		store:     store,
		cfg:       cfgFn,
		registry:  registry,
		mutations: mutations,
		runtime:   runtime,
		locks:     newResourceLockSet(),
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func resourceID(kind ResourceKind, key string) string {
	h := sha256.Sum256([]byte(string(kind) + "\x00" + key))
	return "r-" + hex.EncodeToString(h[:])[:16]
}

func (s *resourceService) Catalogue(kindFilter string) (ResourceCatalogResponse, error) {
	entries := []ResourceEntry{}

	if kindFilter == "" || kindFilter == string(ResourceKindFeatureConfig) {
		fcEntries, err := s.catalogueFeatureConfigs()
		if err != nil {
			return ResourceCatalogResponse{}, err
		}
		entries = append(entries, fcEntries...)
	}

	if kindFilter == "" || kindFilter == string(ResourceKindRuntimeConfig) {
		if entry, ok := s.catalogueRuntimeConfig(); ok {
			entries = append(entries, entry)
		}
	}

	if kindFilter == "" || kindFilter == string(ResourceKindSkill) {
		skEntries, err := s.catalogueSkills()
		if err != nil {
			return ResourceCatalogResponse{}, err
		}
		entries = append(entries, skEntries...)
	}

	if kindFilter == "" || kindFilter == string(ResourceKindGuideline) {
		glEntries, err := s.catalogueGuidelines()
		if err != nil {
			return ResourceCatalogResponse{}, err
		}
		entries = append(entries, glEntries...)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return string(entries[i].Kind) < string(entries[j].Kind)
		}
		return entries[i].Label < entries[j].Label
	})

	truncated := false
	if len(entries) > maxResourceCatalogue {
		entries = entries[:maxResourceCatalogue]
		truncated = true
	}

	return ResourceCatalogResponse{
		APIVersion: APIVersion,
		Resources:  entries,
		Truncated:  truncated,
	}, nil
}

func (s *resourceService) catalogueFeatureConfigs() ([]ResourceEntry, error) {
	if s.store == nil {
		return nil, nil
	}
	features, err := s.store.List()
	if err != nil {
		return nil, fmt.Errorf("list features for resource catalogue: %w", err)
	}
	entries := make([]ResourceEntry, 0, len(features))
	for _, f := range features {
		key := f.ID
		id := resourceID(ResourceKindFeatureConfig, key)
		revision, err := s.featureConfigRevision(f.ID)
		if err != nil {
			continue
		}
		entries = append(entries, ResourceEntry{
			ID:          id,
			Kind:        ResourceKindFeatureConfig,
			Label:       fmt.Sprintf("%s — Configuration", f.Name),
			ContentType: Yaml,
			Revision:    revision,
			Effect:      NextDispatch,
			Validatable: true,
			Hierarchy:   []string{"Features", f.Name, "Configuration"},
			FeatureID:   f.ID,
		})
	}
	return entries, nil
}

func (s *resourceService) catalogueRuntimeConfig() (ResourceEntry, bool) {
	if s.cfg == nil {
		return ResourceEntry{}, false
	}
	id := resourceID(ResourceKindRuntimeConfig, "runtime")
	text, err := s.runtimeConfigYAML()
	if err != nil {
		return ResourceEntry{}, false
	}
	return ResourceEntry{
		ID:          id,
		Kind:        ResourceKindRuntimeConfig,
		Label:       "Runtime Configuration",
		ContentType: Yaml,
		Revision:    textRevision([]byte(text)),
		Effect:      NextDispatch,
		Validatable: true,
		Hierarchy:   []string{"Runtime", "Configuration"},
	}, true
}

func (s *resourceService) catalogueSkills() ([]ResourceEntry, error) {
	skillsDir := s.skillsDir()
	if skillsDir == "" {
		return nil, nil
	}
	return s.walkManagedTree(skillsDir, ResourceKindSkill, "Skills")
}

func (s *resourceService) catalogueGuidelines() ([]ResourceEntry, error) {
	guidelinesDir := s.guidelinesDir()
	if guidelinesDir == "" {
		return nil, nil
	}
	return s.walkManagedTree(guidelinesDir, ResourceKindGuideline, "Guidelines")
}

// errFound is a sentinel returned by walkManagedFiles callbacks to stop the
// tree walk as soon as a target file is located.
var errFound = errors.New("resource found")

// walkManagedFiles walks root and calls visit for each file that passes the
// path-based eligibility checks (not a directory, not excluded, not a symlink,
// within the size limit, and has a computable relative path). The descriptor
// passed to visit has all path-derived fields populated but no content read;
// visit must read the file itself if it needs content or a revision. Returning
// errFound from visit stops the walk immediately.
func walkManagedFiles(root string, kind ResourceKind, rootLabel string, visit func(resourceDescriptor) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if isExcludedFile(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if isSymlink(info) || info.Size() > maxResourceBytes {
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)
		desc := resourceDescriptor{
			id:          resourceID(kind, relPath),
			kind:        kind,
			key:         relPath,
			label:       fileLabel(rootLabel, relPath),
			contentType: contentTypeForFile(path),
			effect:      NextSession,
			validatable: isEntryFile(path),
			hierarchy:   buildHierarchy(rootLabel, relPath),
			filePath:    path,
		}
		return visit(desc)
	})
}

func (s *resourceService) walkManagedTree(root string, kind ResourceKind, rootLabel string) ([]ResourceEntry, error) {
	var entries []ResourceEntry
	err := walkManagedFiles(root, kind, rootLabel, func(desc resourceDescriptor) error {
		data, err := os.ReadFile(desc.filePath)
		if err != nil || !isValidUTF8(data) {
			return nil
		}
		entries = append(entries, ResourceEntry{
			ID:          desc.id,
			Kind:        desc.kind,
			Label:       desc.label,
			ContentType: desc.contentType,
			Revision:    textRevision(data),
			Effect:      desc.effect,
			Validatable: desc.validatable,
			Hierarchy:   desc.hierarchy,
		})
		return nil
	})
	if err != nil && !errors.Is(err, errFound) {
		return nil, fmt.Errorf("walk %s: %w", rootLabel, err)
	}
	return entries, nil
}

func (s *resourceService) Read(resourceID string) (ResourceReadResponse, error) {
	desc, err := s.resolveResource(resourceID)
	if err != nil {
		return ResourceReadResponse{}, err
	}
	text, revision, err := s.readContent(desc)
	if err != nil {
		return ResourceReadResponse{}, err
	}
	return ResourceReadResponse{
		APIVersion:  APIVersion,
		ID:          desc.id,
		Kind:        desc.kind,
		Label:       desc.label,
		ContentType: desc.contentType,
		Revision:    revision,
		Text:        text,
		Effect:      desc.effect,
		Validatable: desc.validatable,
		Hierarchy:   desc.hierarchy,
		FeatureID:   desc.featureID,
	}, nil
}

func (s *resourceService) Validate(resourceID string, text string) (ResourceValidateResponse, error) {
	desc, err := s.resolveResource(resourceID)
	if err != nil {
		return ResourceValidateResponse{}, err
	}
	if len(text) > maxResourceBytes {
		return ResourceValidateResponse{
			APIVersion: APIVersion,
			ID:         desc.id,
			Valid:      false,
			Revision:   "",
			Findings: []ResourceFinding{{
				Code:    "too_large",
				Message: fmt.Sprintf("Content exceeds the %d-byte limit.", maxResourceBytes),
			}},
		}, nil
	}
	findings := s.validateContent(desc, text)
	currentRevision, err := s.currentRevision(desc)
	if err != nil {
		return ResourceValidateResponse{}, err
	}
	return ResourceValidateResponse{
		APIVersion: APIVersion,
		ID:         desc.id,
		Valid:      len(findings) == 0,
		Revision:   currentRevision,
		Findings:   findings,
	}, nil
}

func (s *resourceService) Write(resourceID string, baseRevision string, text string) (ResourceWriteResponse, error) {
	desc, err := s.resolveResource(resourceID)
	if err != nil {
		return ResourceWriteResponse{}, err
	}
	if len(text) > maxResourceBytes {
		return ResourceWriteResponse{}, &ActionConflictError{
			Message: "content exceeds the size limit",
			Target:  map[string]any{"max_bytes": maxResourceBytes},
		}
	}

	unlock := s.locks.lock(resourceID)
	defer unlock()

	currentRevision, err := s.currentRevision(desc)
	if err != nil {
		return ResourceWriteResponse{}, err
	}
	if baseRevision != currentRevision {
		currentText, _ := s.readText(desc)
		return ResourceWriteResponse{
			APIVersion:       APIVersion,
			Type:             Conflict,
			ID:               desc.id,
			ExpectedRevision: baseRevision,
			CurrentRevision:  currentRevision,
			CurrentText:      currentText,
		}, nil
	}

	findings := s.validateContent(desc, text)
	if len(findings) > 0 {
		return ResourceWriteResponse{}, &ValidationFailedError{
			Message:  "content failed validation",
			Findings: findings,
		}
	}

	newRevision, err := s.writeContent(desc, text)
	if err != nil {
		return ResourceWriteResponse{}, err
	}

	return ResourceWriteResponse{
		APIVersion: APIVersion,
		Type:       Saved,
		ID:         desc.id,
		Revision:   newRevision,
		Effect:     desc.effect,
	}, nil
}

func (s *resourceService) resolveResource(id string) (resourceDescriptor, error) {
	if id == "" || len(id) > 256 {
		return resourceDescriptor{}, &ResourceNotFoundError{
			Message: "invalid resource id",
		}
	}

	if s.store != nil {
		features, err := s.store.List()
		if err == nil {
			for _, f := range features {
				key := f.ID
				rid := resourceID(ResourceKindFeatureConfig, key)
				if rid == id {
					return s.featureConfigDescriptor(f), nil
				}
			}
		}
	}

	if s.cfg != nil {
		rid := resourceID(ResourceKindRuntimeConfig, "runtime")
		if rid == id {
			return s.runtimeConfigDescriptor(), nil
		}
	}

	if d, ok := s.findInTree(id, s.skillsDir(), ResourceKindSkill, "Skills"); ok {
		return d, nil
	}
	if d, ok := s.findInTree(id, s.guidelinesDir(), ResourceKindGuideline, "Guidelines"); ok {
		return d, nil
	}

	return resourceDescriptor{}, &ResourceNotFoundError{
		Message: "resource not found",
	}
}

func (s *resourceService) findInTree(id string, root string, kind ResourceKind, rootLabel string) (resourceDescriptor, bool) {
	if root == "" {
		return resourceDescriptor{}, false
	}
	var found resourceDescriptor
	err := walkManagedFiles(root, kind, rootLabel, func(desc resourceDescriptor) error {
		if desc.id != id {
			return nil
		}
		data, err := os.ReadFile(desc.filePath)
		if err != nil || !isValidUTF8(data) {
			return nil
		}
		found = desc
		return errFound
	})
	if err != nil && !errors.Is(err, errFound) {
		return resourceDescriptor{}, false
	}
	return found, found.filePath != ""
}

func (s *resourceService) readContent(desc resourceDescriptor) (string, string, error) {
	text, err := s.readText(desc)
	if err != nil {
		return "", "", err
	}
	return text, textRevision([]byte(text)), nil
}

func (s *resourceService) readText(desc resourceDescriptor) (string, error) {
	switch desc.kind {
	case ResourceKindFeatureConfig:
		return s.featureConfigYAML(desc.featureID)
	case ResourceKindRuntimeConfig:
		return s.runtimeConfigYAML()
	case ResourceKindSkill, ResourceKindGuideline:
		data, err := os.ReadFile(desc.filePath)
		if err != nil {
			return "", &ResourceNotFoundError{
				Message: "resource file is not readable",
			}
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("unknown resource kind: %s", desc.kind)
	}
}

func (s *resourceService) currentRevision(desc resourceDescriptor) (string, error) {
	text, err := s.readText(desc)
	if err != nil {
		return "", err
	}
	return textRevision([]byte(text)), nil
}

func (s *resourceService) writeContent(desc resourceDescriptor, text string) (string, error) {
	switch desc.kind {
	case ResourceKindFeatureConfig:
		return s.writeFeatureConfig(desc.featureID, text)
	case ResourceKindRuntimeConfig:
		return s.writeRuntimeConfig(text)
	case ResourceKindSkill, ResourceKindGuideline:
		return s.writeFile(desc.filePath, text)
	default:
		return "", fmt.Errorf("unknown resource kind: %s", desc.kind)
	}
}

func (s *resourceService) writeFile(filePath string, text string) (string, error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("ensure resource directory: %w", err)
	}
	temp := filePath + ".tmp-" + fmt.Sprintf("%d", os.Getpid())
	if err := os.WriteFile(temp, []byte(text), 0o644); err != nil {
		_ = os.Remove(temp)
		return "", fmt.Errorf("write resource temp file: %w", err)
	}
	if err := os.Rename(temp, filePath); err != nil {
		_ = os.Remove(temp)
		return "", fmt.Errorf("replace resource file: %w", err)
	}
	return textRevision([]byte(text)), nil
}

func (s *resourceService) writeFeatureConfig(featureID string, text string) (string, error) {
	if s.mutations == nil {
		return "", fmt.Errorf("mutation service unavailable")
	}
	var cfg featureConfigYAML
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return "", &ActionConflictError{
			Message: "invalid feature config YAML: " + err.Error(),
			Target:  map[string]any{"feature_id": featureID},
		}
	}
	req := FeatureConfigMutationRequest{
		Models:             cfg.Models.toModelConfig(),
		Inquireness:        cfg.Inquireness,
		Checkpoints:        cfg.Checkpoints,
		Pipeline:           cfg.Pipeline,
		InputNotifications: cfg.InputNotifications,
	}
	if _, err := s.mutations.UpdateFeatureConfig(featureID, req); err != nil {
		return "", err
	}
	newText, err := s.featureConfigYAML(featureID)
	if err != nil {
		return "", fmt.Errorf("re-read feature config after mutation: %w", err)
	}
	return textRevision([]byte(newText)), nil
}

func (s *resourceService) writeRuntimeConfig(text string) (string, error) {
	if s.mutations == nil {
		return "", fmt.Errorf("mutation service unavailable")
	}
	var cfg runtimeConfigYAML
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return "", &ActionConflictError{
			Message: "invalid runtime config YAML: " + err.Error(),
		}
	}
	req := RuntimeConfigMutationRequest{
		Defaults: cfg.Defaults.toRuntimeDefaultsMutation(),
	}
	if cfg.WorkspaceRoots != nil {
		req.WorkspaceRoots = &cfg.WorkspaceRoots
	}
	if cfg.Notifications != nil {
		req.Notifications = cfg.Notifications.toNotificationDTO()
	}
	if _, err := s.mutations.RuntimeConfig(req); err != nil {
		return "", err
	}
	newText, err := s.runtimeConfigYAML()
	if err != nil {
		return "", fmt.Errorf("re-read runtime config after mutation: %w", err)
	}
	return textRevision([]byte(newText)), nil
}

func (s *resourceService) validateContent(desc resourceDescriptor, text string) []ResourceFinding {
	switch desc.kind {
	case ResourceKindFeatureConfig:
		return s.validateFeatureConfig(desc.featureID, text)
	case ResourceKindRuntimeConfig:
		return s.validateRuntimeConfig(text)
	case ResourceKindSkill:
		return s.validateSkillFile(desc, text)
	case ResourceKindGuideline:
		return s.validateGuidelineFile(desc, text)
	default:
		return nil
	}
}

func (s *resourceService) validateFeatureConfig(featureID string, text string) []ResourceFinding {
	var cfg featureConfigYAML
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return []ResourceFinding{{
			Code:    "invalid_yaml",
			Message: "Feature configuration YAML is invalid: " + err.Error(),
		}}
	}
	var findings []ResourceFinding
	if cfg.Pipeline != "" {
		if !isValidPipeline(string(cfg.Pipeline)) {
			findings = append(findings, ResourceFinding{
				Code:    "invalid_pipeline",
				Field:   "pipeline",
				Message: fmt.Sprintf("Unknown pipeline %q. Valid values are defined by the server pipeline catalogue.", cfg.Pipeline),
			})
		}
	}
	if cfg.Inquireness != "" {
		if !isValidInquireness(cfg.Inquireness) {
			findings = append(findings, ResourceFinding{
				Code:    "invalid_inquireness",
				Field:   "inquireness",
				Message: fmt.Sprintf("Unknown inquireness level %q.", cfg.Inquireness),
			})
		}
	}
	if cfg.InputNotifications != "" {
		if !isValidInputNotifications(cfg.InputNotifications) {
			findings = append(findings, ResourceFinding{
				Code:    "invalid_input_notifications",
				Field:   "input_notifications",
				Message: fmt.Sprintf("Unknown input notification policy %q. Use default, enabled, or muted.", cfg.InputNotifications),
			})
		}
	}
	if s.registry != nil {
		findings = append(findings, s.validateModelEligibility(cfg.Models)...)
	}
	return findings
}

func (s *resourceService) validateRuntimeConfig(text string) []ResourceFinding {
	var cfg runtimeConfigYAML
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		return []ResourceFinding{{
			Code:    "invalid_yaml",
			Message: "Runtime configuration YAML is invalid: " + err.Error(),
		}}
	}
	var findings []ResourceFinding
	if cfg.Defaults.Pipeline != "" {
		if !isValidPipeline(cfg.Defaults.Pipeline) {
			findings = append(findings, ResourceFinding{
				Code:    "invalid_pipeline",
				Field:   "defaults.pipeline",
				Message: fmt.Sprintf("Unknown pipeline %q.", cfg.Defaults.Pipeline),
			})
		}
	}
	if cfg.Defaults.Inquireness != "" {
		if !isValidInquireness(cfg.Defaults.Inquireness) {
			findings = append(findings, ResourceFinding{
				Code:    "invalid_inquireness",
				Field:   "defaults.inquireness",
				Message: fmt.Sprintf("Unknown inquireness level %q.", cfg.Defaults.Inquireness),
			})
		}
	}
	if cfg.WorkspaceRoots != nil {
		for i, root := range cfg.WorkspaceRoots {
			if root == "" {
				findings = append(findings, ResourceFinding{
					Code:    "empty_workspace_root",
					Field:   fmt.Sprintf("workspace_roots[%d]", i),
					Message: "Workspace root paths must not be empty.",
				})
			}
		}
	}
	return findings
}

func (s *resourceService) validateSkillFile(desc resourceDescriptor, text string) []ResourceFinding {
	if !isEntryFile(desc.filePath) {
		return nil
	}
	return validateFrontmatter(text, "skill", []string{"description"})
}

func (s *resourceService) validateGuidelineFile(desc resourceDescriptor, text string) []ResourceFinding {
	if !isEntryFile(desc.filePath) {
		return nil
	}
	return validateFrontmatter(text, "guideline", []string{"name", "description"})
}

func (s *resourceService) validateModelEligibility(models modelDefaultsYAML) []ResourceFinding {
	var findings []ResourceFinding
	check := func(phase, model string) {
		if model == "" {
			return
		}
		// Resolve through the canonical path that dispatch itself uses
		// (Registry.ResolveModel), so provider prefixes, alias
		// canonicalization, and bare colon-tagged catalogue ids are all
		// handled identically to runtime dispatch.
		provider, canonical, err := s.registry.ResolveModel(model)
		eligible := err == nil
		if eligible {
			// ResolveModel accepts an explicit provider prefix even when
			// the model is not in that provider's catalogue.  Verify the
			// resolved model is actually available under the resolved
			// provider so a valid name under the wrong provider is caught.
			for _, m := range provider.AvailableModels() {
				if m == canonical {
					return
				}
			}
		}
		findings = append(findings, ResourceFinding{
			Code:    "model_unavailable",
			Field:   "models." + phase,
			Message: fmt.Sprintf("Model %q is not available in the current provider catalogue.", model),
		})
	}
	check("inquiry", models.Inquiry)
	check("research", models.Research)
	check("planning", models.Planning)
	check("implementation", models.Implementation)
	check("review", models.Review)
	check("utilities", models.Utilities)
	check("kb_build", models.KBBuild)
	return findings
}

func (s *resourceService) featureConfigDescriptor(f *feature.Feature) resourceDescriptor {
	return resourceDescriptor{
		id:          resourceID(ResourceKindFeatureConfig, f.ID),
		kind:        ResourceKindFeatureConfig,
		key:         f.ID,
		label:       fmt.Sprintf("%s — Configuration", f.Name),
		contentType: Yaml,
		effect:      NextDispatch,
		validatable: true,
		hierarchy:   []string{"Features", f.Name, "Configuration"},
		featureID:   f.ID,
	}
}

func (s *resourceService) runtimeConfigDescriptor() resourceDescriptor {
	return resourceDescriptor{
		id:          resourceID(ResourceKindRuntimeConfig, "runtime"),
		kind:        ResourceKindRuntimeConfig,
		key:         "runtime",
		label:       "Runtime Configuration",
		contentType: Yaml,
		effect:      NextDispatch,
		validatable: true,
		hierarchy:   []string{"Runtime", "Configuration"},
	}
}

func (s *resourceService) featureConfigRevision(featureID string) (string, error) {
	text, err := s.featureConfigYAML(featureID)
	if err != nil {
		return "", err
	}
	return textRevision([]byte(text)), nil
}

type featureConfigYAML struct {
	Models             modelDefaultsYAML      `yaml:"models"`
	Inquireness        string                 `yaml:"inquireness,omitempty"`
	Checkpoints        feature.Checkpoints    `yaml:"checkpoints,omitempty"`
	Pipeline           feature.PipelineProfile `yaml:"pipeline,omitempty"`
	InputNotifications string                 `yaml:"input_notifications,omitempty"`
}

type modelDefaultsYAML struct {
	Inquiry        string `yaml:"inquiry,omitempty"`
	Research       string `yaml:"research,omitempty"`
	Planning       string `yaml:"planning,omitempty"`
	Implementation string `yaml:"implementation,omitempty"`
	Review         string `yaml:"review,omitempty"`
	Utilities      string `yaml:"utilities,omitempty"`
	KBBuild        string `yaml:"kb_build,omitempty"`
}

func (m modelDefaultsYAML) toModelConfig() config.ModelConfig {
	return config.ModelConfig{
		Inquiry:        m.Inquiry,
		Research:       m.Research,
		Planning:       m.Planning,
		Implementation: m.Implementation,
		Review:         m.Review,
		Utilities:      m.Utilities,
		KBBuild:        m.KBBuild,
	}
}

type runtimeConfigYAML struct {
	Defaults       runtimeDefaultsYAML    `yaml:"defaults"`
	WorkspaceRoots []string               `yaml:"workspace_roots,omitempty"`
	Notifications  *notificationYAML      `yaml:"notifications,omitempty"`
}

type runtimeDefaultsYAML struct {
	Models             modelDefaultsYAML                      `yaml:"models,omitempty"`
	Pipeline           string                                 `yaml:"pipeline,omitempty"`
	Inquireness        string                                 `yaml:"inquireness,omitempty"`
	Checkpoints        *config.Checkpoints                    `yaml:"checkpoints,omitempty"`
	MaxIterations      int                                    `yaml:"max_iterations,omitempty"`
	ExitCriteria       string                                 `yaml:"exit_criteria,omitempty"`
}

func (d runtimeDefaultsYAML) toRuntimeDefaultsMutation() RuntimeDefaultsMutation {
	return RuntimeDefaultsMutation{
		Models:        d.Models.toModelConfig(),
		Pipeline:      d.Pipeline,
		Inquireness:   d.Inquireness,
		Checkpoints:   d.Checkpoints,
		MaxIterations: d.MaxIterations,
		ExitCriteria:  d.ExitCriteria,
	}
}

type notificationYAML struct {
	MuteFeatureInput bool `yaml:"mute_feature_input"`
}

func (n notificationYAML) toNotificationDTO() *NotificationConfigDTO {
	return &NotificationConfigDTO{MuteFeatureInput: n.MuteFeatureInput}
}

func (s *resourceService) featureConfigYAML(featureID string) (string, error) {
	if s.store == nil {
		return "", fmt.Errorf("feature store unavailable")
	}
	f, err := s.store.Load(featureID)
	if err != nil {
		return "", err
	}
	dto := featureConfigYAML{
		Models:             modelConfigToYAML(f.Models),
		Inquireness:        string(f.Inquireness),
		Checkpoints:        f.Checkpoints,
		Pipeline:           f.Pipeline,
		InputNotifications: string(f.InputNotifications),
	}
	data, err := yaml.Marshal(dto)
	if err != nil {
		return "", fmt.Errorf("marshal feature config: %w", err)
	}
	return string(data), nil
}

func modelConfigToYAML(mc config.ModelConfig) modelDefaultsYAML {
	return modelDefaultsYAML{
		Inquiry:        mc.Inquiry,
		Research:       mc.Research,
		Planning:       mc.Planning,
		Implementation: mc.Implementation,
		Review:         mc.Review,
		Utilities:      mc.Utilities,
		KBBuild:        mc.KBBuild,
	}
}

func (s *resourceService) runtimeConfigYAML() (string, error) {
	cfg := s.cfg()
	if cfg == nil {
		cfg = config.NewDefault()
	}
	dto := runtimeConfigYAML{
		Defaults: runtimeDefaultsYAML{
			Models:        modelConfigToYAML(cfg.Defaults.Models),
			Pipeline:      cfg.Defaults.Pipeline,
			Inquireness:   cfg.Defaults.Inquireness,
			Checkpoints:   &cfg.Defaults.Checkpoints,
			MaxIterations: cfg.Defaults.MaxIterations,
			ExitCriteria:  cfg.Defaults.ExitCriteria,
		},
		WorkspaceRoots: cfg.WorkspaceRoots,
		Notifications: &notificationYAML{
			MuteFeatureInput: cfg.Notifications.MuteFeatureInput,
		},
	}
	data, err := yaml.Marshal(dto)
	if err != nil {
		return "", fmt.Errorf("marshal runtime config: %w", err)
	}
	return string(data), nil
}

func (s *resourceService) skillsDir() string {
	if s.runtime.RuntimeDir == "" {
		return ""
	}
	return filepath.Join(s.runtime.RuntimeDir, skillDirName)
}

func (s *resourceService) guidelinesDir() string {
	if s.runtime.RuntimeDir == "" {
		return ""
	}
	return filepath.Join(s.runtime.RuntimeDir, guidelineDirName)
}

func isExcludedFile(path string) bool {
	base := filepath.Base(path)
	switch {
	case strings.Contains(base, "-stamp"):
		return true
	case strings.HasSuffix(base, ".tmp"), strings.HasSuffix(base, ".bak"):
		return true
	case strings.Contains(base, ".tmp-"):
		return true
	default:
		return false
	}
}

func isSymlink(info fs.FileInfo) bool {
	return info.Mode()&os.ModeSymlink != 0
}

func isValidUTF8(data []byte) bool {
	return utf8.Valid(data)
}

func contentTypeForFile(path string) ResourceContentType {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".md", ".markdown":
		return Markdown
	case ".yaml", ".yml":
		return Yaml
	default:
		return Text
	}
}

func fileLabel(rootLabel, relPath string) string {
	parts := strings.Split(relPath, "/")
	if len(parts) == 1 {
		return fmt.Sprintf("%s — %s", rootLabel, parts[0])
	}
	return fmt.Sprintf("%s — %s", rootLabel, strings.Join(parts, " / "))
}

func buildHierarchy(rootLabel, relPath string) []string {
	parts := strings.Split(relPath, "/")
	return append([]string{rootLabel}, parts...)
}

func isEntryFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "skill.md" || base == "index.md"
}

func validateFrontmatter(text, kind string, requiredFields []string) []ResourceFinding {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "---") {
		return []ResourceFinding{{
			Code:    "missing_frontmatter",
			Message: fmt.Sprintf("%s entry files must start with YAML frontmatter (---).", kind),
		}}
	}
	// Search for the closing delimiter at a line start rather than anywhere
	// in the body, so embedded "---" inside frontmatter values or prose is
	// not mistaken for the terminator.
	rest := trimmed[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return []ResourceFinding{{
			Code:    "unclosed_frontmatter",
			Message: fmt.Sprintf("%s entry file frontmatter is not closed (missing closing ---).", kind),
		}}
	}
	frontmatter := rest[:endIdx]
	var parsed map[string]interface{}
	if err := yaml.Unmarshal([]byte(frontmatter), &parsed); err != nil {
		return []ResourceFinding{{
			Code:    "invalid_frontmatter",
			Message: fmt.Sprintf("%s entry file frontmatter is invalid YAML: %s", kind, err.Error()),
		}}
	}
	var findings []ResourceFinding
	for _, field := range requiredFields {
		val, ok := parsed[field]
		if !ok || val == nil {
			findings = append(findings, ResourceFinding{
				Code:    "missing_field",
				Field:   field,
				Message: fmt.Sprintf("%s entry file frontmatter must define %q.", kind, field),
			})
		} else if s, ok := val.(string); ok && strings.TrimSpace(s) == "" {
			findings = append(findings, ResourceFinding{
				Code:    "empty_field",
				Field:   field,
				Message: fmt.Sprintf("%s entry file frontmatter field %q must not be empty.", kind, field),
			})
		}
	}
	return findings
}

func isValidPipeline(p string) bool {
	return feature.PipelineProfile(p).IsValid()
}

func isValidInquireness(s string) bool {
	return feature.Inquireness(s).IsValid()
}

func isValidInputNotifications(s string) bool {
	switch s {
	case "default", "enabled", "muted", "":
		return true
	default:
		return false
	}
}
