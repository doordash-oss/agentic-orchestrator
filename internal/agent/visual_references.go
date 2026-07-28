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
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent/prompts"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"gopkg.in/yaml.v3"
)

const (
	// DesignMockupManifestVersion is the only manifest schema version accepted
	// by ResolveVisualReferences.
	DesignMockupManifestVersion = 1
	designMockupManifestPath    = "mockups/manifest.yaml"
)

// ApprovedMockup is one generated visual artifact approved during Design.
// Paths are absolute, validated paths beneath the Design artifact root.
type ApprovedMockup struct {
	ID       string
	HTMLPath string
	PNGPath  string
}

// ResolvedVisualReferences combines original feature images with generated,
// approved Design mockups. A missing manifest produces an empty Mockups slice.
type ResolvedVisualReferences struct {
	Images  []string
	Mockups []ApprovedMockup
}

// Paths returns every reference path in prompt order: original images, then
// each generated mockup's HTML and PNG representations.
func (r ResolvedVisualReferences) Paths() []string {
	paths := make([]string, 0, len(r.Images)+len(r.Mockups)*2)
	paths = append(paths, r.Images...)
	for _, mockup := range r.Mockups {
		paths = append(paths, mockup.HTMLPath, mockup.PNGPath)
	}
	return paths
}

// PromptInput projects resolved references into the shared prompt partial.
func (r ResolvedVisualReferences) PromptInput(label string) prompts.VisualReferencesInput {
	mockups := make([]prompts.MockupReference, 0, len(r.Mockups))
	for _, mockup := range r.Mockups {
		mockups = append(mockups, prompts.MockupReference{
			ID:       mockup.ID,
			HTMLPath: mockup.HTMLPath,
			PNGPath:  mockup.PNGPath,
		})
	}
	return prompts.VisualReferencesInput{
		Images:  append([]string(nil), r.Images...),
		Mockups: mockups,
		Label:   label,
	}
}

// ResolveVisualReferencesInput resolves and projects references in one call.
// Roadmap, phase-plan, implementation, implementation-review, and final-review
// callers can supply their phase-specific label without duplicating plumbing.
func ResolveVisualReferencesInput(
	f *feature.Feature,
	designArtifactPath string,
	label string,
) (prompts.VisualReferencesInput, error) {
	resolved, err := ResolveVisualReferences(f, designArtifactPath)
	if err != nil {
		return prompts.VisualReferencesInput{}, err
	}
	return resolved.PromptInput(label), nil
}

// ResolveVisualReferences merges the feature's original images with approved
// generated mockups declared by mockups/manifest.yaml beside the Design
// artifact. Missing manifests are accepted for backward compatibility.
func ResolveVisualReferences(f *feature.Feature, designArtifactPath string) (ResolvedVisualReferences, error) {
	var resolved ResolvedVisualReferences
	if f != nil {
		resolved.Images = append([]string(nil), f.Images...)
	}
	if designArtifactPath == "" {
		return resolved, nil
	}

	designRoot := filepath.Dir(designArtifactPath)
	manifestPath := filepath.Join(designRoot, filepath.FromSlash(designMockupManifestPath))
	if err := validateManifestConfinement(designRoot, manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ResolvedVisualReferences{}, err
	}
	manifest, err := readDesignMockupManifest(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return resolved, nil
	}
	if err != nil {
		return ResolvedVisualReferences{}, err
	}

	mockupRoot := filepath.Dir(manifestPath)
	if err := validateManifestDesignArtifact(designRoot, mockupRoot, designArtifactPath, manifest.DesignArtifact); err != nil {
		return ResolvedVisualReferences{}, fmt.Errorf("validating %s: %w", manifestPath, err)
	}
	htmlPath, err := validateMockupFile(designRoot, mockupRoot, manifest.HTML, ".html")
	if err != nil {
		return ResolvedVisualReferences{}, fmt.Errorf("validating %s: html: %w", manifestPath, err)
	}
	seenIDs := make(map[string]struct{}, len(manifest.States))
	seenSources := make(map[string]string, len(manifest.States))
	seenPNGs := make(map[string]string, len(manifest.States))
	for i, entry := range manifest.States {
		mockup, err := validateDesignMockup(
			designRoot, mockupRoot, htmlPath, entry, i, seenIDs, seenSources, seenPNGs,
		)
		if err != nil {
			return ResolvedVisualReferences{}, fmt.Errorf("validating %s: %w", manifestPath, err)
		}
		resolved.Mockups = append(resolved.Mockups, mockup)
	}
	if err := validateDesignMockupMetadata(manifest, resolved.Mockups); err != nil {
		return ResolvedVisualReferences{}, fmt.Errorf("validating %s: %w", manifestPath, err)
	}
	return resolved, nil
}

// ValidateDesignMockupManifest validates a manifest and every artifact it
// references without requiring the caller to know the design markdown path.
func ValidateDesignMockupManifest(manifestPath string) error {
	manifest, err := readDesignMockupManifest(manifestPath)
	if err != nil {
		return err
	}
	mockupRoot := filepath.Dir(manifestPath)
	designRoot := filepath.Dir(mockupRoot)
	designPath, err := validateMockupFile(designRoot, mockupRoot, manifest.DesignArtifact, ".md")
	if err != nil {
		return fmt.Errorf("validating %s: design_artifact: %w", manifestPath, err)
	}
	_, err = ResolveVisualReferences(nil, designPath)
	return err
}

func validateManifestConfinement(designRoot, manifestPath string) error {
	realManifest, err := filepath.EvalSymlinks(manifestPath)
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(designRoot)
	if err != nil {
		return fmt.Errorf("resolve Design artifact root %q: %w", designRoot, err)
	}
	confined, err := pathWithinRoot(realRoot, realManifest)
	if err != nil {
		return err
	}
	if !confined {
		return fmt.Errorf("design mockup manifest %q resolves outside the Design artifact root", manifestPath)
	}
	return nil
}

type designMockupManifest struct {
	SchemaVersion          int                 `yaml:"schema_version"`
	DesignArtifact         string              `yaml:"design_artifact"`
	HTML                   string              `yaml:"html"`
	ResponsiveExpectations []string            `yaml:"responsive_expectations,omitempty"`
	BindingDecisions       []string            `yaml:"binding_decisions,omitempty"`
	IllustrativeDetails    []string            `yaml:"illustrative_details,omitempty"`
	States                 []designMockupEntry `yaml:"states"`
}

type designMockupEntry struct {
	ID             string               `yaml:"id"`
	Title          string               `yaml:"title"`
	Source         string               `yaml:"source"`
	PNG            string               `yaml:"png"`
	Viewport       designMockupViewport `yaml:"viewport"`
	DesignSections []string             `yaml:"design_sections"`
	Description    string               `yaml:"description"`
}

type designMockupViewport struct {
	Width             int `yaml:"width"`
	Height            int `yaml:"height"`
	DeviceScaleFactor int `yaml:"device_scale_factor"`
}

func readDesignMockupManifest(path string) (designMockupManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return designMockupManifest{}, fmt.Errorf("opening design mockup manifest: %w", err)
	}
	defer file.Close()

	var manifest designMockupManifest
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return designMockupManifest{}, fmt.Errorf("decoding design mockup manifest %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return designMockupManifest{}, fmt.Errorf("decoding design mockup manifest %s: multiple YAML documents", path)
		}
		return designMockupManifest{}, fmt.Errorf("decoding design mockup manifest %s: %w", path, err)
	}
	if manifest.SchemaVersion != DesignMockupManifestVersion {
		return designMockupManifest{}, fmt.Errorf(
			"design mockup manifest %s has schema_version %d; want %d",
			path, manifest.SchemaVersion, DesignMockupManifestVersion,
		)
	}
	if strings.TrimSpace(manifest.DesignArtifact) == "" {
		return designMockupManifest{}, fmt.Errorf("design mockup manifest %s has no design_artifact", path)
	}
	if strings.TrimSpace(manifest.HTML) == "" {
		return designMockupManifest{}, fmt.Errorf("design mockup manifest %s has no html", path)
	}
	if len(manifest.States) == 0 {
		return designMockupManifest{}, fmt.Errorf("design mockup manifest %s has no states", path)
	}
	return manifest, nil
}

func validateManifestDesignArtifact(designRoot, mockupRoot, designArtifactPath, declaredPath string) error {
	resolved, err := validateMockupFile(designRoot, mockupRoot, strings.TrimSpace(declaredPath), ".md")
	if err != nil {
		return fmt.Errorf("design_artifact: %w", err)
	}
	expected, err := filepath.Abs(designArtifactPath)
	if err != nil {
		return fmt.Errorf("resolve Design artifact path %q: %w", designArtifactPath, err)
	}
	if resolved != filepath.Clean(expected) {
		return fmt.Errorf("design_artifact resolves to %q; want %q", resolved, filepath.Clean(expected))
	}
	return nil
}

func validateDesignMockup(
	designRoot string,
	mockupRoot string,
	manifestHTMLPath string,
	entry designMockupEntry,
	index int,
	seenIDs map[string]struct{},
	seenSources map[string]string,
	seenPNGs map[string]string,
) (ApprovedMockup, error) {
	entry.ID = strings.TrimSpace(entry.ID)
	entry.Source = strings.TrimSpace(entry.Source)
	entry.PNG = strings.TrimSpace(entry.PNG)
	if entry.ID == "" {
		return ApprovedMockup{}, fmt.Errorf("states[%d].id is required", index)
	}
	if entry.Source == "" {
		return ApprovedMockup{}, fmt.Errorf("mockup state %q source is required", entry.ID)
	}
	if entry.PNG == "" {
		return ApprovedMockup{}, fmt.Errorf("mockup state %q png is required", entry.ID)
	}
	if _, exists := seenIDs[entry.ID]; exists {
		return ApprovedMockup{}, fmt.Errorf("duplicate mockup id %q", entry.ID)
	}
	seenIDs[entry.ID] = struct{}{}

	sourceFile, fragment, hasFragment := strings.Cut(entry.Source, "#")
	if !hasFragment || strings.TrimSpace(fragment) == "" {
		return ApprovedMockup{}, fmt.Errorf("mockup state %q source %q requires a fragment", entry.ID, entry.Source)
	}
	htmlPath, err := validateMockupFile(designRoot, mockupRoot, sourceFile, ".html")
	if err != nil {
		return ApprovedMockup{}, fmt.Errorf("mockup state %q source: %w", entry.ID, err)
	}
	if htmlPath != manifestHTMLPath {
		return ApprovedMockup{}, fmt.Errorf(
			"mockup state %q source resolves to %q; want manifest html %q",
			entry.ID, htmlPath, manifestHTMLPath,
		)
	}
	pngPath, err := validateMockupFile(designRoot, mockupRoot, entry.PNG, ".png")
	if err != nil {
		return ApprovedMockup{}, fmt.Errorf("mockup state %q png: %w", entry.ID, err)
	}
	sourcePath := htmlPath + "#" + fragment
	if owner, exists := seenSources[sourcePath]; exists {
		return ApprovedMockup{}, fmt.Errorf("duplicate source path %q used by mockups %q and %q", sourcePath, owner, entry.ID)
	}
	seenSources[sourcePath] = entry.ID
	if owner, exists := seenPNGs[pngPath]; exists {
		return ApprovedMockup{}, fmt.Errorf("duplicate png path %q used by mockups %q and %q", pngPath, owner, entry.ID)
	}
	seenPNGs[pngPath] = entry.ID
	return ApprovedMockup{ID: entry.ID, HTMLPath: sourcePath, PNGPath: pngPath}, nil
}

func validateDesignMockupMetadata(manifest designMockupManifest, mockups []ApprovedMockup) error {
	if len(manifest.ResponsiveExpectations) == 0 {
		return errors.New("responsive_expectations must contain at least one decision")
	}
	if len(manifest.BindingDecisions) == 0 {
		return errors.New("binding_decisions must contain at least one decision")
	}
	if manifest.IllustrativeDetails == nil {
		return errors.New("illustrative_details must be present (use [] when none apply)")
	}
	for i, entry := range manifest.States {
		if strings.TrimSpace(entry.Title) == "" {
			return fmt.Errorf("mockup state %q title is required", entry.ID)
		}
		if entry.Viewport.Width <= 0 || entry.Viewport.Height <= 0 || entry.Viewport.DeviceScaleFactor <= 0 {
			return fmt.Errorf("mockup state %q viewport values must be positive", entry.ID)
		}
		if len(entry.DesignSections) == 0 {
			return fmt.Errorf("mockup state %q design_sections is required", entry.ID)
		}
		if strings.TrimSpace(entry.Description) == "" {
			return fmt.Errorf("mockup state %q description is required", entry.ID)
		}
		file, err := os.Open(mockups[i].PNGPath)
		if err != nil {
			return fmt.Errorf("open mockup state %q PNG: %w", entry.ID, err)
		}
		cfg, _, decodeErr := image.DecodeConfig(file)
		closeErr := file.Close()
		if decodeErr != nil {
			return fmt.Errorf("mockup state %q PNG is not decodable: %w", entry.ID, decodeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close mockup state %q PNG: %w", entry.ID, closeErr)
		}
		expectedWidth := entry.Viewport.Width * entry.Viewport.DeviceScaleFactor
		expectedHeight := entry.Viewport.Height * entry.Viewport.DeviceScaleFactor
		if cfg.Width != expectedWidth || cfg.Height != expectedHeight {
			return fmt.Errorf(
				"mockup state %q PNG dimensions are %dx%d; want %dx%d from viewport",
				entry.ID, cfg.Width, cfg.Height, expectedWidth, expectedHeight,
			)
		}
	}
	return nil
}

func validateMockupFile(designRoot, mockupRoot, manifestPath, extension string) (string, error) {
	if filepath.IsAbs(manifestPath) {
		return "", fmt.Errorf("path %q must be relative", manifestPath)
	}
	path := filepath.Clean(filepath.Join(mockupRoot, filepath.FromSlash(manifestPath)))
	confined, err := pathWithinRoot(designRoot, path)
	if err != nil {
		return "", err
	}
	if !confined {
		return "", fmt.Errorf("path %q escapes the Design artifact root", manifestPath)
	}
	if !strings.EqualFold(filepath.Ext(path), extension) {
		return "", fmt.Errorf("path %q must use the %s extension", manifestPath, extension)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path %q is not a regular file", path)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("path %q is empty", path)
	}

	realRoot, err := filepath.EvalSymlinks(designRoot)
	if err != nil {
		return "", fmt.Errorf("resolve Design artifact root %q: %w", designRoot, err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	confined, err = pathWithinRoot(realRoot, realPath)
	if err != nil {
		return "", err
	}
	if !confined {
		return "", fmt.Errorf("path %q resolves outside the Design artifact root", manifestPath)
	}
	return path, nil
}

func pathWithinRoot(root, path string) (bool, error) {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false, fmt.Errorf("compare path %q with root %q: %w", path, root, err)
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

// visualReferencesSection renders user-attached images (mockups, design
// comps, screenshots of desired state, annotated whiteboards, bug
// repros, etc.) as an explicit prompt section that instructs the agent
// to Read each one before producing output.
//
// Today's pipeline carries f.Images only through Inquire and Design;
// every downstream phase — Plan, Implement, Review, FinalReview — has
// historically dropped them. For a feature whose intent was communicated
// through pixels, that's the same failure mode as dropping the design:
// the agent ends up working from text-only summaries of visual commitments.
//
// Returns "" when the images slice is empty.
//
// label is a short phase-specific noun woven into the imperative so each
// phase's prompt reads naturally. Pass "" to use a generic fallback.
//
// The literal prose lives in
// internal/agent/prompts/partials/visual_references.tmpl.
func visualReferencesSection(images []string, label string) string {
	return prompts.VisualReferences(prompts.VisualReferencesInput{
		Images: images,
		Label:  label,
	})
}

// resolvedVisualReferencesSection renders original images and approved
// generated mockups through the same shared prompt partial.
func resolvedVisualReferencesSection(references ResolvedVisualReferences, label string) string {
	return prompts.VisualReferences(references.PromptInput(label))
}

func visualReferencesForFeature(f *feature.Feature, designArtifactPath, label string) prompts.VisualReferencesInput {
	resolved, err := ResolveVisualReferences(f, designArtifactPath)
	if err != nil {
		images := []string(nil)
		if f != nil {
			images = append(images, f.Images...)
		}
		return prompts.VisualReferencesInput{Images: images, Label: label}
	}
	return resolved.PromptInput(label)
}

func visualReferencesSectionForFeature(f *feature.Feature, designArtifactPath, label string) string {
	return prompts.VisualReferences(visualReferencesForFeature(f, designArtifactPath, label))
}
