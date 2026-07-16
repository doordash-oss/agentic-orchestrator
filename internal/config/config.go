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

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/workspace"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Defaults        DefaultsConfig            `yaml:"defaults"`
	Repos           map[string]RepoConfig     `yaml:"repos"`
	WorkspaceRoots  []string                  `yaml:"workspace_roots,omitempty"`
	DiscoveredRepos map[string]RepoConfig     `yaml:"-"` // in-memory only, never persisted
	Notifications   NotificationConfig        `yaml:"notifications,omitempty"`
	Observability   ObservabilityConfig       `yaml:"observability,omitempty"`
	Providers       map[string]ProviderConfig `yaml:"providers,omitempty"`
}

// ProviderConfig holds per-provider overrides. CLI overrides the executable
// name (or path) Agentico invokes for that provider; when empty, the provider's
// built-in default binary name is used.
type ProviderConfig struct {
	CLI string `yaml:"cli,omitempty"`
}

// ProviderCLI returns the configured CLI binary for a provider, or fallback
// when no override is set.
func (c *Config) ProviderCLI(name, fallback string) string {
	if pc, ok := c.Providers[name]; ok {
		if b := strings.TrimSpace(pc.CLI); b != "" {
			return b
		}
	}
	return fallback
}

// ObservabilityConfig controls JSONL event emission and OTel export.
type ObservabilityConfig struct {
	Events          bool   `yaml:"events"`
	OTelEnabled     bool   `yaml:"otel_enabled,omitempty"`
	OTelEndpoint    string `yaml:"otel_endpoint,omitempty"`
	OTelInsecure    bool   `yaml:"otel_insecure,omitempty"`
	OTelServiceName string `yaml:"otel_service_name,omitempty"`

	parsed bool // set by UnmarshalYAML; not serialized
}

// UnmarshalYAML defaults Events to true so that configs which include an
// observability section but omit events still get event recording.
func (o *ObservabilityConfig) UnmarshalYAML(value *yaml.Node) error {
	o.Events = true
	o.parsed = true
	type plain ObservabilityConfig
	return value.Decode((*plain)(o))
}

type NotificationConfig struct {
	// MuteFeatureInput suppresses notifications when an agent is waiting for
	// manual feature input. Other notification types are unaffected.
	MuteFeatureInput bool `yaml:"mute_feature_input,omitempty"`
}

type DefaultsConfig struct {
	Models                   ModelConfig                   `yaml:"models" json:"models"`
	Effort                   EffortConfig                  `yaml:"effort,omitempty" json:"effort,omitempty"`
	PipelinePreferences      map[string]PipelinePreference `yaml:"pipeline_preferences,omitempty" json:"pipeline_preferences,omitempty"`
	ExitCriteria             string                        `yaml:"exit_criteria" json:"exit_criteria,omitempty"`
	Inquireness              string                        `yaml:"inquireness" json:"inquireness,omitempty"`
	Pipeline                 string                        `yaml:"pipeline,omitempty" json:"pipeline,omitempty"`
	MaxIterations            int                           `yaml:"max_iterations" json:"max_iterations,omitempty"`
	MaxConsecutiveFailures   int                           `yaml:"max_consecutive_failures" json:"max_consecutive_failures,omitempty"`
	MaxConsecutiveNoProgress int                           `yaml:"max_consecutive_no_progress" json:"max_consecutive_no_progress,omitempty"`
	MaxPhasePlanIterations   int                           `yaml:"max_phase_plan_iterations,omitempty" json:"max_phase_plan_iterations,omitempty"`
	Checkpoints              Checkpoints                   `yaml:"checkpoints" json:"checkpoints"`
	// AutomaticReviewEnabled controls the default-off workspace behavior that
	// lets an isolated reviewer classify a narrow set of Bash commands. An
	// absent or false flag means disabled; it is never seeded by applyDefaults so
	// legacy configs load as disabled without altering established defaults.
	AutomaticReviewEnabled bool `yaml:"automatic_review_enabled,omitempty" json:"automatic_review_enabled,omitempty"`
}

// PipelinePreference stores the last-used feature-creation settings for a
// specific pipeline profile.
type PipelinePreference struct {
	Models      ModelConfig  `yaml:"models,omitempty" json:"models,omitempty"`
	Effort      EffortConfig `yaml:"effort,omitempty" json:"effort,omitempty"`
	Inquireness string       `yaml:"inquireness,omitempty" json:"inquireness,omitempty"`
}

type ModelConfig struct {
	Inquiry        string `yaml:"inquiry" json:"inquiry,omitempty"`
	Research       string `yaml:"research" json:"research,omitempty"`
	Planning       string `yaml:"planning" json:"planning,omitempty"`
	Implementation string `yaml:"implementation" json:"implementation,omitempty"`
	Review         string `yaml:"review" json:"review,omitempty"`
	Utilities      string `yaml:"utilities" json:"utilities,omitempty"`
	KBBuild        string `yaml:"kb_build" json:"kb_build,omitempty"`
	// AutomaticReview is the optional reviewer model for automatic Bash review.
	// An empty value is meaningful: it means "Automatic" (resolve the first
	// eligible provider's preferred inexpensive model at review time). It is
	// intentionally not seeded by applyDefaults or catalog defaulting so no
	// inferred model is ever written back.
	AutomaticReview string `yaml:"automatic_review,omitempty" json:"automatic_review,omitempty"`
}

// EffortConfig holds per-role reasoning-effort configuration alongside
// ModelConfig. Each field accepts the closed set auto|low|medium|high|xhigh|max.
// Empty or missing values load as Auto without triggering a load-time rewrite.
// Every field corresponds to the same-named field on ModelConfig, except
// Utilities which mirrors ModelConfig.Utilities (the "chat" role).
type EffortConfig struct {
	Inquiry        string `yaml:"inquiry,omitempty" json:"inquiry,omitempty"`
	Research       string `yaml:"research,omitempty" json:"research,omitempty"`
	Planning       string `yaml:"planning,omitempty" json:"planning,omitempty"`
	Implementation string `yaml:"implementation,omitempty" json:"implementation,omitempty"`
	Review         string `yaml:"review,omitempty" json:"review,omitempty"`
	Utilities      string `yaml:"utilities,omitempty" json:"utilities,omitempty"`
	KBBuild        string `yaml:"kb_build,omitempty" json:"kb_build,omitempty"`
}

// EffortConfigFieldByName returns the effort value for the named role. The
// field name must match a ModelConfig field name (Inquiry, Research, Planning,
// Implementation, Review, Utilities, KBBuild). Returns "" for unrecognized
// names, which is treated as Auto by the resolver.
func EffortConfigFieldByName(e EffortConfig, field string) string {
	switch field {
	case "Inquiry":
		return e.Inquiry
	case "Research":
		return e.Research
	case "Planning":
		return e.Planning
	case "Implementation":
		return e.Implementation
	case "Review":
		return e.Review
	case "Utilities":
		return e.Utilities
	case "KBBuild":
		return e.KBBuild
	}
	return ""
}

// SetEffortConfigFieldByName sets the effort value for the named role on the
// given EffortConfig pointer. The field name must match a ModelConfig field
// name. Unrecognized names are silently ignored.
func SetEffortConfigFieldByName(e *EffortConfig, field, value string) {
	switch field {
	case "Inquiry":
		e.Inquiry = value
	case "Research":
		e.Research = value
	case "Planning":
		e.Planning = value
	case "Implementation":
		e.Implementation = value
	case "Review":
		e.Review = value
	case "Utilities":
		e.Utilities = value
	case "KBBuild":
		e.KBBuild = value
	}
}

// ModelConfigFieldByName returns the model value for the named role from
// ModelConfig. The field name must match a ModelConfig field name.
func ModelConfigFieldByName(m ModelConfig, field string) string {
	switch field {
	case "Inquiry":
		return m.Inquiry
	case "Research":
		return m.Research
	case "Planning":
		return m.Planning
	case "Implementation":
		return m.Implementation
	case "Review":
		return m.Review
	case "Utilities":
		return m.Utilities
	case "KBBuild":
		return m.KBBuild
	}
	return ""
}

// AllEffortRoles returns the canonical ordered list of role field names that
// have both a model and an effort field. The order matches ModelConfig field
// order. Utilities is included; callers that must omit it (e.g. the feature
// overlay) filter it out.
func AllEffortRoles() []string {
	return []string{"Inquiry", "Research", "Planning", "Implementation", "Review", "Utilities", "KBBuild"}
}

// UnmarshalYAML migrates the legacy "chat" YAML key to "utilities".
// If both keys are present, "utilities" takes precedence.
func (m *ModelConfig) UnmarshalYAML(value *yaml.Node) error {
	type aux struct {
		Inquiry         string `yaml:"inquiry"`
		Research        string `yaml:"research"`
		Planning        string `yaml:"planning"`
		Implementation  string `yaml:"implementation"`
		Review          string `yaml:"review"`
		Utilities       string `yaml:"utilities"`
		Chat            string `yaml:"chat"`
		KBBuild         string `yaml:"kb_build"`
		AutomaticReview string `yaml:"automatic_review"`
	}
	var a aux
	if err := value.Decode(&a); err != nil {
		return err
	}
	m.Inquiry = a.Inquiry
	m.Research = a.Research
	m.Planning = a.Planning
	m.Implementation = a.Implementation
	m.Review = a.Review
	m.Utilities = a.Utilities
	m.KBBuild = a.KBBuild
	m.AutomaticReview = a.AutomaticReview
	if m.Utilities == "" && a.Chat != "" {
		m.Utilities = a.Chat
	}
	if m.Inquiry == "" {
		m.Inquiry = m.Research
	}
	return nil
}

// Checkpoints controls which phase transitions pause for human review in the config defaults.
type Checkpoints struct {
	InquiryReview   bool `yaml:"inquiry_review" json:"inquiry_review,omitempty"`
	ResearchReview  bool `yaml:"research_review" json:"research_review,omitempty"`
	DesignReview    bool `yaml:"design_review" json:"design_review,omitempty"`
	RoadmapReview   bool `yaml:"roadmap_review" json:"roadmap_review,omitempty"`
	PhasePlanReview bool `yaml:"phase_plan_review" json:"phase_plan_review,omitempty"`
	ManualPublish   bool `yaml:"manual_publish" json:"manual_publish"`
	DraftPublish    bool `yaml:"draft_publish" json:"draft_publish,omitempty"`

	parsed bool // set by UnmarshalYAML; not serialized
}

// UnmarshalYAML defaults omitted checkpoint fields to true, except DraftPublish
// which defaults to false (opting into draft is the exception, not the rule).
// The legacy plan_review key is accepted by the decoder but intentionally ignored.
func (c *Checkpoints) UnmarshalYAML(value *yaml.Node) error {
	type checkpointFields struct {
		InquiryReview   *bool `yaml:"inquiry_review"`
		ResearchReview  *bool `yaml:"research_review"`
		DesignReview    *bool `yaml:"design_review"`
		RoadmapReview   *bool `yaml:"roadmap_review"`
		PhasePlanReview *bool `yaml:"phase_plan_review"`
		ManualPublish   *bool `yaml:"manual_publish"`
		DraftPublish    *bool `yaml:"draft_publish"`
	}

	var fields checkpointFields
	if err := value.Decode(&fields); err != nil {
		return err
	}

	c.InquiryReview = boolValueOrDefault(fields.InquiryReview, true)
	c.ResearchReview = boolValueOrDefault(fields.ResearchReview, true)
	c.DesignReview = boolValueOrDefault(fields.DesignReview, true)
	c.RoadmapReview = boolValueOrDefault(fields.RoadmapReview, true)
	c.PhasePlanReview = boolValueOrDefault(fields.PhasePlanReview, true)
	c.ManualPublish = boolValueOrDefault(fields.ManualPublish, true)
	c.DraftPublish = boolValueOrDefault(fields.DraftPublish, false)
	c.parsed = true
	return nil
}

func boolValueOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

type RepoConfig struct {
	Path          string                 `yaml:"path"`
	PipelineGates map[string]Checkpoints `yaml:"pipeline_gates,omitempty"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	applyDefaults(&cfg)
	return &cfg, nil
}

func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// LoadOrCreateWithStatus works like LoadOrCreate but also reports whether
// the config file was newly created. This enables callers to detect
// first-run scenarios.
func LoadOrCreateWithStatus(path string) (cfg *Config, isNew bool, err error) {
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		cfg = NewDefault()
		if err = Save(path, cfg); err != nil {
			return nil, false, fmt.Errorf("creating default config: %w", err)
		}
		return cfg, true, nil
	}
	cfg, err = Load(path)
	return cfg, false, err
}

// LoadOrCreate loads an existing config or creates a default one.
func LoadOrCreate(path string) (*Config, error) {
	cfg, _, err := LoadOrCreateWithStatus(path)
	return cfg, err
}

func applyDefaults(cfg *Config) {
	d := NewDefault()
	if cfg.Defaults.Models.Inquiry == "" {
		if cfg.Defaults.Models.Research != "" {
			cfg.Defaults.Models.Inquiry = cfg.Defaults.Models.Research
		} else {
			cfg.Defaults.Models.Inquiry = d.Defaults.Models.Inquiry
		}
	}
	if cfg.Defaults.Models.Research == "" {
		cfg.Defaults.Models.Research = d.Defaults.Models.Research
	}
	if cfg.Defaults.Models.Planning == "" {
		cfg.Defaults.Models.Planning = d.Defaults.Models.Planning
	}
	if cfg.Defaults.Models.Implementation == "" {
		cfg.Defaults.Models.Implementation = d.Defaults.Models.Implementation
	}
	if cfg.Defaults.Models.Review == "" {
		cfg.Defaults.Models.Review = d.Defaults.Models.Review
	}
	if cfg.Defaults.Models.Utilities == "" {
		cfg.Defaults.Models.Utilities = d.Defaults.Models.Utilities
	}
	if cfg.Defaults.Models.KBBuild == "" {
		cfg.Defaults.Models.KBBuild = d.Defaults.Models.KBBuild
	}
	if cfg.Defaults.MaxIterations == 0 {
		cfg.Defaults.MaxIterations = d.Defaults.MaxIterations
	}
	if cfg.Defaults.MaxConsecutiveFailures == 0 {
		cfg.Defaults.MaxConsecutiveFailures = d.Defaults.MaxConsecutiveFailures
	}
	if cfg.Defaults.MaxConsecutiveNoProgress == 0 {
		cfg.Defaults.MaxConsecutiveNoProgress = d.Defaults.MaxConsecutiveNoProgress
	}
	if cfg.Defaults.MaxPhasePlanIterations == 0 {
		cfg.Defaults.MaxPhasePlanIterations = 10
	}
	if cfg.Defaults.ExitCriteria == "" {
		cfg.Defaults.ExitCriteria = d.Defaults.ExitCriteria
	}
	if cfg.Defaults.Inquireness == "" {
		cfg.Defaults.Inquireness = d.Defaults.Inquireness
	}
	// If the YAML had no checkpoints section at all, UnmarshalYAML was never
	// called, so parsed is false and the fields are zero values. Apply the
	// complete default checkpoint set.
	if !cfg.Defaults.Checkpoints.parsed {
		cfg.Defaults.Checkpoints = d.Defaults.Checkpoints
	}

	if cfg.Defaults.Pipeline == "" {
		cfg.Defaults.Pipeline = "large"
	}

	if cfg.Repos == nil {
		cfg.Repos = make(map[string]RepoConfig)
	}

	// If the YAML had no observability section, apply defaults.
	if !cfg.Observability.parsed {
		cfg.Observability.Events = true
		cfg.Observability.OTelServiceName = "agentico"
	}
	if cfg.Observability.OTelServiceName == "" {
		cfg.Observability.OTelServiceName = "agentico"
	}

}

// ExpandHome expands a leading "~" or "~/" in a path to the user's home directory.
func ExpandHome(path string) string {
	return workspace.ExpandHome(path)
}

// DiscoverReposFromRoots scans all workspace roots for git repositories and
// populates cfg.DiscoveredRepos. Explicit repos in cfg.Repos are never
// shadowed. Returns the number of discovered repos.
func DiscoverReposFromRoots(cfg *Config) int {
	// Always start with an empty map (rebuild from scratch).
	cfg.DiscoveredRepos = make(map[string]RepoConfig)

	if len(cfg.WorkspaceRoots) == 0 {
		return 0
	}

	explicitRepoPaths := make(map[string]string, len(cfg.Repos))
	for name, rc := range cfg.Repos {
		explicitRepoPaths[name] = rc.Path
	}

	repos := workspace.DiscoverReposFromRoots(cfg.WorkspaceRoots, explicitRepoPaths)
	for _, repo := range repos {
		cfg.DiscoveredRepos[repo.Name] = RepoConfig{Path: repo.Path}
	}

	return len(cfg.DiscoveredRepos)
}

// AllRepos merges DiscoveredRepos with Repos. Explicit Repos entries take priority,
// but if an explicit entry has an empty Path, it inherits the path from the
// discovered entry (so users can override settings like pipeline_gates without
// having to duplicate the path).
func AllRepos(cfg *Config) map[string]RepoConfig {
	merged := make(map[string]RepoConfig)
	for k, v := range cfg.DiscoveredRepos {
		merged[k] = v
	}
	for k, v := range cfg.Repos {
		if v.Path == "" {
			if discovered, ok := cfg.DiscoveredRepos[k]; ok {
				v.Path = discovered.Path
			}
		}
		merged[k] = v
	}
	return merged
}

// ApplyProviderDefaults fills empty model config fields with defaults computed
// from detected providers in the registry. User-set values are never overwritten.
// The defaults map is keyed by PhaseRole string values (e.g. "research", "planning").
// Callers obtain this map via registry.DefaultModels().
func ApplyProviderDefaults(cfg *Config, defaults map[string]string) {
	if defaults == nil {
		return
	}
	m := &cfg.Defaults.Models
	if m.Inquiry == "" {
		m.Inquiry = defaults["inquiry"]
		if m.Inquiry == "" {
			m.Inquiry = defaults["research"]
		}
	}
	if m.Research == "" {
		m.Research = defaults["research"]
	}
	if m.Planning == "" {
		m.Planning = defaults["planning"]
	}
	if m.Implementation == "" {
		m.Implementation = defaults["implementation"]
	}
	if m.Review == "" {
		m.Review = defaults["review"]
	}
	if m.Utilities == "" {
		m.Utilities = defaults["chat"]
	}
	if m.KBBuild == "" {
		m.KBBuild = defaults["kb_build"]
	}
}

// PreferenceForPipeline resolves the effective remembered preferences for a
// pipeline profile. Missing remembered fields fall back to the global defaults.
func (d DefaultsConfig) PreferenceForPipeline(profile string) PipelinePreference {
	pref := PipelinePreference{
		Models:      d.Models,
		Effort:      d.Effort,
		Inquireness: d.Inquireness,
	}
	if profile == "" || d.PipelinePreferences == nil {
		return pref
	}
	saved, ok := d.PipelinePreferences[profile]
	if !ok {
		return pref
	}
	pref.Models = overlayModelConfig(pref.Models, saved.Models)
	pref.Effort = OverlayEffortConfig(pref.Effort, saved.Effort)
	if saved.Inquireness != "" {
		pref.Inquireness = saved.Inquireness
	}
	return pref
}

// OverlayEffortConfig overlays non-empty override fields onto base, preserving
// the other base values. Used by the server mutation target and pipeline
// preference resolution.
func OverlayEffortConfig(base, override EffortConfig) EffortConfig {
	if override.Inquiry != "" {
		base.Inquiry = override.Inquiry
	}
	if override.Research != "" {
		base.Research = override.Research
	}
	if override.Planning != "" {
		base.Planning = override.Planning
	}
	if override.Implementation != "" {
		base.Implementation = override.Implementation
	}
	if override.Review != "" {
		base.Review = override.Review
	}
	if override.Utilities != "" {
		base.Utilities = override.Utilities
	}
	if override.KBBuild != "" {
		base.KBBuild = override.KBBuild
	}
	return base
}

func overlayModelConfig(base, override ModelConfig) ModelConfig {
	if override.Inquiry != "" {
		base.Inquiry = override.Inquiry
	}
	if override.Research != "" {
		base.Research = override.Research
	}
	if override.Planning != "" {
		base.Planning = override.Planning
	}
	if override.Implementation != "" {
		base.Implementation = override.Implementation
	}
	if override.Review != "" {
		base.Review = override.Review
	}
	if override.Utilities != "" {
		base.Utilities = override.Utilities
	}
	if override.KBBuild != "" {
		base.KBBuild = override.KBBuild
	}
	if override.AutomaticReview != "" {
		base.AutomaticReview = override.AutomaticReview
	}
	return base
}
