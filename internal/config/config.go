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
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// validKeyboardLayouts defines the set of accepted keyboard_layout values.
// An empty string means "use default (US) behaviour".
var validKeyboardLayouts = map[string]bool{
	"":       true,
	"us":     true,
	"nordic": true,
}

type Config struct {
	Defaults        DefaultsConfig        `yaml:"defaults"`
	Repos           map[string]RepoConfig `yaml:"repos"`
	WorkspaceRoots  []string              `yaml:"workspace_roots,omitempty"`
	DiscoveredRepos map[string]RepoConfig `yaml:"-"` // in-memory only, never persisted
	Notifications   NotificationConfig    `yaml:"notifications,omitempty"`
	UI              UIConfig              `yaml:"ui,omitempty"`
	Observability   ObservabilityConfig   `yaml:"observability,omitempty"`
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

type UIConfig struct {
	CollapsedSections []string `yaml:"collapsed_sections,omitempty"`
	KeyboardLayout    string   `yaml:"keyboard_layout,omitempty"`
}

type NotificationConfig struct {
	// TerminalBundleID overrides the auto-detected terminal app bundle ID
	// used by terminal-notifier's -activate flag. Example: "com.googlecode.iterm2"
	TerminalBundleID string `yaml:"terminal_bundle_id,omitempty"`
	// MuteFeatureInput suppresses notifications when an agent is waiting for
	// manual feature input. Other notification types are unaffected.
	MuteFeatureInput bool `yaml:"mute_feature_input,omitempty"`
}

type DefaultsConfig struct {
	Models                   ModelConfig                   `yaml:"models"`
	PipelinePreferences      map[string]PipelinePreference `yaml:"pipeline_preferences,omitempty"`
	ExitCriteria             string                        `yaml:"exit_criteria"`
	Inquireness              string                        `yaml:"inquireness"`
	Pipeline                 string                        `yaml:"pipeline,omitempty"`
	MaxIterations            int                           `yaml:"max_iterations"`
	MaxConsecutiveFailures   int                           `yaml:"max_consecutive_failures"`
	MaxConsecutiveNoProgress int                           `yaml:"max_consecutive_no_progress"`
	MaxPhasePlanIterations   int                           `yaml:"max_phase_plan_iterations,omitempty"`
	Checkpoints              Checkpoints                   `yaml:"checkpoints"`
}

// PipelinePreference stores the last-used feature-creation settings for a
// specific pipeline profile.
type PipelinePreference struct {
	Models      ModelConfig `yaml:"models,omitempty"`
	Inquireness string      `yaml:"inquireness,omitempty"`
}

type ModelConfig struct {
	Inquiry        string `yaml:"inquiry"`
	Research       string `yaml:"research"`
	Planning       string `yaml:"planning"`
	Implementation string `yaml:"implementation"`
	Review         string `yaml:"review"`
	Utilities      string `yaml:"utilities"`
	KBBuild        string `yaml:"kb_build"`
}

// UnmarshalYAML migrates the legacy "chat" YAML key to "utilities".
// If both keys are present, "utilities" takes precedence.
func (m *ModelConfig) UnmarshalYAML(value *yaml.Node) error {
	type aux struct {
		Inquiry        string `yaml:"inquiry"`
		Research       string `yaml:"research"`
		Planning       string `yaml:"planning"`
		Implementation string `yaml:"implementation"`
		Review         string `yaml:"review"`
		Utilities      string `yaml:"utilities"`
		Chat           string `yaml:"chat"`
		KBBuild        string `yaml:"kb_build"`
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
	InquiryReview   bool `yaml:"inquiry_review"`
	ResearchReview  bool `yaml:"research_review"`
	DesignReview    bool `yaml:"design_review"`
	RoadmapReview   bool `yaml:"roadmap_review"`
	PhasePlanReview bool `yaml:"phase_plan_review"`
	ManualPublish   bool `yaml:"manual_publish"`

	parsed bool // set by UnmarshalYAML; not serialized
}

// UnmarshalYAML defaults omitted checkpoint fields to true. The legacy
// plan_review key is accepted by the decoder but intentionally ignored.
func (c *Checkpoints) UnmarshalYAML(value *yaml.Node) error {
	type checkpointFields struct {
		InquiryReview   *bool `yaml:"inquiry_review"`
		ResearchReview  *bool `yaml:"research_review"`
		DesignReview    *bool `yaml:"design_review"`
		RoadmapReview   *bool `yaml:"roadmap_review"`
		PhasePlanReview *bool `yaml:"phase_plan_review"`
		ManualPublish   *bool `yaml:"manual_publish"`
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

// DiscoverRepos scans baseDir for immediate subdirectories that contain a .git directory
// and adds them to the config if not already present. Returns the number of newly added repos.
func DiscoverRepos(cfg *Config, baseDir string) int {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return 0
	}
	added := 0
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name()[0] == '.' {
			continue
		}
		repoPath := filepath.Join(baseDir, entry.Name())
		gitDir := filepath.Join(repoPath, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			// Check if already in config
			if _, exists := cfg.Repos[entry.Name()]; !exists {
				cfg.Repos[entry.Name()] = RepoConfig{Path: repoPath}
				added++
			}
		}
	}
	return added
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

	// Validate keyboard layout; reset to empty (default) if unrecognised.
	if !validKeyboardLayouts[cfg.UI.KeyboardLayout] {
		log.Printf("config: unknown keyboard_layout %q, falling back to default", cfg.UI.KeyboardLayout)
		cfg.UI.KeyboardLayout = ""
	}
}

// ExpandHome expands a leading ~/ in a path to the user's home directory.
func ExpandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
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

	// Collect explicit repo names that already have a path into a skip-set.
	// Repos with an empty path are NOT skipped so that discovery can fill it in.
	explicitNames := make(map[string]bool, len(cfg.Repos))
	for name, rc := range cfg.Repos {
		if rc.Path != "" {
			explicitNames[name] = true
		}
	}

	// Deduplicate roots by resolved path before scanning.
	type repoTuple struct {
		rootResolved string
		rootBasename string
		repoName     string
		repoPath     string
	}

	seenRoots := make(map[string]bool)
	var tuples []repoTuple

	for _, root := range cfg.WorkspaceRoots {
		expanded := ExpandHome(root)
		resolved, err := filepath.Abs(expanded)
		if err != nil {
			resolved = expanded
		}
		if seenRoots[resolved] {
			continue
		}
		seenRoots[resolved] = true

		// Check if the workspace root itself is a git repo.
		rootGitDir := filepath.Join(expanded, ".git")
		if info, err := os.Stat(rootGitDir); err == nil && info.IsDir() {
			repoName := filepath.Base(resolved)
			if !explicitNames[repoName] {
				tuples = append(tuples, repoTuple{
					rootResolved: filepath.Dir(resolved),
					rootBasename: filepath.Base(filepath.Dir(resolved)),
					repoName:     repoName,
					repoPath:     expanded,
				})
			}
			continue
		}

		entries, err := os.ReadDir(expanded)
		if err != nil {
			continue
		}
		rootBase := filepath.Base(resolved)
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			repoPath := filepath.Join(expanded, entry.Name())
			gitDir := filepath.Join(repoPath, ".git")
			if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
				repoName := entry.Name()
				if explicitNames[repoName] {
					continue
				}
				tuples = append(tuples, repoTuple{
					rootResolved: resolved,
					rootBasename: rootBase,
					repoName:     repoName,
					repoPath:     repoPath,
				})
			}
		}
	}

	// Detect collisions: group by repoName, tracking distinct root paths per name.
	nameRoots := make(map[string]map[string]bool) // repoName -> set of rootResolved
	for _, t := range tuples {
		if nameRoots[t.repoName] == nil {
			nameRoots[t.repoName] = make(map[string]bool)
		}
		nameRoots[t.repoName][t.rootResolved] = true
	}

	// For colliding repo names, check if the root basenames are also identical.
	// If so, we need a more specific qualifier than just rootBasename.
	basenameCounts := make(map[string]map[string]bool) // repoName -> set of rootBasename
	for _, t := range tuples {
		if len(nameRoots[t.repoName]) <= 1 {
			continue
		}
		if basenameCounts[t.repoName] == nil {
			basenameCounts[t.repoName] = make(map[string]bool)
		}
		basenameCounts[t.repoName][t.rootBasename] = true
	}

	// Second pass: compute keys and populate DiscoveredRepos.
	for _, t := range tuples {
		key := t.repoName
		if len(nameRoots[t.repoName]) > 1 {
			// Collision — need to qualify. Use rootBasename if unique, else
			// use progressively more parent path components.
			if len(basenameCounts[t.repoName]) == len(nameRoots[t.repoName]) {
				// All root basenames are distinct — rootBasename is sufficient.
				key = t.rootBasename + "/" + t.repoName
			} else {
				// Some root basenames collide — use enough path components to disambiguate.
				key = uniqueRootPrefix(t.rootResolved, nameRoots[t.repoName]) + "/" + t.repoName
			}
		}
		cfg.DiscoveredRepos[key] = RepoConfig{Path: t.repoPath}
	}

	return len(cfg.DiscoveredRepos)
}

// uniqueRootPrefix returns the shortest trailing path components of rootPath
// that distinguish it from all other roots in the set.
func uniqueRootPrefix(rootPath string, allRoots map[string]bool) string {
	// Split rootPath into components.
	parts := strings.Split(filepath.ToSlash(rootPath), "/")
	// Remove empty leading component from absolute paths.
	var cleaned []string
	for _, p := range parts {
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	if len(cleaned) == 0 {
		return rootPath
	}

	// Collect other roots' component lists.
	var others [][]string
	for r := range allRoots {
		if r == rootPath {
			continue
		}
		rParts := strings.Split(filepath.ToSlash(r), "/")
		var rCleaned []string
		for _, p := range rParts {
			if p != "" {
				rCleaned = append(rCleaned, p)
			}
		}
		others = append(others, rCleaned)
	}

	// Try progressively more trailing components until unique.
	for depth := 1; depth <= len(cleaned); depth++ {
		suffix := cleaned[len(cleaned)-depth:]
		candidate := strings.Join(suffix, "/")
		unique := true
		for _, other := range others {
			if len(other) >= depth {
				otherSuffix := other[len(other)-depth:]
				if strings.Join(otherSuffix, "/") == candidate {
					unique = false
					break
				}
			}
		}
		if unique {
			return candidate
		}
	}
	// Fallback: use the full path (should not happen for distinct roots).
	return strings.Join(cleaned, "/")
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
	if saved.Inquireness != "" {
		pref.Inquireness = saved.Inquireness
	}
	return pref
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
	return base
}
