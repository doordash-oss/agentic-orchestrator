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
	// ModelRecommendations is a startup-only, ordered preference list per phase.
	// Entries are exact provider:model IDs or aliases from the discovered catalog.
	// It guides new defaults without replacing explicit defaults.models selections.
	ModelRecommendations map[string][]string `yaml:"model_recommendations,omitempty"`

	Defaults        DefaultsConfig            `yaml:"defaults"`
	Repos           map[string]RepoConfig     `yaml:"repos"`
	WorkspaceRoots  []string                  `yaml:"workspace_roots,omitempty"`
	DiscoveredRepos map[string]RepoConfig     `yaml:"-"` // in-memory only, never persisted
	Notifications   NotificationConfig        `yaml:"notifications,omitempty"`
	Observability   ObservabilityConfig       `yaml:"observability,omitempty"`
	Providers       map[string]ProviderConfig `yaml:"providers,omitempty"`
	// Server holds startup-only settings for the headless server. They are
	// read at launch and intentionally not part of the runtime-config REST
	// surface.
	Server ServerConfig `yaml:"server,omitempty"`
}

// ServerConfig holds headless-server settings applied once at startup.
type ServerConfig struct {
	// Name overrides the server display name. Precedence is
	// --name > server.name > the persisted generated name.
	Name string `yaml:"name,omitempty"`
	// Updates carries the release-availability startup settings. They are
	// validated at server launch and, like the rest of this section,
	// intentionally never appear on the runtime-config REST surface.
	Updates ServerUpdatesConfig `yaml:"updates,omitempty"`
	// Capabilities controls built-in verification capability probes.
	Capabilities ServerCapabilitiesConfig `yaml:"capabilities,omitempty"`
}

// ServerCapabilitiesConfig holds server.capabilities startup settings.
type ServerCapabilitiesConfig struct {
	// BrowserState is "allow" or "deny" for the authenticated-browser
	// capability. Empty defaults to allow on loopback servers and deny on
	// servers exposed to the network, where a signed-in session must never
	// be provisioned.
	BrowserState string `yaml:"browser_state,omitempty"`
}

// ServerUpdatesConfig is the raw server.updates config map. Values stay
// untouched strings here; the headless server parses, validates, and applies
// them at launch (see internal/selfupdate.ResolveStartupSettings). strategy
// and window shape automatic installs only.
type ServerUpdatesConfig struct {
	Policy        string `yaml:"policy,omitempty"`
	Channel       string `yaml:"channel,omitempty"`
	CheckInterval string `yaml:"check_interval,omitempty"`
	Strategy      string `yaml:"strategy,omitempty"`
	Window        string `yaml:"window,omitempty"`
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
	// Metrics share otel_endpoint unless overridden; trace-only collectors
	// need either an override or the opt-out.
	OTelMetricsDisabled    bool   `yaml:"otel_metrics_disabled,omitempty"`
	OTelMetricsEndpoint    string `yaml:"otel_metrics_endpoint,omitempty"`
	OTelMetricsTemporality string `yaml:"otel_metrics_temporality,omitempty"`

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
	// DangerouslySkipPermissions approves permission requests in the harness without a reviewer.
	// Explicit feature auto-mode overrides take precedence over this workspace default.
	DangerouslySkipPermissions bool `yaml:"dangerously_skip_permissions,omitempty" json:"dangerously_skip_permissions,omitempty"`
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

// IsEmpty reports whether no stage model, including the automatic reviewer,
// is set.
func (m ModelConfig) IsEmpty() bool {
	return m.Inquiry == "" &&
		m.Research == "" &&
		m.Planning == "" &&
		m.Implementation == "" &&
		m.Review == "" &&
		m.Utilities == "" &&
		m.KBBuild == "" &&
		m.AutomaticReview == ""
}

// EffortConfig holds per-role reasoning-effort configuration alongside
// ModelConfig. Each field accepts the closed set auto|low|medium|high|xhigh|max|ultra.
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

// Role is one model-and-effort role, keyed by the role string the provider
// layer uses. Model and Effort return the role's field on the given struct.
type Role struct {
	Name   string
	Model  func(*ModelConfig) *string
	Effort func(*EffortConfig) *string
}

// Roles lists every model-and-effort role in ModelConfig field order. The
// "chat" role uses the Utilities fields. AutomaticReview is not a role here:
// it has no effort field and is never defaulted.
var Roles = []Role{
	{"inquiry", func(m *ModelConfig) *string { return &m.Inquiry }, func(e *EffortConfig) *string { return &e.Inquiry }},
	{"research", func(m *ModelConfig) *string { return &m.Research }, func(e *EffortConfig) *string { return &e.Research }},
	{"planning", func(m *ModelConfig) *string { return &m.Planning }, func(e *EffortConfig) *string { return &e.Planning }},
	{"implementation", func(m *ModelConfig) *string { return &m.Implementation }, func(e *EffortConfig) *string { return &e.Implementation }},
	{"review", func(m *ModelConfig) *string { return &m.Review }, func(e *EffortConfig) *string { return &e.Review }},
	{"chat", func(m *ModelConfig) *string { return &m.Utilities }, func(e *EffortConfig) *string { return &e.Utilities }},
	{"kb_build", func(m *ModelConfig) *string { return &m.KBBuild }, func(e *EffortConfig) *string { return &e.KBBuild }},
}

// RoleNamed returns the role with the given name and whether it exists.
func RoleNamed(name string) (Role, bool) {
	for _, r := range Roles {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
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
	if err := validateModelRecommendations(cfg.ModelRecommendations); err != nil {
		return nil, err
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
	if m := &cfg.Defaults.Models; m.Inquiry == "" && m.Research != "" {
		m.Inquiry = m.Research
	}
	fillEmptyModels(&cfg.Defaults.Models, d.Defaults.Models)
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

// ApplyProviderDefaults fills empty model config fields with the catalog
// defaults computed from detected providers (registry.CatalogDefaultModels).
// User-set values are never overwritten; an empty inquiry default falls back to
// the research default. Returns true when any model field changed.
func ApplyProviderDefaults(cfg *Config, defaults ModelConfig) bool {
	before := cfg.Defaults.Models
	if defaults.Inquiry == "" {
		defaults.Inquiry = defaults.Research
	}
	fillEmptyModels(&cfg.Defaults.Models, defaults)
	return cfg.Defaults.Models != before
}

// fillEmptyModels copies each role's model from defaults into m when unset.
func fillEmptyModels(m *ModelConfig, defaults ModelConfig) {
	for _, r := range Roles {
		if p := r.Model(m); *p == "" {
			*p = *r.Model(&defaults)
		}
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
	for _, r := range Roles {
		if v := *r.Effort(&override); v != "" {
			*r.Effort(&base) = v
		}
	}
	return base
}

func overlayModelConfig(base, override ModelConfig) ModelConfig {
	for _, r := range Roles {
		if v := *r.Model(&override); v != "" {
			*r.Model(&base) = v
		}
	}
	if override.AutomaticReview != "" {
		base.AutomaticReview = override.AutomaticReview
	}
	return base
}

// validateModelRecommendations rejects typos instead of silently falling back
// to an unrelated model. Missing catalog entries remain valid fallbacks: access
// and live model availability are resolved after discovery.
func validateModelRecommendations(preferences map[string][]string) error {
	for role, models := range preferences {
		if _, ok := RoleNamed(role); !ok {
			return fmt.Errorf("model_recommendations: unknown phase %q", role)
		}
		for _, model := range models {
			provider, id, ok := strings.Cut(model, ":")
			if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(id) == "" || strings.TrimSpace(model) != model {
				return fmt.Errorf("model_recommendations.%s: expected provider:model selector", role)
			}
		}
	}
	return nil
}
