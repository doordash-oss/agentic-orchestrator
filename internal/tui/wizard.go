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

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
)

// ImagePastedMsg is sent when a clipboard image is successfully saved.
type ImagePastedMsg struct {
	Path string
}

// ImagePasteFailedMsg is sent when no image is available on the clipboard.
type ImagePasteFailedMsg struct{}

// TextPastedMsg is sent when Ctrl+V falls back to text clipboard content.
type TextPastedMsg struct {
	Text string
}

// FilesPastedMsg is sent when file(s) are successfully captured from the clipboard.
type FilesPastedMsg struct {
	Paths []string // temp file paths
	Names []string // original filenames
}

type wizardStep int

const (
	wizardStepWhat     wizardStep = iota // Step 1: Name + Description
	wizardStepWhere                      // Step 2: Repo selection
	wizardStepPipeline                   // Step 3: Pipeline profile selection
	wizardStepReview                     // Step 4: Summary + create
)

// whereFocus is the focus axis on the Where step (chip picker layout).
// Tab cycles forward, Shift+Tab cycles backward. The list is the primary
// axis; the others become reachable as the user moves through the picker.
type whereFocus int

const (
	whereFocusList     whereFocus = iota // repo list (default; filter input is part of this focus)
	whereFocusBrowse                     // "Browse for more..." action row
	whereFocusCreate                     // "Create new repo..." action row (only when workspace roots exist)
	whereFocusContinue                   // "Continue with N repos" CTA button
)

type summaryField int

const (
	summaryFieldName summaryField = iota
	summaryFieldRepos
	summaryFieldRisk
	summaryFieldModels
	summaryFieldInquireness
	summaryFieldCheckpoints
	summaryFieldExitCriteria
)

type modelProviderGroup struct {
	Name   string
	Models []string
}

// RepoBranchInfo holds branch detection results for a repo.
type RepoBranchInfo struct {
	Name          string
	CurrentBranch string
	DefaultBranch string
	IsOffDefault  bool
}

// WizardResult holds the collected data from the wizard.
type WizardResult struct {
	Name         string
	Description  string
	Images       []string // temp image paths
	Attachments  []string // temp attachment file paths
	Repos        []string
	Models       config.ModelConfig
	ExitCriteria string
	Inquireness  string
	RiskLevel    string
	Pipeline     feature.PipelineProfile
	// UseCurrentBranch is the "any repo branched off default" flag, retained
	// for downstream callers that just need a yes/no signal. The per-repo
	// truth lives in UseCurrentBranchPerRepo.
	UseCurrentBranch bool
	// UseCurrentBranchPerRepo carries each repo's branch-base choice: true
	// means "start the worktree from current HEAD"; false (or missing) means
	// "start from default branch". Only off-default repos appear in the map.
	UseCurrentBranchPerRepo map[string]bool
	Checkpoints             feature.Checkpoints
}

type WizardModel struct {
	step                    wizardStep
	whatFocus               int // 0=name, 1=description (tracks focus on Step 1)
	nameInput               textinput.Model
	nameWarning             string            // warning message for duplicate name
	existingSlugs           map[string]string // slug → feature name, for duplicate detection
	descInput               SimpleTextarea
	repoInput               textinput.Model
	exitInput               SimpleTextarea
	availRepos              []string
	filteredRepos           []string          // repos matching current filter (nil = show all)
	repoPaths               map[string]string // repo name → filesystem path
	selectedRepos           map[string]bool
	repoCursor              int
	repoScrollOffset        int                          // scroll window start index for visible repos
	whereFocus              whereFocus                   // focus axis on Step 2 (list / browse / create / continue)
	repoError               string                       // validation error ("Select at least one repo")
	repos                   map[string]config.RepoConfig // full repo configs for pipeline gate overrides
	models                  config.ModelConfig
	modelCursor             int
	modelFields             []string
	providerModels          map[string][]string            // provider name → model IDs (ordered)
	providerOrder           []string                       // provider names in display order
	phaseDefaults           map[string]string              // field name ("Research", etc.) → recommended model ID
	phaseProviderModels     map[string]map[string][]string // field → provider → eligible model IDs
	pipelinePreferences     map[string]config.PipelinePreference
	allModels               []string // flattened model list for cycling (all providers combined)
	exitCriteria            string
	inquirenessCursor       int
	inquirenessOptions      []string
	riskCursor              int
	riskOptions             []string
	pipelineCursor          int
	pipelineOptions         []string         // ["moonshot", "medium", "large"]
	branchInfos             []RepoBranchInfo // populated when entering branch warning step
	branchScreenIndex       int              // which off-default repo's screen we're currently on (0..len(offDefaultRepos)-1)
	branchOptionCursor      int              // 0 = start from default branch (recommended), 1 = start from current branch
	branchChoices           map[string]bool  // repo name -> useCurrentBranch (true = start from current HEAD)
	showBranchWarning       bool             // true when the dedicated branch-selection screen replaces the Where panel
	checkpointsCursor       int              // which row is focused (0-4)
	checkpoints             [5]bool          // toggle state for each checkpoint item
	result                  *WizardResult
	cancelled               bool
	width, height           int
	filePicker              FilePickerModel
	skillPicker             SkillPickerModel
	images                  []string       // temp image paths (ordered)
	imageCounter            int            // next image number
	imageTempDir            string         // temp directory for wizard images
	canPasteImages          bool           // true if clipboard paste is supported (macOS); also covers file paste
	attachments             []string       // temp attachment file paths (ordered)
	attachNames             []string       // original filenames for placeholders
	attachTempDir           string         // temp directory for wizard attachments
	summaryCursor           summaryField   // which field is highlighted on the Review screen
	riskAutoDetected        bool           // true if suggestRiskLevel returned non-medium (for provenance label)
	summaryEditing          bool           // true when inline editing a field on the Review screen
	riskManuallySet         bool           // true after user manually cycled risk value
	inquirenessManuallySet  bool           // true after user manually cycled inquireness value
	modelsManuallySet       bool           // true after user edited any model value
	checkpointsManuallySet  bool           // true after user toggled any checkpoint
	exitCriteriaManuallySet bool           // true after user edited exit criteria
	exitCriteriaOriginal    string         // original exitCriteria before editing (for cancel/revert)
	riskEditCursor          int            // cursor within risk pill selector (for cancel/revert)
	riskEditAutoDetected    bool           // original auto-detected flag while editing risk
	riskEditManuallySet     bool           // original manual-set flag while editing risk
	inquirenessEditCursor   int            // cursor within inquiry pill selector (for cancel/revert)
	inquiryEditManuallySet  bool           // original manual-set flag while editing inquiry
	dirPicker               DirPickerModel // browse-for-more picker
	dirPickerActive         bool           // true when picker overlay is showing
	browseRoot              string         // root selected via browse (consumed by app)

	// Create new repo state
	createRepoActive     bool            // true when in the create-new-repo sub-flow
	createRepoParentPath string          // parent dir selected via dir picker
	createRepoNameInput  textinput.Model // text input for repo name
	createRepoError      string          // validation error message
	createRepoPath       string          // full path of created repo (consumed by app like browseRoot)

	// Root picker state (for "Create new repo" parent-dir selection)
	workspaceRoots   []string // configured workspace root dirs
	rootPickerRoots  []string // workspace roots filtered to exclude git repos
	rootPickerActive bool     // true when root selection overlay is showing
	rootPickerCursor int      // highlighted root index

	// Provisional publishability
	provisionalPublishable bool // derived from selected repos; false if any lacks origin remote

	detectBranchesFn func(WizardModel) []RepoBranchInfo
	createRepoFn     func(parentPath, name string) error

	// Shared three-axis editor: embedded ConfigEditorModel used only while
	// summaryEditing is true on one of summaryFieldModels /
	// summaryFieldInquireness / summaryFieldCheckpoints. Rebuilt on every
	// editing-mode entry; the wizard remains source of truth for its own
	// modelCursor / checkpointsCursor / inquirenessCursor / manually-set flags.
	configCatalog PhaseModelCatalog
	configEditor  ConfigEditorModel
}

func pipelineProfileFromKey(key string) feature.PipelineProfile {
	profile, err := feature.ParsePipelineProfile(key)
	if err != nil {
		return feature.PipelineLarge
	}
	return profile
}

func pipelineConfigKeys(profile feature.PipelineProfile) []string {
	return []string{profile.ConfigKey()}
}

func NewWizardModel(availRepos []string, repoPaths map[string]string, repoConfigs map[string]config.RepoConfig, defaults config.DefaultsConfig, workspaceDir string, providerModels map[string][]string, providerOrder []string, phaseDefaults map[string]string, phaseModels map[string]map[string][]string, existingSlugs map[string]string, workspaceRoots []string) WizardModel {
	ni := textinput.New()
	ni.Placeholder = "Feature name"

	// Always focus nameInput initially (What step starts with name focused)
	ni.Focus()

	di := NewSimpleTextarea()
	di.Placeholder = "Brief description"
	di.SetHeight(4)

	ri := textinput.New()
	ri.Placeholder = "repo name (or select from list)"

	ei := NewSimpleTextarea()
	ei.Placeholder = "Exit criteria (leave empty for default)"
	ei.SetHeight(4)

	if workspaceDir == "" {
		workspaceDir, _ = os.Getwd()
	}

	var allModels []string
	for _, prov := range providerOrder {
		allModels = append(allModels, providerModels[prov]...)
	}
	// Shared phase-model catalog. Built before clamping so the same
	// provider-aware matching used by the picker also guards default clamping:
	// a multi-provider default persisted in "<provider>:<id>" routing form is
	// kept rather than discarded when option lists carry bare backend ids.
	configCatalog := PhaseModelCatalog{
		ProviderModels:      providerModels,
		ProviderOrder:       providerOrder,
		PhaseDefaults:       phaseDefaults,
		PhaseProviderModels: phaseModels,
		Fields:              append([]string(nil), phaseCatalogFields...),
	}

	canPaste := canPasteClipboardImage()
	var tempDir string
	var attachDir string
	if canPaste {
		tempDir, _ = os.MkdirTemp("", "agentic-wizard-images-*")
		attachDir, _ = os.MkdirTemp("", "agentic-wizard-attach-*")
		di.Placeholder = "Brief description (Ctrl+V to paste)"
	}

	// Resolve workspace roots to absolute paths.
	resolvedRoots := make([]string, 0, len(workspaceRoots))
	for _, r := range workspaceRoots {
		expanded := config.ExpandHome(r)
		if abs, err := filepath.Abs(expanded); err == nil {
			expanded = abs
		}
		resolvedRoots = append(resolvedRoots, expanded)
	}

	// Create-new-repo name input.
	crni := textinput.New()
	crni.Placeholder = "Repository name"
	crni.CharLimit = 100

	// Initialize checkpoint toggles from config defaults.
	var initCheckpoints [5]bool
	initCheckpoints[0] = defaults.Checkpoints.InquiryReview
	initCheckpoints[1] = defaults.Checkpoints.ResearchReview
	initCheckpoints[2] = defaults.Checkpoints.DesignReview
	initCheckpoints[3] = defaults.Checkpoints.PlanReview
	initCheckpoints[4] = defaults.Checkpoints.ManualPublish

	pipelineOptions := []string{
		feature.PipelineMedium.ConfigKey(),
		feature.PipelineLarge.ConfigKey(),
		feature.PipelineMoonshot.ConfigKey(),
	}
	pipelinePrefs := make(map[string]config.PipelinePreference, len(pipelineOptions))
	for _, profile := range pipelineOptions {
		pref := defaults.PreferenceForPipeline(profile)
		pref.Models.Research = configCatalog.ClampModelValue("Research", pref.Models.Research)
		pref.Models.Planning = configCatalog.ClampModelValue("Planning", pref.Models.Planning)
		pref.Models.Implementation = configCatalog.ClampModelValue("Implementation", pref.Models.Implementation)
		pref.Models.Review = configCatalog.ClampModelValue("Review", pref.Models.Review)
		pref.Models.KBBuild = configCatalog.ClampModelValue("KB Build", pref.Models.KBBuild)
		if pref.Inquireness == "" {
			pref.Inquireness = defaults.Inquireness
		}
		pipelinePrefs[profile] = pref
	}

	initialPipelineCursor := 1 // default to large
	initialPref := pipelinePrefs[pipelineOptions[initialPipelineCursor]]
	models := initialPref.Models
	inquirenessCursor := 1 // default to medium
	for i, opt := range []string{"none", "medium", "high"} {
		if opt == initialPref.Inquireness {
			inquirenessCursor = i
			break
		}
	}

	return WizardModel{
		step:                   wizardStepWhat,
		whatFocus:              0,
		nameInput:              ni,
		existingSlugs:          existingSlugs,
		descInput:              di,
		repoInput:              ri,
		exitInput:              ei,
		availRepos:             availRepos,
		filteredRepos:          availRepos,
		repoPaths:              repoPaths,
		selectedRepos:          make(map[string]bool),
		repos:                  repoConfigs,
		models:                 models,
		modelFields:            []string{"Research", "Planning", "Implementation", "Review", "KB Build"},
		providerModels:         providerModels,
		providerOrder:          providerOrder,
		phaseDefaults:          phaseDefaults,
		phaseProviderModels:    phaseModels,
		pipelinePreferences:    pipelinePrefs,
		allModels:              allModels,
		exitCriteria:           defaults.ExitCriteria,
		inquirenessOptions:     []string{"none", "medium", "high"},
		inquirenessCursor:      inquirenessCursor,
		checkpoints:            initCheckpoints,
		riskOptions:            []string{"low", "medium", "high"},
		riskCursor:             1, // default to medium
		pipelineOptions:        pipelineOptions,
		pipelineCursor:         initialPipelineCursor,
		filePicker:             NewFilePickerModel(repoPaths),
		skillPicker:            NewSkillPickerModel(),
		canPasteImages:         canPaste,
		imageTempDir:           tempDir,
		attachTempDir:          attachDir,
		summaryCursor:          summaryFieldRisk, // first editable field below divider
		createRepoNameInput:    crni,
		workspaceRoots:         resolvedRoots,
		provisionalPublishable: true, // default true until an unpublished repo is selected
		configCatalog:          configCatalog,
	}
}

func createGitRepo(parentPath, name string) error {
	targetPath := filepath.Join(parentPath, name)
	if err := os.MkdirAll(targetPath, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	initCmd := exec.Command("git", "init", targetPath)
	if out, err := initCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %s", strings.TrimSpace(string(out)))
	}
	commitCmd := exec.Command("git", "-C", targetPath, "commit", "--allow-empty", "-m", "Initial commit")
	commitCmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=agentic",
		"GIT_AUTHOR_EMAIL=agentic@local",
		"GIT_COMMITTER_NAME=agentic",
		"GIT_COMMITTER_EMAIL=agentic@local",
	)
	if out, err := commitCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initial commit: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (m WizardModel) Init() tea.Cmd {
	return textinput.Blink
}

func wizardDescriptionNewlineKey(msg tea.KeyPressMsg) bool {
	if key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+j", "alt+enter", "shift+enter"))) {
		return true
	}
	return msg.Code == tea.KeyEnter && msg.Mod.Contains(tea.ModShift)
}

func (m *WizardModel) focusWhatName() tea.Cmd {
	m.descInput.Blur()
	m.whatFocus = 0
	return m.nameInput.Focus()
}

func (m *WizardModel) focusWhatDescription() tea.Cmd {
	m.nameInput.Blur()
	m.whatFocus = 1
	return m.descInput.Focus()
}

func (m WizardModel) insertDescriptionNewline() (WizardModel, tea.Cmd) {
	var cmd tea.Cmd
	m.descInput, cmd = m.descInput.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return m, cmd
}

// isTextInputStep returns true if the current step has an active text input.
func (m WizardModel) isTextInputStep() bool {
	if m.step == wizardStepReview && m.summaryEditing &&
		m.summaryCursor == summaryFieldExitCriteria {
		return true
	}
	switch m.step {
	case wizardStepWhat:
		return true
	case wizardStepWhere:
		return true
	}
	return false
}

// summaryFields returns the ordered list of visible summary fields.
func (m WizardModel) summaryFields() []summaryField {
	return []summaryField{
		summaryFieldName,
		summaryFieldRepos,
		summaryFieldRisk,
		summaryFieldModels,
		summaryFieldInquireness,
		summaryFieldCheckpoints,
		summaryFieldExitCriteria,
	}
}

// nextSummaryField moves the cursor to the next visible field.
// Stops at the last field (no wrapping).
func (m *WizardModel) nextSummaryField() {
	fields := m.summaryFields()
	for i, f := range fields {
		if f == m.summaryCursor && i < len(fields)-1 {
			m.summaryCursor = fields[i+1]
			return
		}
	}
}

// prevSummaryField moves the cursor to the previous visible field.
// Stops at the first field (no wrapping).
func (m *WizardModel) prevSummaryField() {
	fields := m.summaryFields()
	for i, f := range fields {
		if f == m.summaryCursor && i > 0 {
			m.summaryCursor = fields[i-1]
			return
		}
	}
}

func (m *WizardModel) pipelineCheckpointOverrides(opt string) []feature.Checkpoints {
	if m.repos == nil {
		return nil
	}
	var contributors []string
	for repoName, sel := range m.selectedRepos {
		if !sel {
			continue
		}
		rc, ok := m.repos[repoName]
		if !ok || rc.PipelineGates == nil {
			continue
		}
		for _, key := range pipelineConfigKeys(pipelineProfileFromKey(opt)) {
			if _, exists := rc.PipelineGates[key]; exists {
				contributors = append(contributors, repoName)
				break
			}
		}
	}
	if len(contributors) == 0 {
		return nil
	}
	sort.Strings(contributors)

	overrides := make([]feature.Checkpoints, 0, len(contributors))
	for _, repoName := range contributors {
		rc := m.repos[repoName]
		for _, key := range pipelineConfigKeys(pipelineProfileFromKey(opt)) {
			if cp, ok := rc.PipelineGates[key]; ok {
				overrides = append(overrides, feature.ConfigCheckpointsToFeature(cp))
				break
			}
		}
	}
	return overrides
}

// mergedPipelineCheckpoints computes the effective gate checkpoints for the
// given pipeline profile across all selected repos.
func (m *WizardModel) mergedPipelineCheckpoints(opt string) (feature.Checkpoints, bool) {
	profile := pipelineProfileFromKey(opt)
	projection := profile.ProjectMergedGates(
		feature.DefaultCheckpointsForProfile(profile),
		m.pipelineCheckpointOverrides(opt),
		m.provisionalPublishable,
	)
	return projection.Checkpoints, projection.FromConfig
}

func (m *WizardModel) checkpointState() feature.Checkpoints {
	return feature.Checkpoints{
		InquiryReview:  m.checkpoints[0],
		ResearchReview: m.checkpoints[1],
		DesignReview:   m.checkpoints[2],
		PlanReview:     m.checkpoints[3],
		ManualPublish:  m.checkpoints[4],
	}
}

func (m *WizardModel) setCheckpointState(cp feature.Checkpoints) {
	m.checkpoints[0] = cp.InquiryReview
	m.checkpoints[1] = cp.ResearchReview
	m.checkpoints[2] = cp.DesignReview
	m.checkpoints[3] = cp.PlanReview
	m.checkpoints[4] = cp.ManualPublish
}

func (m *WizardModel) projectedPipelineCheckpoints(opt string) feature.GateProjection {
	profile := pipelineProfileFromKey(opt)
	return profile.ProjectMergedGates(
		feature.DefaultCheckpointsForProfile(profile),
		m.pipelineCheckpointOverrides(opt),
		m.provisionalPublishable,
	)
}

func (m *WizardModel) currentGateProjection() feature.GateProjection {
	return pipelineProfileFromKey(m.pipelineOptions[m.pipelineCursor]).ProjectGates(
		m.checkpointState(),
		m.provisionalPublishable,
	)
}

func gateLabels(projection feature.GateProjection) []string {
	gates := make([]string, 0, len(projection.Visible))
	for _, gate := range projection.Visible {
		switch gate {
		case feature.GateInquiryReview:
			if projection.Checkpoints.InquiryReview {
				gates = append(gates, "Inquiry review")
			}
		case feature.GateResearchReview:
			if projection.Checkpoints.ResearchReview {
				gates = append(gates, "Research review")
			}
		case feature.GateDesignReview:
			if projection.Checkpoints.DesignReview {
				gates = append(gates, "Design review")
			}
		case feature.GatePlanReview:
			if projection.Checkpoints.PlanReview {
				gates = append(gates, "Plan review")
			}
		case feature.GateManualPublish:
			if projection.Checkpoints.ManualPublish {
				gates = append(gates, "Publish review")
			}
		}
	}
	return gates
}

func availableGateLabels(projection feature.GateProjection) []string {
	gates := make([]string, 0, len(projection.Visible))
	for _, gate := range projection.Visible {
		switch gate {
		case feature.GateInquiryReview:
			gates = append(gates, "Inquiry review")
		case feature.GateResearchReview:
			gates = append(gates, "Research review")
		case feature.GateDesignReview:
			gates = append(gates, "Design review")
		case feature.GatePlanReview:
			gates = append(gates, "Plan review")
		case feature.GateManualPublish:
			gates = append(gates, "Publish review")
		}
	}
	return gates
}

// applyPipelineDefaults updates checkpoints to match the selected pipeline profile.
// It first applies the profile defaults, then checks for per-repo pipeline_gates
// config overrides (matching the logic used for card rendering in step 3).
func (m *WizardModel) applyPipelineDefaults() {
	opt := m.pipelineOptions[m.pipelineCursor]
	projection := m.projectedPipelineCheckpoints(opt)
	m.setCheckpointState(projection.Checkpoints)
	if !m.provisionalPublishable {
		m.checkpoints[4] = true // force ManualPublish when unpublished
	}
}

func (m *WizardModel) currentPipelineKey() string {
	if m.pipelineCursor < 0 || m.pipelineCursor >= len(m.pipelineOptions) {
		return ""
	}
	return m.pipelineOptions[m.pipelineCursor]
}

func (m *WizardModel) snapshotPipelinePreference(profile string) {
	if profile == "" {
		return
	}
	if m.pipelinePreferences == nil {
		m.pipelinePreferences = make(map[string]config.PipelinePreference)
	}
	m.pipelinePreferences[profile] = config.PipelinePreference{
		Models:      m.models,
		Inquireness: m.inquirenessOptions[m.inquirenessCursor],
	}
}

func (m *WizardModel) applyPipelinePreference(profile string) {
	pref, ok := m.pipelinePreferences[profile]
	if !ok {
		return
	}
	pref.Models.Research = m.configCatalog.ClampModelValue("Research", pref.Models.Research)
	pref.Models.Planning = m.configCatalog.ClampModelValue("Planning", pref.Models.Planning)
	pref.Models.Implementation = m.configCatalog.ClampModelValue("Implementation", pref.Models.Implementation)
	pref.Models.Review = m.configCatalog.ClampModelValue("Review", pref.Models.Review)
	pref.Models.KBBuild = m.configCatalog.ClampModelValue("KB Build", pref.Models.KBBuild)
	m.models = pref.Models

	for i, opt := range m.inquirenessOptions {
		if opt == pref.Inquireness {
			m.inquirenessCursor = i
			return
		}
	}
	m.inquirenessCursor = 1
}

// virtualConfigFeature builds a throwaway *feature.Feature carrying the
// wizard's current three-axis state (models, inquireness, checkpoints).
// Used to seed a freshly-constructed ConfigEditorModel on editing-mode entry.
//
// Re-entrancy + crash recovery: pure function; no persisted state and no
// side effects, so identical wizard state yields an equal *feature.Feature.
// Process crash is irrelevant — nothing is written to disk.
func (m *WizardModel) virtualConfigFeature() *feature.Feature {
	inq := feature.InquirenessMedium
	if m.inquirenessCursor >= 0 && m.inquirenessCursor < len(m.inquirenessOptions) {
		inq = feature.Inquireness(m.inquirenessOptions[m.inquirenessCursor])
	}
	return &feature.Feature{
		Pipeline:    pipelineProfileFromKey(m.currentPipelineKey()),
		Models:      m.models,
		Inquireness: inq,
		Checkpoints: feature.Checkpoints{
			InquiryReview:  m.checkpoints[0],
			ResearchReview: m.checkpoints[1],
			DesignReview:   m.checkpoints[2],
			PlanReview:     m.checkpoints[3],
			ManualPublish:  m.checkpoints[4],
		},
	}
}

// activeAxisBounds returns [start, end] rowCursor range for the active config
// axis. Used by the delegation block to axis-clamp the editor's rowCursor
// after each delegated Update so wrap-at-bounds navigation does not cross
// axes.
func (m *WizardModel) activeAxisBounds() (int, int) {
	switch m.summaryCursor {
	case summaryFieldModels:
		return 0, m.configEditor.modelsCount() - 1
	case summaryFieldInquireness:
		return m.configEditor.inquirenessRow(), m.configEditor.inquirenessRow()
	case summaryFieldCheckpoints:
		return m.configEditor.checkpointsStart(), m.configEditor.lastRow()
	}
	return 0, 0
}

// wizardSubCursor returns the wizard sub-cursor for the active config axis
// (0 for Inquireness; m.modelCursor for Models; m.checkpointsCursor for
// Checkpoints).
func (m *WizardModel) wizardSubCursor() int {
	switch m.summaryCursor {
	case summaryFieldModels:
		return m.modelCursor
	case summaryFieldInquireness:
		return 0
	case summaryFieldCheckpoints:
		return m.checkpointsCursor
	}
	return 0
}

// syncConfigEditorFromWizard copies m.models / m.inquirenessCursor /
// m.checkpoints / m.provisionalPublishable and the active-axis sub-cursor
// into m.configEditor. Called before every delegated Update AND at every
// Review-view render entry for the three config axes so the editor
// reflects current wizard state.
func (m *WizardModel) syncConfigEditorFromWizard() {
	projection := m.currentGateProjection()
	m.configEditor.models = m.models
	if m.inquirenessCursor >= 0 && m.inquirenessCursor < len(m.inquirenessOptions) {
		m.configEditor.inquireness = feature.Inquireness(m.inquirenessOptions[m.inquirenessCursor])
	}
	m.configEditor.pipeline = pipelineProfileFromKey(m.currentPipelineKey())
	m.configEditor.checkpoints = projection.Checkpoints
	m.configEditor.provisionalPublishable = m.provisionalPublishable
	start, _ := m.activeAxisBounds()
	m.configEditor.rowCursor = start + m.wizardSubCursor()
}

// syncWizardFromConfigEditor copies the editor's post-Update internal state
// back into wizard fields. Uses the editor's raw internal `checkpoints`
// field — NOT `Snapshot()` — so the wizard's pre-Phase-3 semantics where
// m.checkpoints[4] retains its init value when !provisionalPublishable are
// preserved (Snapshot() forces ManualPublish=true).
func (m *WizardModel) syncWizardFromConfigEditor() {
	m.models = m.configEditor.models
	m.inquirenessCursor = indexOfInquireness(m.configEditor.inquireness, m.inquirenessOptions)
	m.setCheckpointState(m.configEditor.checkpoints)
	start, _ := m.activeAxisBounds()
	sub := m.configEditor.rowCursor - start
	switch m.summaryCursor {
	case summaryFieldModels:
		m.modelCursor = sub
	case summaryFieldCheckpoints:
		m.checkpointsCursor = sub
	}
}

// clampConfigEditorToActiveAxis restores m.configEditor.rowCursor to within
// [start, end] for the active axis after a delegated Update. Preserves the
// wizard's clamp-at-bounds cursor contract against the editor's
// wrap-at-bounds default. `previousSubCursor` is the wizard sub-cursor
// captured BEFORE the delegated Update — used as the clamp fallback so the
// cursor returns to the last valid sub-position.
func (m *WizardModel) clampConfigEditorToActiveAxis(previousSubCursor int) {
	start, end := m.activeAxisBounds()
	if m.configEditor.rowCursor < start || m.configEditor.rowCursor > end {
		m.configEditor.rowCursor = start + previousSubCursor
		if m.configEditor.rowCursor < start {
			m.configEditor.rowCursor = start
		}
		if m.configEditor.rowCursor > end {
			m.configEditor.rowCursor = end
		}
	}
}

// indexOfInquireness maps a feature.Inquireness value back to its position
// in opts (the wizard's inquirenessOptions slice). Defaults to 1 (medium).
func indexOfInquireness(v feature.Inquireness, opts []string) int {
	for i, opt := range opts {
		if feature.Inquireness(opt) == v {
			return i
		}
	}
	if len(opts) > 1 {
		return 1
	}
	return 0
}

// handleSummaryEditing processes key events while inline editing is active on the Review screen.
func (m WizardModel) handleSummaryEditing(msg tea.KeyMsg) (WizardModel, tea.Cmd) {
	switch m.summaryCursor {
	case summaryFieldRisk:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("left"))):
			if m.riskCursor > 0 {
				m.riskCursor--
				m.riskManuallySet = true
				m.riskAutoDetected = false
			}
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("right"))):
			if m.riskCursor < len(m.riskOptions)-1 {
				m.riskCursor++
				m.riskManuallySet = true
				m.riskAutoDetected = false
			}
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.riskCursor = m.riskEditCursor
			m.riskAutoDetected = m.riskEditAutoDetected
			m.riskManuallySet = m.riskEditManuallySet
			m.summaryEditing = false
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			m.summaryEditing = false
			return m, nil
		}
		return m, nil

	case summaryFieldInquireness, summaryFieldModels, summaryFieldCheckpoints:
		// Lifecycle keys owned by the wizard: enter collapses (always); esc
		// on Inquireness reverts the cursor + manually-set flag, esc on
		// Models/Checkpoints collapses preserving cycled/toggled values.
		if key.Matches(msg, key.NewBinding(key.WithKeys("enter"))) {
			m.summaryEditing = false
			return m, nil
		}
		if key.Matches(msg, key.NewBinding(key.WithKeys("esc"))) {
			if m.summaryCursor == summaryFieldInquireness {
				m.inquirenessCursor = m.inquirenessEditCursor
				m.inquirenessManuallySet = m.inquiryEditManuallySet
			}
			m.summaryEditing = false
			return m, nil
		}
		// Checkpoints: reshape tab/shift+tab -> space so the editor's space
		// toggle fires instead of the editor's cross-axis Tab jump. Preserves
		// the wizard's Tab-toggles contract.
		var outMsg tea.Msg = msg
		if m.summaryCursor == summaryFieldCheckpoints {
			if key.Matches(msg, key.NewBinding(key.WithKeys("tab"))) ||
				key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))) {
				outMsg = tea.KeyPressMsg{Code: ' ', Text: " "}
			}
		}
		// Pre-Phase-3 Models-axis contract: modelsManuallySet flipped
		// unconditionally on any cycling key (tab/right/left/shift+tab),
		// even when the cycle is a no-op because opts is empty. Matched
		// here so existing tests pass without modification.
		forceModelsManuallySet := m.summaryCursor == summaryFieldModels &&
			(key.Matches(msg, key.NewBinding(key.WithKeys("tab"))) ||
				key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))) ||
				key.Matches(msg, key.NewBinding(key.WithKeys("right"))) ||
				key.Matches(msg, key.NewBinding(key.WithKeys("left"))))
		// Sync editor from wizard, capture before-snapshot, delegate Update,
		// axis-clamp the editor's rowCursor, sync wizard back. Manually-set
		// flag is flipped when the resulting snapshot differs (or always
		// for the Models cycle-key case above).
		prevSub := m.wizardSubCursor()
		m.syncConfigEditorFromWizard()
		before := m.configEditor.Snapshot()
		m.configEditor, _ = m.configEditor.Update(outMsg)
		m.clampConfigEditorToActiveAxis(prevSub)
		after := m.configEditor.Snapshot()
		m.syncWizardFromConfigEditor()
		changed := before != after
		switch m.summaryCursor {
		case summaryFieldModels:
			if changed || forceModelsManuallySet {
				m.modelsManuallySet = true
			}
		case summaryFieldInquireness:
			if changed {
				m.inquirenessManuallySet = true
			}
		case summaryFieldCheckpoints:
			if changed {
				m.checkpointsManuallySet = true
			}
		}
		return m, nil

	case summaryFieldExitCriteria:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			m.exitCriteria = m.exitCriteriaOriginal
			m.exitInput.Blur()
			m.summaryEditing = false
			return m, nil
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+j"))):
			// Insert newline in exit criteria textarea
			var cmd tea.Cmd
			m.exitInput, cmd = m.exitInput.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return m, cmd
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			val := strings.TrimSpace(m.exitInput.Value())
			if val != "" {
				m.exitCriteria = val
			}
			m.exitCriteriaManuallySet = true
			m.exitInput.Blur()
			m.summaryEditing = false
			return m, nil
		default:
			var cmd tea.Cmd
			m.exitInput, cmd = m.exitInput.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m WizardModel) Update(msg tea.Msg) (WizardModel, tea.Cmd) {
	// Dir picker active: delegate all messages
	if m.dirPickerActive {
		var cmd tea.Cmd
		m.dirPicker, cmd = m.dirPicker.Update(msg)
		if m.dirPicker.IsDone() && !m.dirPicker.IsCancelled() {
			selected := m.dirPicker.SelectedPath()
			if m.dirPicker.mode == dirPickerModeCreateRepo {
				m.createRepoParentPath = selected
				m.createRepoActive = true
				m.createRepoNameInput.Focus()
				m.createRepoError = ""
			} else {
				m.browseRoot = selected
			}
			m.dirPickerActive = false
		} else if m.dirPicker.IsCancelled() {
			m.dirPickerActive = false
		}
		return m, cmd
	}

	// Root picker overlay: root selection, then name input.
	if m.rootPickerActive {
		if msg, ok := msg.(tea.KeyPressMsg); ok {
			if m.createRepoActive {
				// Name input inside the overlay.
				switch {
				case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
					m, cmd := m.createNewRepo()
					if !m.createRepoActive {
						// Creation succeeded — close the overlay
						m.rootPickerActive = false
					}
					return m, cmd
				case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
					// Back to root selection
					m.createRepoActive = false
					m.createRepoParentPath = ""
					m.createRepoError = ""
					m.createRepoNameInput.Reset()
					return m, nil
				default:
					var cmd tea.Cmd
					m.createRepoNameInput, cmd = m.createRepoNameInput.Update(msg)
					m.createRepoError = ""
					return m, cmd
				}
			}
			// Root selection.
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter")), key.NewBinding(key.WithKeys("tab"))):
				if len(m.rootPickerRoots) > 0 {
					m.createRepoParentPath = m.rootPickerRoots[m.rootPickerCursor]
					m.createRepoActive = true
					m.createRepoNameInput.Focus()
					m.createRepoError = ""
					return m, textinput.Blink
				}
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				m.rootPickerActive = false
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("up"))),
				key.Matches(msg, key.NewBinding(key.WithKeys("k"))):
				if m.rootPickerCursor > 0 {
					m.rootPickerCursor--
				}
				return m, nil
			case key.Matches(msg, key.NewBinding(key.WithKeys("down"))),
				key.Matches(msg, key.NewBinding(key.WithKeys("j"))):
				if m.rootPickerCursor < len(m.rootPickerRoots)-1 {
					m.rootPickerCursor++
				}
				return m, nil
			}
		}
		return m, nil
	}

	// Create-new-repo name input active: route keys to the text input
	if m.createRepoActive && m.createRepoParentPath != "" {
		if msg, ok := msg.(tea.KeyPressMsg); ok {
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
				return m.createNewRepo()
			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				m.createRepoActive = false
				m.createRepoParentPath = ""
				m.createRepoError = ""
				m.createRepoNameInput.Reset()
				return m, nil
			default:
				var cmd tea.Cmd
				m.createRepoNameInput, cmd = m.createRepoNameInput.Update(msg)
				m.createRepoError = "" // clear error on typing
				return m, cmd
			}
		}
	}

	switch msg := msg.(type) {
	case ImagePastedMsg:
		m.imageCounter++
		m.images = append(m.images, msg.Path)
		placeholder := fmt.Sprintf("[Image #%d]", len(m.images))
		m.descInput.InsertString(placeholder)
		return m, nil

	case ImagePasteFailedMsg:
		return m, nil

	case TextPastedMsg:
		m.descInput.InsertString(msg.Text)
		return m, nil

	case FilesPastedMsg:
		m.attachments = append(m.attachments, msg.Paths...)
		m.attachNames = append(m.attachNames, msg.Names...)
		for _, name := range msg.Names {
			m.descInput.InsertString(fmt.Sprintf("[%s]", name))
		}
		return m, nil

	case tea.KeyPressMsg:

		// When file picker is active, route navigation keys there first.
		// Regular characters are NOT consumed — they flow to the text input,
		// then the prefix is derived from the input value.
		if m.filePicker.IsActive() {
			fp, selected, consumed := m.filePicker.Update(msg)
			m.filePicker = fp
			if selected != "" {
				if m.filePicker.IsActive() {
					// Tab drilled into a directory — update text input to show the path
					m.updateAtPrefix(selected)
				} else {
					// Completed selection — replace @... with final path
					m.replaceAtMention(selected)
				}
			}
			if consumed {
				return m, nil
			}
		}

		if m.skillPicker.IsActive() {
			sp, selected, consumed := m.skillPicker.Update(msg)
			m.skillPicker = sp
			if selected != "" {
				m.replaceSlashMention(selected)
			}
			if consumed {
				return m, nil
			}
		}

		// When the dedicated branch-selection screen is showing, one repo is
		// asked about at a time. The user toggles between two options
		// (default branch / current branch), Enter saves the choice and
		// advances to the next off-default repo; on the last screen it
		// continues to Step 3. Esc backs up one screen (or dismisses the
		// whole branch flow on the first screen).
		if m.showBranchWarning && m.step == wizardStepWhere {
			rows := m.offDefaultRepos()
			switch {
			case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+c"))):
				m.cancelled = true
				return m, nil

			case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))),
				key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+j"))):
				// Save the choice for the current screen's repo.
				if m.branchScreenIndex >= 0 && m.branchScreenIndex < len(rows) {
					if m.branchChoices == nil {
						m.branchChoices = make(map[string]bool)
					}
					m.branchChoices[rows[m.branchScreenIndex].Name] = m.branchOptionCursor == 1
				}
				// More repos to ask about? Advance to the next one.
				if m.branchScreenIndex < len(rows)-1 {
					m.branchScreenIndex++
					// Pre-seed the option cursor from any previously-saved
					// choice for this repo (lets back-and-forth navigation
					// remember what was picked).
					next := rows[m.branchScreenIndex].Name
					if m.branchChoices[next] {
						m.branchOptionCursor = 1
					} else {
						m.branchOptionCursor = 0
					}
					return m, nil
				}
				// Last repo: continue to Step 3.
				return m.advance()

			case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
				if m.branchScreenIndex > 0 {
					m.branchScreenIndex--
					prev := rows[m.branchScreenIndex].Name
					if m.branchChoices[prev] {
						m.branchOptionCursor = 1
					} else {
						m.branchOptionCursor = 0
					}
					return m, nil
				}
				// Already on the first screen: dismiss the branch flow.
				m.showBranchWarning = false
				m.whereFocus = whereFocusList
				m.repoInput.Focus()
				return m, textinput.Blink

			case key.Matches(msg, key.NewBinding(key.WithKeys("up"))),
				key.Matches(msg, key.NewBinding(key.WithKeys("down"))),
				key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
				// Two options, vertically stacked: ↑/↓/tab all flip between
				// them. Minimal set - users don't need to memorize alternates.
				m.branchOptionCursor = 1 - m.branchOptionCursor
				return m, nil
			}
			// Block all other keys (typing into repo filter, etc.)
			return m, nil
		}

		// Handle summary inline editing mode
		if m.step == wizardStepReview && m.summaryEditing {
			return m.handleSummaryEditing(msg)
		}

		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+c"))):
			m.cancelled = true
			return m, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+d"))):
			// Ctrl+D = "I'm done" shortcut. On the Where step, jumps straight
			// to advance (Continue) from anywhere - so users who get used to
			// the chip picker can skip the Tab dance.
			if m.step == wizardStepWhere && !m.showBranchWarning {
				return m.advance()
			}
			return m, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			if m.filePicker.IsActive() {
				m.filePicker.Deactivate()
				return m, nil
			}
			if m.skillPicker.IsActive() {
				m.skillPicker.Deactivate()
				return m, nil
			}
			// Esc goes back; on first step goBack() cancels the wizard
			return m.goBack()

		case wizardDescriptionNewlineKey(msg):
			// On wizardStepWhat when description is focused, insert newline
			if m.step == wizardStepWhat && m.whatFocus == 1 {
				return m.insertDescriptionNewline()
			}
			if key.Matches(msg, key.NewBinding(key.WithKeys("shift+enter"))) ||
				msg.Mod.Contains(tea.ModShift) {
				return m, nil
			}
			// On other steps/cases, treat as advance — respect picker guards
			if m.filePicker.IsActive() {
				return m, nil
			}
			if m.skillPicker.IsActive() {
				return m, nil
			}
			return m.advance()

		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			if m.filePicker.IsActive() {
				return m, nil
			}
			if m.skillPicker.IsActive() {
				return m, nil
			}
			if msg.Mod.Contains(tea.ModAlt) {
				// Alt+enter on description: insert newline
				if m.step == wizardStepWhat && m.whatFocus == 1 {
					var cmd tea.Cmd
					m.descInput, cmd = m.descInput.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					return m, cmd
				}
			}
			if m.step == wizardStepReview {
				switch m.summaryCursor {
				case summaryFieldName:
					m.step = wizardStepWhat
					if m.whatFocus == 1 {
						cmd := m.descInput.Focus()
						return m, cmd
					}
					m.nameInput.Focus()
					return m, textinput.Blink
				case summaryFieldRepos:
					m.showBranchWarning = false
					m.step = wizardStepWhere
					m.whereFocus = whereFocusList
					m.repoInput.Focus()
					return m, textinput.Blink
				case summaryFieldRisk:
					m.summaryEditing = true
					m.riskEditCursor = m.riskCursor
					m.riskEditAutoDetected = m.riskAutoDetected
					m.riskEditManuallySet = m.riskManuallySet
					return m, nil
				case summaryFieldInquireness:
					m.summaryEditing = true
					m.inquirenessEditCursor = m.inquirenessCursor
					m.inquiryEditManuallySet = m.inquirenessManuallySet
					m.configEditor = NewConfigEditorModel(m.virtualConfigFeature(), m.configCatalog, m.provisionalPublishable)
					return m, nil
				case summaryFieldModels:
					m.summaryEditing = true
					m.modelCursor = 0
					m.configEditor = NewConfigEditorModel(m.virtualConfigFeature(), m.configCatalog, m.provisionalPublishable)
					return m, nil
				case summaryFieldCheckpoints:
					m.summaryEditing = true
					m.checkpointsCursor = 0
					m.configEditor = NewConfigEditorModel(m.virtualConfigFeature(), m.configCatalog, m.provisionalPublishable)
					return m, nil
				case summaryFieldExitCriteria:
					m.summaryEditing = true
					m.exitCriteriaOriginal = m.exitCriteria
					m.exitInput.SetValue(m.exitCriteria)
					m.exitInput.SetWidth(reviewEditorInputWidth(m.wizardPanelWidth()))
					m.exitInput.SetHeight(4)
					cmd := m.exitInput.Focus()
					return m, cmd
				default:
					return m, nil
				}
			}
			// On wizardStepWhat with name focused, move to description first
			if m.step == wizardStepWhat && m.whatFocus == 0 {
				return m, m.focusWhatDescription()
			}
			// Where step: Enter is contextual based on the focus axis.
			if m.step == wizardStepWhere {
				switch m.whereFocus {
				case whereFocusList:
					return m.addFocusedRepo()
				case whereFocusBrowse:
					return m.openBrowsePicker()
				case whereFocusCreate:
					return m.openRootPicker()
				case whereFocusContinue:
					return m.advance()
				}
			}
			// wizardStepWhat (description) — advance to Where
			return m.advance()

		case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
			if m.filePicker.IsActive() {
				return m, nil
			}
			if m.step == wizardStepWhat {
				// If description focused, switch to name
				if m.whatFocus == 1 {
					return m, m.focusWhatName()
				}
				// Already on name (first step) — no-op
				return m, nil
			}
			return m.goBack()

		case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
			if m.filePicker.IsActive() {
				return m, nil
			}
			if m.step == wizardStepWhat {
				// Toggle whatFocus between 0 (name) and 1 (description)
				if m.whatFocus == 0 {
					return m, m.focusWhatDescription()
				}
				return m, m.focusWhatName()
			}
			if m.step == wizardStepWhere {
				// Tab cycles list -> Browse -> Create (if visible) -> Continue -> list.
				m.whereFocus = m.cycleWhereFocusForward()
				m.handleWhereFocusEntry()
				return m, nil
			}
			if m.step == wizardStepReview && !m.summaryEditing {
				// Tab does nothing on review when not editing a field
				return m, nil
			}
			return m, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("up"))) ||
			key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+p"))) ||
			(!m.isTextInputStep() && key.Matches(msg, key.NewBinding(key.WithKeys("k")))):
			if m.filePicker.IsActive() {
				return m, nil
			}
			// On wizardStepWhat with description focused, leave the field only
			// from its top visual line; otherwise let textarea move the cursor.
			if m.step == wizardStepWhat && m.whatFocus == 1 {
				if m.descInput.atFirstVisualLine() {
					return m, m.focusWhatName()
				}
				break
			}
			if m.step == wizardStepWhere {
				avail := m.availableRepos()
				switch m.whereFocus {
				case whereFocusList:
					if m.repoCursor > 0 {
						m.repoCursor--
						if m.repoCursor < m.repoScrollOffset {
							m.repoScrollOffset = m.repoCursor
						}
					}
				case whereFocusBrowse:
					// Jump into the list at its last row.
					if len(avail) > 0 {
						m.whereFocus = whereFocusList
						m.repoCursor = len(avail) - 1
						const maxVisibleRepos = 12
						if m.repoCursor >= m.repoScrollOffset+maxVisibleRepos {
							m.repoScrollOffset = m.repoCursor - maxVisibleRepos + 1
						}
						m.handleWhereFocusEntry()
					}
				case whereFocusCreate:
					m.whereFocus = whereFocusBrowse
					m.handleWhereFocusEntry()
				case whereFocusContinue:
					if m.whereCreateVisible() {
						m.whereFocus = whereFocusCreate
					} else {
						m.whereFocus = whereFocusBrowse
					}
					m.handleWhereFocusEntry()
				}
			}
			if m.step == wizardStepPipeline && m.pipelineCursor > 0 {
				m.snapshotPipelinePreference(m.currentPipelineKey())
				m.pipelineCursor--
				m.applyPipelinePreference(m.currentPipelineKey())
				m.applyPipelineDefaults()
			}
			if m.step == wizardStepReview {
				m.prevSummaryField()
			}
			return m, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("down"))) ||
			key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+n"))) ||
			(!m.isTextInputStep() && key.Matches(msg, key.NewBinding(key.WithKeys("j")))):
			if m.filePicker.IsActive() {
				return m, nil
			}
			if m.step == wizardStepWhat && m.whatFocus == 0 {
				return m, m.focusWhatDescription()
			}
			// On wizardStepWhat with description focused, let textarea handle cursor
			if m.step == wizardStepWhat && m.whatFocus == 1 {
				break
			}
			if m.step == wizardStepWhere {
				avail := m.availableRepos()
				switch m.whereFocus {
				case whereFocusList:
					if m.repoCursor < m.maxRepoCursor() {
						m.repoCursor++
						const maxVisibleRepos = 12
						if m.repoCursor >= m.repoScrollOffset+maxVisibleRepos {
							m.repoScrollOffset = m.repoCursor - maxVisibleRepos + 1
						}
					} else if len(avail) > 0 {
						// At bottom of list - step into the action axis.
						m.whereFocus = whereFocusBrowse
						m.handleWhereFocusEntry()
					}
				case whereFocusBrowse:
					if m.whereCreateVisible() {
						m.whereFocus = whereFocusCreate
					} else {
						m.whereFocus = whereFocusContinue
					}
					m.handleWhereFocusEntry()
				case whereFocusCreate:
					m.whereFocus = whereFocusContinue
					m.handleWhereFocusEntry()
				case whereFocusContinue:
					// Stay at the bottom.
				}
			}
			if m.step == wizardStepPipeline && m.pipelineCursor < len(m.pipelineOptions)-1 {
				m.snapshotPipelinePreference(m.currentPipelineKey())
				m.pipelineCursor++
				m.applyPipelinePreference(m.currentPipelineKey())
				m.applyPipelineDefaults()
			}
			if m.step == wizardStepReview {
				m.nextSummaryField()
			}
			return m, nil

		case key.Matches(msg, key.NewBinding(key.WithKeys("left"))):
			// Otherwise fall through to text input

		case key.Matches(msg, key.NewBinding(key.WithKeys("right"))):
			// Otherwise fall through to text input

		case key.Matches(msg, key.NewBinding(key.WithKeys("G"))):
			if m.step == wizardStepReview {
				return m.advance()
			}
			// On other steps, fall through to text input

		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+v"))):
			// Clipboard paste only on What step with description focused and paste supported
			if m.step == wizardStepWhat && m.whatFocus == 1 && m.canPasteImages {
				imgDir := m.imageTempDir
				attachDir := m.attachTempDir
				nextIdx := m.imageCounter + 1
				return m, func() tea.Msg {
					// Try image first
					path, err := saveClipboardImage(imgDir, nextIdx)
					if err == nil {
						return ImagePastedMsg{Path: path}
					}
					// Try file paste
					paths, names, ferr := saveClipboardFiles(attachDir)
					if ferr == nil && len(paths) > 0 {
						return FilesPastedMsg{Paths: paths, Names: names}
					}
					// Fall back to text paste
					text, terr := getClipboardText()
					if terr == nil && text != "" {
						return TextPastedMsg{Text: text}
					}
					return ImagePasteFailedMsg{}
				}
			}
			return m, nil
		}

		// Forward to active input
		var cmd tea.Cmd
		switch m.step {
		case wizardStepWhat:
			if m.whatFocus == 0 {
				m.nameInput, cmd = m.nameInput.Update(msg)
			} else {
				prevVal := m.descInput.Value()
				m.descInput, cmd = m.descInput.Update(msg)
				newVal := m.descInput.Value()
				// Track @ prefix for file picker
				if m.filePicker.IsActive() {
					idx := strings.LastIndex(newVal, "@")
					if idx < 0 {
						m.filePicker.Deactivate()
					} else {
						m.filePicker.SetPrefix(newVal[idx+1:])
					}
				} else if len(newVal) > len(prevVal) && strings.HasSuffix(newVal, "@") {
					m.filePicker.Activate("")
				}
			}
		case wizardStepWhere:
			// Space is kept as a synonym for Enter on the list axis (muscle-
			// memory back-compat from the prior toggle-by-space UX).
			if key.Matches(msg, key.NewBinding(key.WithKeys("space"))) && m.whereFocus == whereFocusList {
				return m.addFocusedRepo()
			}
			// Backspace on an EMPTY filter input removes the last selected
			// chip (matches the chip/pill picker convention from GitHub /
			// Linear / Slack). When the filter input has text, fall through
			// so Backspace edits the filter normally.
			if key.Matches(msg, key.NewBinding(key.WithKeys("backspace"))) &&
				m.whereFocus == whereFocusList &&
				m.repoInput.Value() == "" &&
				m.hasAnyRepoSelected() {
				chips := m.selectedReposInOrder()
				last := chips[len(chips)-1]
				delete(m.selectedRepos, last)
				m.recomputeProvisionalPublishability()
				m.repoError = ""
				m.handleWhereFocusEntry()
				return m, nil
			}
			prevVal := m.repoInput.Value()
			m.repoInput, cmd = m.repoInput.Update(msg)
			if m.repoInput.Value() != prevVal {
				m.updateFilteredRepos()
			}
		}
		return m, cmd
	}

	// Forward non-key messages
	var cmd tea.Cmd
	switch m.step {
	case wizardStepWhat:
		if m.whatFocus == 0 {
			m.nameInput, cmd = m.nameInput.Update(msg)
		} else {
			m.descInput, cmd = m.descInput.Update(msg)
		}
	case wizardStepWhere:
		m.repoInput, cmd = m.repoInput.Update(msg)
	case wizardStepReview:
		if m.summaryEditing && m.summaryCursor == summaryFieldExitCriteria {
			m.exitInput, cmd = m.exitInput.Update(msg)
		}
	}
	return m, cmd
}

// updateFilteredRepos recomputes filteredRepos based on repoInput value.
// When filter is empty, filteredRepos == availRepos (show all).
// Resets repoCursor to 0 on every change.
func (m *WizardModel) updateFilteredRepos() {
	filter := strings.ToLower(m.repoInput.Value())
	if filter == "" {
		m.filteredRepos = m.availRepos
	} else {
		m.filteredRepos = nil
		for _, r := range m.availRepos {
			if strings.Contains(strings.ToLower(r), filter) {
				m.filteredRepos = append(m.filteredRepos, r)
			}
		}
	}
	m.repoCursor = 0
	m.repoScrollOffset = 0
}

// openBrowsePicker creates and opens the DirPickerModel for browse.
func (m WizardModel) openBrowsePicker() (WizardModel, tea.Cmd) {
	m.dirPicker = NewDirPickerModel()
	m.dirPickerActive = true
	cmds := []tea.Cmd{m.dirPicker.Init()}
	if m.width > 0 && m.height > 0 {
		m.dirPicker, _ = m.dirPicker.Update(tea.WindowSizeMsg{
			Width:  m.width,
			Height: m.height,
		})
	}
	return m, tea.Batch(cmds...)
}

// IsPickerActive returns true when the browse picker is showing.
func (m WizardModel) IsPickerActive() bool {
	return m.dirPickerActive
}

// PickerView returns the picker's rendered content for overlay stacking.
func (m WizardModel) PickerView() string {
	if !m.dirPickerActive {
		return ""
	}
	return m.dirPicker.ViewContent()
}

// IsRootPickerActive returns true when the root selection overlay is showing.
func (m WizardModel) IsRootPickerActive() bool {
	return m.rootPickerActive
}

// RootPickerView renders the root selection overlay content.
// First shows root list selection, then the selected parent plus a name input.
func (m WizardModel) RootPickerView() string {
	modalWidth := max(min(m.width-4, 72), 28)

	var content strings.Builder
	var title string

	if m.createRepoActive {
		// Name input.
		title = "Create New Repo"
		content.WriteString("\n")
		parentDisplay := m.createRepoParentPath
		if parentDisplay == "" {
			parentDisplay = "(not set)"
		} else {
			parentDisplay = compactHome(parentDisplay)
		}
		content.WriteString("  Parent: " + parentDisplay + "\n")
		content.WriteString("  Name:   " + m.createRepoNameInput.View() + "\n")
		if m.createRepoError != "" {
			content.WriteString("  " + WarningStyle.Render(m.createRepoError) + "\n")
		}
		content.WriteString("\n")
		content.WriteString(KeyHelpStyle.Render("enter create  •  esc back"))
	} else {
		// Root selection.
		title = "Select Parent Directory"
		content.WriteString("\n")
		for i, root := range m.rootPickerRoots {
			cursor := "  "
			if i == m.rootPickerCursor {
				cursor = "> "
			}
			content.WriteString(cursor + compactHome(root) + "\n")
		}
		content.WriteString("\n")
		content.WriteString(KeyHelpStyle.Render("enter select  •  esc cancel"))
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBrand).
		Padding(0, 1).
		Width(modalWidth).
		Render(content.String())

	box = renderBorderTitle(box, title, TitleStyle)

	return box
}

// ConsumeBrowseRoot returns and clears the pending browse root path.
func (m *WizardModel) ConsumeBrowseRoot() string {
	root := m.browseRoot
	m.browseRoot = ""
	return root
}

// openRootPicker opens the workspace root selection overlay.
func (m WizardModel) openRootPicker() (WizardModel, tea.Cmd) {
	// Filter out workspace roots that are themselves git repos
	m.rootPickerRoots = nil
	for _, root := range m.workspaceRoots {
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			m.rootPickerRoots = append(m.rootPickerRoots, root)
		}
	}
	m.rootPickerActive = true
	m.rootPickerCursor = 0
	return m, nil
}

// createNewRepo validates the name input and creates a new git repo.
func (m WizardModel) createNewRepo() (WizardModel, tea.Cmd) {
	name := strings.TrimSpace(m.createRepoNameInput.Value())
	if name == "" {
		m.createRepoError = "Name cannot be empty"
		return m, nil
	}
	// Reject names with filesystem-unsafe characters
	if strings.ContainsAny(name, "/\\:*?\"<>|\x00") {
		m.createRepoError = "Name contains invalid characters"
		return m, nil
	}
	targetPath := filepath.Join(m.createRepoParentPath, name)
	if _, err := os.Stat(targetPath); err == nil {
		m.createRepoError = "Directory already exists"
		return m, nil
	}
	createRepo := m.createRepoFn
	if createRepo == nil {
		createRepo = createGitRepo
	}
	if err := createRepo(m.createRepoParentPath, name); err != nil {
		m.createRepoError = fmt.Sprintf("Failed to create repo: %v", err)
		return m, nil
	}

	// Set up state for app consumption
	m.createRepoPath = targetPath
	m.createRepoActive = false
	m.createRepoNameInput.Reset()

	// Deselect all existing repos — new repo will be sole selection after refresh
	for k := range m.selectedRepos {
		delete(m.selectedRepos, k)
	}
	m.provisionalPublishable = false // new repo always unpublished
	return m, nil
}

// ConsumeCreateRepoPath returns and clears the pending create-repo path.
// Only clears parent path and error when a repo was actually created.
func (m *WizardModel) ConsumeCreateRepoPath() string {
	p := m.createRepoPath
	if p == "" {
		return ""
	}
	m.createRepoPath = ""
	m.createRepoParentPath = ""
	m.createRepoError = ""
	return p
}

// Advance moves the wizard to the next step. Used by the app to auto-advance
// after programmatic state changes (e.g. after auto-selecting a newly created repo).
func (m *WizardModel) Advance() {
	advanced, _ := m.advance()
	*m = advanced
}

// AutoSelectCreatedRepo sets the newly created repo as the sole selection
// using path-based key resolution (same contract as RefreshRepos).
func (m *WizardModel) AutoSelectCreatedRepo(createdPath string, repoPaths map[string]string) {
	// Build inverse map: path → key
	pathToKey := make(map[string]string, len(repoPaths))
	for k, p := range repoPaths {
		pathToKey[p] = k
	}
	// Clear all selections
	for k := range m.selectedRepos {
		delete(m.selectedRepos, k)
	}
	if repoKey, ok := pathToKey[createdPath]; ok {
		m.selectedRepos[repoKey] = true
	}
	m.provisionalPublishable = false // new repo always has no remote
}

// recomputeProvisionalPublishability recalculates whether all selected repos
// have an origin remote. If any selected repo lacks one, provisionalPublishable
// is set to false.
func (m *WizardModel) recomputeProvisionalPublishability() {
	if m.createRepoActive || m.createRepoPath != "" {
		m.provisionalPublishable = false
		return
	}
	m.provisionalPublishable = true
	for repo, sel := range m.selectedRepos {
		if !sel {
			continue
		}
		if p, ok := m.repoPaths[repo]; ok {
			if !git.HasOriginRemote(p) {
				m.provisionalPublishable = false
				return
			}
		}
	}
}

// maxRepoCursor returns the maximum valid repoCursor index within the
// available-repo list. Chip-picker layout: only the in-list rows count
// (Browse / Create / Continue live on their own focus axis).
func (m WizardModel) maxRepoCursor() int {
	n := len(m.availableRepos())
	if n == 0 {
		return 0
	}
	return n - 1
}

// availableRepos returns the filtered repos that are not yet selected.
// Selected repos appear as chips at the top and are excluded from the
// list so a repo cannot be "added" twice.
func (m WizardModel) availableRepos() []string {
	out := make([]string, 0, len(m.filteredRepos))
	for _, r := range m.filteredRepos {
		if !m.selectedRepos[r] {
			out = append(out, r)
		}
	}
	return out
}

// selectedReposInOrder returns selected repos in availRepos order (stable
// for chip rendering and undo-via-backspace).
func (m WizardModel) selectedReposInOrder() []string {
	out := make([]string, 0, len(m.selectedRepos))
	for _, r := range m.availRepos {
		if m.selectedRepos[r] {
			out = append(out, r)
		}
	}
	return out
}

// hasAnyRepoSelected returns true when at least one repo is selected.
func (m WizardModel) hasAnyRepoSelected() bool {
	for _, v := range m.selectedRepos {
		if v {
			return true
		}
	}
	return false
}

// whereCreateVisible returns true when the "Create new repo" action is
// available (workspace roots are configured).
func (m WizardModel) whereCreateVisible() bool {
	return len(m.workspaceRoots) > 0
}

// cycleWhereFocusForward returns the next focus position for Tab on the
// Where step. Cycle: list → browse → create (if visible) → continue → list.
func (m WizardModel) cycleWhereFocusForward() whereFocus {
	switch m.whereFocus {
	case whereFocusList:
		return whereFocusBrowse
	case whereFocusBrowse:
		if m.whereCreateVisible() {
			return whereFocusCreate
		}
		return whereFocusContinue
	case whereFocusCreate:
		return whereFocusContinue
	case whereFocusContinue:
		return whereFocusList
	}
	return whereFocusList
}

// addFocusedRepo adds the repo at repoCursor (in availableRepos space) to
// the selected set. Used by Enter / Space on the list axis. Acts as a
// no-op when there is no repo to add at the cursor.
func (m WizardModel) addFocusedRepo() (WizardModel, tea.Cmd) {
	avail := m.availableRepos()
	if len(avail) == 0 || m.repoCursor < 0 || m.repoCursor >= len(avail) {
		return m, nil
	}
	repo := avail[m.repoCursor]
	m.selectedRepos[repo] = true
	m.createRepoActive = false
	m.createRepoParentPath = ""
	m.createRepoPath = ""
	m.createRepoNameInput.Reset()
	m.recomputeProvisionalPublishability()
	m.repoError = ""
	// After removing the picked repo from the list, the cursor naturally
	// points at the next one. Clamp / scroll-adjust via the focus helper.
	m.handleWhereFocusEntry()
	return m, nil
}

// handleWhereFocusEntry runs side effects whenever m.whereFocus has just
// changed: it focuses the filter input when the list axis is active and
// blurs it otherwise, and clamps repoCursor to the current availableRepos
// length so the cursor never points past the end.
func (m *WizardModel) handleWhereFocusEntry() {
	if m.whereFocus == whereFocusList {
		m.repoInput.Focus()
	} else {
		m.repoInput.Blur()
	}
	availLen := len(m.availableRepos())
	if availLen == 0 {
		m.repoCursor = 0
		m.repoScrollOffset = 0
		return
	}
	if m.repoCursor > availLen-1 {
		m.repoCursor = availLen - 1
	}
	const maxVisibleRepos = 12
	if m.repoCursor < m.repoScrollOffset {
		m.repoScrollOffset = m.repoCursor
	}
	if m.repoCursor >= m.repoScrollOffset+maxVisibleRepos {
		m.repoScrollOffset = m.repoCursor - maxVisibleRepos + 1
	}
}

// renderRepoChips lays out selected repos as removable pills. Wraps at the
// available width; first chip is rendered with a bracket border. Style is
// kept simple (no per-chip cursor) - chips are removed via Backspace on
// an empty filter input, not by direct focus.
func (m WizardModel) renderRepoChips(chips []string, maxWidth int) string {
	chipStyle := lipgloss.NewStyle().
		Foreground(colorActive).
		Bold(true)
	xStyle := MutedStyle
	var lines []string
	var current strings.Builder
	currentWidth := 0
	for _, name := range chips {
		pill := chipStyle.Render("[ "+name+" ") + xStyle.Render("✕") + chipStyle.Render(" ]")
		// 4 visual chars of overhead per pill (brackets + spaces) + name + x
		pillWidth := len(name) + 6
		if currentWidth > 0 && currentWidth+pillWidth+1 > maxWidth {
			lines = append(lines, current.String())
			current.Reset()
			currentWidth = 0
		}
		if currentWidth > 0 {
			current.WriteString(" ")
			currentWidth++
		}
		current.WriteString(pill)
		currentWidth += pillWidth
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return "  " + strings.Join(lines, "\n  ")
}

// renderActionRow renders one of the "+ Browse" / "+ Create" rows with
// focus-sensitive styling.
func (m WizardModel) renderActionRow(label string, focused bool) string {
	if focused {
		return SelectedRowStyle.Render("▸" + label)
	}
	return " " + MutedStyle.Render(label)
}

// renderBranchSelectionScreen renders the dedicated branch-selection screen
// shown when off-default branches are detected. The screen asks about one repo
// at a time so the user only ever has two choices and one action (Enter).
// After answering for one repo, Enter moves to the next repo's screen; on the
// last, Enter advances to Step 3. Esc backs up one screen, or dismisses the
// flow on the first.
//
// The recommended option (default branch) is pre-selected.
func (m WizardModel) renderBranchSelectionScreen(headingStyle lipgloss.Style, maxWidth int) string {
	rows := m.offDefaultRepos()
	if len(rows) == 0 {
		return ""
	}
	idx := m.branchScreenIndex
	if idx < 0 {
		idx = 0
	}
	if idx >= len(rows) {
		idx = len(rows) - 1
	}
	bi := rows[idx]

	var c strings.Builder
	bodyStyle := lipgloss.NewStyle().Foreground(colorSubtext)

	// Header: which repo + how many are left.
	progress := ""
	if len(rows) > 1 {
		progress = MutedStyle.Render(fmt.Sprintf("   (%d of %d)", idx+1, len(rows)))
	}
	repoHeadingStyle := headingStyle.Bold(true)
	c.WriteString(repoHeadingStyle.Render(bi.Name) + headingStyle.Render(" is on a non-default branch.") + progress + "\n\n")

	// Decision guidance before the choices. "Current branch" means current
	// HEAD: committed branch history, not dirty working-tree changes.
	guidanceStyle := bodyStyle.Width(maxWidth)
	c.WriteString(guidanceStyle.Render("Start from "+SuccessStyle.Render(bi.DefaultBranch)+" for a clean feature branch.") + "\n")
	c.WriteString(guidanceStyle.Render("Use "+WarningStyle.Render(bi.CurrentBranch)+" only if this feature depends on commits already there.") + "\n\n")

	// Two full-width action buttons. The selected button uses the same active
	// color treatment as the Step 2 Continue CTA, while the unselected button
	// stays quiet so the screen reads as a branch-base decision rather than a
	// warning form.
	useCurrent := m.branchOptionCursor == 1
	c.WriteString(renderBranchSourceButton(
		fmt.Sprintf("Start from %s", bi.DefaultBranch),
		"Recommended",
		"Create the feature branch from the default codebase.",
		!useCurrent,
		maxWidth,
	) + "\n\n")
	c.WriteString(renderBranchSourceButton(
		fmt.Sprintf("Start from %s", bi.CurrentBranch),
		"Include current branch commits",
		"Build on work that already exists on this branch.",
		useCurrent,
		maxWidth,
	) + "\n")

	return c.String()
}

func renderBranchSourceButton(title, primary, secondary string, selected bool, width int) string {
	if width < 32 {
		width = 32
	}

	borderColor := colorOverlay
	border := lipgloss.RoundedBorder()
	titleStyle := MutedStyle
	primaryStyle := lipgloss.NewStyle().Bold(true).Foreground(colorSubtext)
	secondaryStyle := MutedStyle
	if selected {
		borderColor = colorActive
		border = lipgloss.ThickBorder()
		titleStyle = lipgloss.NewStyle().Foreground(colorActive).Bold(true)
		primaryStyle = lipgloss.NewStyle().Bold(true).Foreground(colorActive)
		secondaryStyle = lipgloss.NewStyle().Foreground(colorSubtext)
	}

	prefix := "  "
	if selected {
		prefix = "› "
	}
	content := primaryStyle.Render(prefix+primary) + "\n" +
		secondaryStyle.Render("  "+secondary)

	box := lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(width).
		Render(content)

	title = truncateString(title, width-6)
	return renderBorderTitle(box, title, titleStyle)
}

// renderContinueButton renders the primary Continue CTA. It is bright
// brand-colored and bordered when enabled, dim when no repos are selected.
// The outer width matches the filter-input box (maxWidth) so the CTA lines
// up with the input above it.
func (m WizardModel) renderContinueButton(count int, maxWidth int) string {
	enabled := count > 0
	focused := m.whereFocus == whereFocusContinue
	var label string
	switch {
	case count == 0:
		label = "Continue (select a repo to enable)"
	case count == 1:
		label = "Continue with 1 repo →"
	default:
		label = fmt.Sprintf("Continue with %d repos →", count)
	}

	if maxWidth < 24 {
		maxWidth = 24
	}
	// The style's Width is the OUTER width (lipgloss includes border+padding
	// inside the given width when a border/padding is set), so passing
	// maxWidth here makes the button align exactly with the filter input.
	style := lipgloss.NewStyle().
		Padding(0, 1).
		Width(maxWidth).
		Align(lipgloss.Center)

	switch {
	case !enabled:
		style = style.Foreground(colorOverlay).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorOverlay)
	case focused:
		// Focused state shifts to colorActive (teal) so it stands out clearly
		// against the brand-purple unfocused state — matching how
		// SelectedRowStyle teals the highlighted list/action rows.
		style = style.Foreground(colorActive).
			Bold(true).
			Border(lipgloss.ThickBorder()).
			BorderForeground(colorActive)
	default:
		style = style.Foreground(colorBrand).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBrand)
	}
	return style.Render(label)
}

// RefreshRepos updates the wizard's repo list after a workspace root was added.
// Preserves existing selections by stable path identity so that collision-induced
// re-keying (e.g. "myrepo" → "rootA/myrepo") does not drop user selections.
func (m *WizardModel) RefreshRepos(availRepos []string, repoPaths map[string]string, repoConfigs map[string]config.RepoConfig) {
	// Build path → new key lookup for remapping.
	newPathToKey := make(map[string]string, len(repoPaths))
	for key, path := range repoPaths {
		newPathToKey[path] = key
	}

	// Remap selectedRepos: find each old key's path, then the new key for that path.
	remapped := make(map[string]bool, len(m.selectedRepos))
	for oldKey, sel := range m.selectedRepos {
		if !sel {
			continue
		}
		if path := m.repoPaths[oldKey]; path != "" {
			if newKey, ok := newPathToKey[path]; ok {
				remapped[newKey] = true
				continue
			}
		}
		// Fallback: keep old key (e.g. if repoPaths was nil)
		remapped[oldKey] = true
	}
	m.selectedRepos = remapped

	m.availRepos = availRepos
	m.repoPaths = repoPaths
	m.repos = repoConfigs
	m.filePicker.UpdateRepoRoots(repoPaths)
	m.updateFilteredRepos()
	// Clamp cursor to valid range (including browse + create items)
	if m.repoCursor > m.maxRepoCursor() {
		m.repoCursor = m.maxRepoCursor()
	}
	m.recomputeProvisionalPublishability()
}

// SetWorkspaceRoots updates the available workspace root directories.
func (m *WizardModel) SetWorkspaceRoots(roots []string) {
	// Resolve all roots to absolute paths so "." and other relative paths
	// display and behave correctly in the root picker.
	resolved := make([]string, 0, len(roots))
	for _, r := range roots {
		expanded := config.ExpandHome(r)
		if abs, err := filepath.Abs(expanded); err == nil {
			expanded = abs
		}
		resolved = append(resolved, expanded)
	}
	m.workspaceRoots = resolved
	if m.repoCursor > m.maxRepoCursor() {
		m.repoCursor = m.maxRepoCursor()
	}
}

// attachmentSummary returns a short description of attached images and files,
// e.g. "2 image(s) · 1 file(s)". Returns "" if nothing is attached.
func (m WizardModel) attachmentSummary() string {
	var parts []string
	if len(m.images) > 0 {
		parts = append(parts, fmt.Sprintf("%d image(s)", len(m.images)))
	}
	if len(m.attachments) > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s)", len(m.attachments)))
	}
	return strings.Join(parts, " · ")
}

// detectBranches checks the current and default branches for all selected repos.
func (m WizardModel) detectBranches() []RepoBranchInfo {
	if m.detectBranchesFn != nil {
		return m.detectBranchesFn(m)
	}
	var infos []RepoBranchInfo
	for _, name := range m.availRepos {
		if !m.selectedRepos[name] {
			continue
		}
		repoPath := m.repoPaths[name]
		if repoPath == "" {
			continue
		}
		cur := git.CurrentBranch(repoPath)
		def := git.DefaultBranch(repoPath)
		infos = append(infos, RepoBranchInfo{
			Name:          name,
			CurrentBranch: cur,
			DefaultBranch: def,
			IsOffDefault:  cur != "" && def != "" && cur != def,
		})
	}
	return infos
}

// defaultBranchName returns the default branch name from the first branch info entry, or "main" as fallback.
func (m WizardModel) defaultBranchName() string {
	for _, bi := range m.branchInfos {
		if bi.DefaultBranch != "" {
			return bi.DefaultBranch
		}
	}
	return "main"
}

func (m WizardModel) hasOffDefaultRepos() bool {
	for _, bi := range m.branchInfos {
		if bi.IsOffDefault {
			return true
		}
	}
	return false
}

// offDefaultRepos returns the branch info rows for repos that are NOT on
// their default branch. Order matches the order of m.branchInfos (which
// itself is derived from m.availRepos via detectBranches), so navigation
// in the branch-selection screen is stable.
func (m WizardModel) offDefaultRepos() []RepoBranchInfo {
	out := make([]RepoBranchInfo, 0, len(m.branchInfos))
	for _, bi := range m.branchInfos {
		if bi.IsOffDefault {
			out = append(out, bi)
		}
	}
	return out
}

// useCurrentBranchPerRepo returns a copy of m.branchChoices restricted to
// the repos currently known to be off-default. Default-branch repos are
// not in the map (they always use the default base). Returns nil if no
// off-default repos exist, so consumers can rely on the absence of the
// map as a signal that no per-repo decision was made.
func (m WizardModel) useCurrentBranchPerRepo() map[string]bool {
	rows := m.offDefaultRepos()
	if len(rows) == 0 {
		return nil
	}
	out := make(map[string]bool, len(rows))
	for _, bi := range rows {
		out[bi.Name] = m.branchChoices[bi.Name]
	}
	return out
}

func (m WizardModel) goBack() (WizardModel, tea.Cmd) {
	switch m.step {
	case wizardStepWhat:
		// First step — cancel wizard
		m.cancelled = true
		return m, nil

	case wizardStepWhere:
		m.repoInput.Blur()
		m.step = wizardStepWhat
		// Restore focus to the appropriate input based on whatFocus
		if m.whatFocus == 1 {
			cmd := m.descInput.Focus()
			return m, cmd
		}
		m.nameInput.Focus()
		return m, textinput.Blink

	case wizardStepPipeline:
		m.showBranchWarning = false
		m.step = wizardStepWhere
		m.whereFocus = whereFocusList
		m.repoInput.Focus()
		return m, textinput.Blink

	case wizardStepReview:
		m.step = wizardStepPipeline
		return m, nil
	}
	return m, nil
}

func (m WizardModel) advance() (WizardModel, tea.Cmd) {
	switch m.step {
	case wizardStepWhat:
		// Validate: name must be non-empty
		name := strings.TrimSpace(m.nameInput.Value())
		if name == "" {
			return m, nil
		}
		// Check for duplicate slug
		slug := feature.Slugify(name)
		if existingName, ok := m.existingSlugs[slug]; ok {
			m.nameWarning = fmt.Sprintf("A feature named %q already exists", existingName)
			return m, nil
		}
		m.nameWarning = ""

		// Advance to Where
		m.step = wizardStepWhere
		m.whereFocus = whereFocusList
		m.nameInput.Blur()
		m.descInput.Blur()
		m.repoInput.Focus()
		return m, textinput.Blink

	case wizardStepWhere:
		// Validate at least one repo is selected
		hasSelected := false
		for _, v := range m.selectedRepos {
			if v {
				hasSelected = true
				break
			}
		}
		if !hasSelected && !m.createRepoActive {
			m.repoError = "Select at least one repo"
			m.whereFocus = whereFocusList
			m.repoInput.Focus()
			return m, textinput.Blink
		}
		m.repoError = ""
		m.repoInput.Blur()

		if !m.showBranchWarning {
			// First Continue: detect branches for selected repos.
			m.branchInfos = m.detectBranches()
			if m.hasOffDefaultRepos() {
				// Initialize per-repo choices to "use default" (false).
				m.branchChoices = make(map[string]bool)
				for _, bi := range m.branchInfos {
					if bi.IsOffDefault {
						m.branchChoices[bi.Name] = false
					}
				}
				// Start on the first off-default repo's screen with the
				// recommended (default-branch) option selected.
				m.branchScreenIndex = 0
				m.branchOptionCursor = 0
				m.showBranchWarning = true
				return m, nil // stay on Where step, show the dedicated branch screen
			}
		}

		// Either no off-default repos (first Enter) or second Enter after warning
		// Auto-suggest pipeline and risk for the pipeline step
		m.riskCursor = suggestRiskLevel(m.descInput.Value(), m.nameInput.Value())
		m.riskAutoDetected = m.riskCursor != 1

		m.step = wizardStepPipeline
		return m, nil

	case wizardStepPipeline:
		// Pipeline selected — apply pipeline defaults (including per-repo config overrides)
		// before advancing to the review screen.
		m.applyPipelineDefaults()
		m.skillPicker.LoadItems(m.repoPaths, m.selectedRepos)
		m.summaryCursor = summaryFieldRisk
		m.step = wizardStepReview
		return m, nil

	case wizardStepReview:
		// Build WizardResult — same logic as current wizardStepConfirm
		var repos []string
		for _, r := range m.availRepos {
			if m.selectedRepos[r] {
				repos = append(repos, r)
			}
		}
		projection := m.currentGateProjection()
		perRepo := m.useCurrentBranchPerRepo()
		anyCurrent := false
		for _, v := range perRepo {
			if v {
				anyCurrent = true
				break
			}
		}
		m.result = &WizardResult{
			Name:                    strings.TrimSpace(m.nameInput.Value()),
			Description:             strings.TrimSpace(m.descInput.Value()),
			Images:                  m.images,
			Attachments:             m.attachments,
			Repos:                   repos,
			Models:                  m.models,
			ExitCriteria:            m.exitCriteria,
			Inquireness:             m.inquirenessOptions[m.inquirenessCursor],
			RiskLevel:               m.riskOptions[m.riskCursor],
			Pipeline:                pipelineProfileFromKey(m.pipelineOptions[m.pipelineCursor]),
			UseCurrentBranch:        anyCurrent,
			UseCurrentBranchPerRepo: perRepo,
			Checkpoints:             projection.Checkpoints,
		}
		return m, nil
	}
	return m, nil
}

func (m *WizardModel) modelOptionsForField(field string) []string {
	if pm, ok := m.phaseProviderModels[field]; ok {
		var opts []string
		for _, prov := range m.providerOrder {
			opts = append(opts, pm[prov]...)
		}
		if len(opts) > 0 {
			return opts
		}
	}
	return m.allModels
}

// providerModelsForField returns the per-provider model map for a specific
// phase field, falling back to the global providerModels when no phase-specific
// filtering is available.
func (m *WizardModel) providerModelsForField(field string) map[string][]string {
	if pm, ok := m.phaseProviderModels[field]; ok && len(pm) > 0 {
		return pm
	}
	return m.providerModels
}

func (m *WizardModel) cycleModel() {
	field := m.modelFields[m.modelCursor]
	opts, providers := m.configCatalog.FlatOptionsForField(field)
	if len(opts) == 0 {
		return
	}
	current := m.getModelField(field)
	nextIdx := 0
	for i := range opts {
		if m.configCatalog.MatchesModelValue(providers[i], opts[i], current) {
			nextIdx = (i + 1) % len(opts)
			break
		}
	}
	m.setModelField(field, opts[nextIdx])
}

func (m *WizardModel) cycleModelReverse() {
	field := m.modelFields[m.modelCursor]
	opts, providers := m.configCatalog.FlatOptionsForField(field)
	if len(opts) == 0 {
		return
	}
	current := m.getModelField(field)
	nextIdx := len(opts) - 1
	for i := range opts {
		if m.configCatalog.MatchesModelValue(providers[i], opts[i], current) {
			nextIdx = (i - 1 + len(opts)) % len(opts)
			break
		}
	}
	m.setModelField(field, opts[nextIdx])
}

func (m *WizardModel) getModelField(field string) string {
	switch field {
	case "Research":
		return m.models.Research
	case "Planning":
		return m.models.Planning
	case "Implementation":
		return m.models.Implementation
	case "Review":
		return m.models.Review
	case "KB Build":
		return m.models.KBBuild
	}
	return ""
}

func (m *WizardModel) setModelField(field, value string) {
	switch field {
	case "Research":
		m.models.Research = value
	case "Planning":
		m.models.Planning = value
	case "Implementation":
		m.models.Implementation = value
	case "Review":
		m.models.Review = value
	case "KB Build":
		m.models.KBBuild = value
	}
}

func (m *WizardModel) modelProviderGroups(field string) []modelProviderGroup {
	providerModels := m.providerModelsForField(field)
	var groups []modelProviderGroup
	for _, prov := range m.providerOrder {
		if models := providerModels[prov]; len(models) > 0 {
			groups = append(groups, modelProviderGroup{Name: prov, Models: models})
		}
	}
	if len(groups) > 0 {
		return groups
	}
	if opts := m.modelOptionsForField(field); len(opts) > 0 {
		return []modelProviderGroup{{Name: "Available", Models: opts}}
	}
	return nil
}

func splitProviderModel(value string) (provider, model string) {
	provider, model, ok := strings.Cut(value, ":")
	if ok && provider != "" && model != "" {
		return provider, model
	}
	return "", value
}

func (m *WizardModel) modelAssignmentSummary(field string) string {
	value := m.getModelField(field)
	if value == "" {
		return "—"
	}

	provider, model := splitProviderModel(value)
	if provider != "" {
		return provider + " / " + model
	}

	groups := m.modelProviderGroups(field)
	showProvider := len(groups) > 1
	for _, group := range groups {
		for _, opt := range group.Models {
			if opt != value {
				continue
			}
			if showProvider && group.Name != "Available" {
				return group.Name + " / " + value
			}
			return value
		}
	}
	return value
}

func wrapRenderedTokens(tokens []string, width int) []string {
	if len(tokens) == 0 {
		return nil
	}
	if width <= 0 {
		return []string{strings.Join(tokens, " ")}
	}

	var lines []string
	current := tokens[0]
	for _, token := range tokens[1:] {
		candidate := current + " " + token
		if lipgloss.Width(candidate) > width {
			lines = append(lines, current)
			current = token
			continue
		}
		current = candidate
	}
	lines = append(lines, current)
	return lines
}

func wrapRenderedTokensWithPrefix(prefix string, tokens []string, width int) []string {
	if len(tokens) == 0 {
		return []string{prefix}
	}

	prefixWidth := lipgloss.Width(prefix)
	if width <= prefixWidth+1 {
		return []string{prefix + strings.Join(tokens, " ")}
	}

	contPrefix := strings.Repeat(" ", prefixWidth)
	avail := width - prefixWidth

	var lines []string
	current := ""
	linePrefix := prefix
	for _, token := range tokens {
		candidate := token
		if current != "" {
			candidate = current + " " + token
		}
		if current != "" && lipgloss.Width(candidate) > avail {
			lines = append(lines, linePrefix+current)
			current = token
			linePrefix = contPrefix
			continue
		}
		current = candidate
	}
	lines = append(lines, linePrefix+current)
	return lines
}

func reviewEditorContentWidth(width int) int {
	contentWidth := width - 4
	if contentWidth < 44 {
		contentWidth = 44
	}
	return contentWidth
}

func reviewEditorInputWidth(panelWidth int) int {
	inputWidth := reviewEditorContentWidth(panelWidth-10) - 2
	if inputWidth < 20 {
		inputWidth = 20
	}
	return inputWidth
}

func renderReviewEditorBox(title string, width int, content string) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorOverlay).
		Padding(0, 1).
		Width(reviewEditorContentWidth(width)).
		Render(content)
	return renderBorderTitle(box, title, lipgloss.NewStyle().Foreground(colorBrand))
}

// renderCheckpointsEditor is a thin back-compat shim over the shared
// ConfigEditorModel's Checkpoints renderer. Kept so existing wizard tests
// (TestWizardCheckpointHidingReviewEditor, TestWizardCheckpointVisibleWhenPublishable)
// continue to exercise the wizard's Review-view checkpoint rendering. New
// code paths call renderReviewEditorBox("Gates", …) directly.
func (m *WizardModel) renderCheckpointsEditor(width int) string {
	m.syncConfigEditorFromWizard()
	content := m.configEditor.renderCheckpointsBox(reviewEditorContentWidth(width))
	return renderReviewEditorBox("Gates", width, content)
}

func (m *WizardModel) renderExitCriteriaEditor(width int) string {
	return renderReviewEditorBox("Exit Criteria", width, m.exitInput.View())
}

func renderPillChoiceEditor(title string, width int, options []string, selected int, descs []string) string {
	selectedPillStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorBase).
		Background(colorActive).
		Padding(0, 1)
	optionStyle := lipgloss.NewStyle().Foreground(colorText)

	current := ""
	if selected >= 0 && selected < len(options) {
		current = options[selected]
	}

	lines := []string{
		MutedStyle.Render("Current:") + " " + SummarySelectedValueStyle.Render(current),
		"",
		MutedStyle.Render("Options"),
	}

	var pills []string
	for i, opt := range options {
		if i == selected {
			pills = append(pills, selectedPillStyle.Render(opt))
		} else {
			pills = append(pills, optionStyle.Render(opt))
		}
	}
	lines = append(lines, wrapRenderedTokens(pills, reviewEditorContentWidth(width))...)
	if selected >= 0 && selected < len(descs) && descs[selected] != "" {
		lines = append(lines, "", MutedStyle.Render(descs[selected]))
	}

	return renderReviewEditorBox(title, width, strings.Join(lines, "\n"))
}

// wizardPanelWidth returns the panel width for the current terminal size.
// Earlier steps stay relatively compact, while Pipeline and Review are allowed
// to expand more because they benefit from additional horizontal space.
func (m WizardModel) wizardPanelWidth() int {
	w := m.width
	if w < 40 {
		w = 80
	}

	minWidth := 60
	maxWidth := w - 8
	if maxWidth < minWidth {
		maxWidth = minWidth
	}

	ratio := 60
	hardMax := 110
	switch m.step {
	case wizardStepPipeline:
		ratio = 68
		hardMax = 132
	case wizardStepReview:
		ratio = 78
		hardMax = 160
	}

	pw := w * ratio / 100
	if pw < minWidth {
		pw = minWidth
	}
	if pw > hardMax {
		pw = hardMax
	}
	if pw > maxWidth {
		pw = maxWidth
	}

	return pw
}

// suggestRiskLevel returns a cursor index (0=low, 1=medium, 2=high) based on
// keyword analysis of the feature description and name. This provides an
// intelligent default that the user can override.
func suggestRiskLevel(description, name string) int {
	text := strings.ToLower(description + " " + name)

	highRiskKeywords := []string{
		"auth", "authentication", "authorization", "oauth", "jwt", "token",
		"payment", "billing", "charge", "invoice", "credit card",
		"migration", "schema", "database migration", "alter table",
		"breaking change", "breaking api", "deprecate",
		"new service", "microservice", "cross-service",
		"pii", "gdpr", "compliance", "encryption", "secret",
		"security", "vulnerability", "cve",
	}
	for _, kw := range highRiskKeywords {
		if strings.Contains(text, kw) {
			return 2 // high
		}
	}

	lowRiskKeywords := []string{
		"typo", "comment", "readme", "docs", "documentation",
		"rename", "refactor", "cleanup", "clean up",
		"config", "configuration", "formatting",
		"log message", "error message", "bump version",
	}
	for _, kw := range lowRiskKeywords {
		if strings.Contains(text, kw) {
			return 0 // low
		}
	}

	return 1 // medium (default)
}

// wizardContent returns the wizard panel box and footer without vertical padding.
func (m WizardModel) wizardContent() (contentBox, footer string) {
	stepProgress := m.renderStepProgress()
	titlePrefix := "New Feature  "

	panelWidth := m.wizardPanelWidth()
	inputBoxWidth := panelWidth - 6

	// Build the step content
	var c strings.Builder

	switch m.step {
	case wizardStepWhat:
		nameLabelStyle := LabelStyle
		descLabelStyle := MutedStyle
		mutedBorderColor := compat.AdaptiveColor{Light: lipgloss.Color("#b5b9c8"), Dark: lipgloss.Color("#45475a")}
		nameBorderColor := colorOverlay
		descBorderColor := compat.AdaptiveColor(mutedBorderColor)
		if m.whatFocus == 1 {
			nameLabelStyle = MutedStyle
			descLabelStyle = LabelStyle
			nameBorderColor = compat.AdaptiveColor(mutedBorderColor)
			descBorderColor = colorOverlay
		}

		c.WriteString(nameLabelStyle.Render("Name") + "\n")
		nameBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(nameBorderColor).
			Padding(0, 1).
			Width(inputBoxWidth).
			Render(m.nameInput.View())
		c.WriteString(nameBox + "\n")
		if m.nameWarning != "" {
			c.WriteString(WarningStyle.Render("  \u26a0 "+m.nameWarning) + "\n")
		}

		c.WriteString("\n")

		// Description input with hints
		var descHint string
		if m.canPasteImages {
			descHint = " (@ files \u00b7 Ctrl+V paste)"
		} else {
			descHint = " (@ for file paths)"
		}
		c.WriteString(descLabelStyle.Render("Description") + MutedStyle.Render(descHint) + "\n")
		descBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(descBorderColor).
			Padding(0, 1).
			Width(inputBoxWidth).
			Render(m.descInput.View())
		c.WriteString(descBox + "\n")
		if summary := m.attachmentSummary(); summary != "" {
			c.WriteString(MutedStyle.Render(fmt.Sprintf("  %s attached", summary)) + "\n")
		}
		// File picker dropdown
		if m.filePicker.IsActive() {
			c.WriteString(m.filePicker.View() + "\n")
		}
	case wizardStepWhere:
		// LabelStyle has a hard Width(12) cap, so any heading or repo-name
		// label that might exceed 12 characters has to use a plain inline
		// style. Matches Step 3's heading pattern.
		headingStyle := lipgloss.NewStyle().Foreground(colorSubtext)

		// When off-default branches were detected and the user hit Continue,
		// the picker is replaced entirely by a dedicated branch-selection
		// screen — one decision per off-default repo, no competing CTAs.
		if m.showBranchWarning {
			c.WriteString(m.renderBranchSelectionScreen(headingStyle, inputBoxWidth))
			break
		}

		// Chips: selected repos shown as removable pills at the top.
		chips := m.selectedReposInOrder()
		if len(chips) > 0 {
			c.WriteString(headingStyle.Render("Building in") + "\n")
			c.WriteString(m.renderRepoChips(chips, inputBoxWidth) + "\n\n")
		} else {
			c.WriteString(headingStyle.Render("Pick one or more repos") + "\n\n")
		}

		// Filter input. The border brightens when focus is on the list axis.
		mutedBorder := compat.AdaptiveColor{Light: lipgloss.Color("#b5b9c8"), Dark: lipgloss.Color("#45475a")}
		activeBorder := compat.AdaptiveColor(mutedBorder)
		if m.whereFocus == whereFocusList {
			activeBorder = colorOverlay
		}
		repoInputBox := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(activeBorder).
			Padding(0, 1).
			Width(inputBoxWidth).
			Render(m.repoInput.View())
		c.WriteString(repoInputBox + "\n")

		// Show match count when filtering.
		avail := m.availableRepos()
		if filter := m.repoInput.Value(); filter != "" {
			c.WriteString(MutedStyle.Render(fmt.Sprintf("  %d of %d available", len(avail), len(m.availRepos)-len(chips))) + "\n")
		} else {
			c.WriteString("\n")
		}

		// Render visible repos with scroll window.
		const maxVisibleRepos = 12
		visibleStart := m.repoScrollOffset
		visibleEnd := visibleStart + maxVisibleRepos
		if visibleEnd > len(avail) {
			visibleEnd = len(avail)
		}

		if visibleStart > 0 {
			c.WriteString(MutedStyle.Render(fmt.Sprintf("  ↑ %d more", visibleStart)) + "\n")
		}
		listFocused := m.whereFocus == whereFocusList
		for i := visibleStart; i < visibleEnd; i++ {
			r := avail[i]
			label := fmt.Sprintf(" %s", r)
			if listFocused && i == m.repoCursor {
				c.WriteString(SelectedRowStyle.Render("▸" + label))
			} else {
				c.WriteString(" " + label)
			}
			c.WriteString("\n")
		}
		if visibleEnd < len(avail) {
			c.WriteString(MutedStyle.Render(fmt.Sprintf("  ↓ %d more", len(avail)-visibleEnd)) + "\n")
		}
		if len(avail) == 0 {
			switch {
			case m.repoInput.Value() != "" && len(m.filteredRepos) == 0:
				c.WriteString(MutedStyle.Render("  No repos match filter.") + "\n")
			case len(m.availRepos) > 0 && len(chips) == len(m.availRepos):
				c.WriteString(MutedStyle.Render("  All repos added.") + "\n")
			case len(m.availRepos) == 0:
				c.WriteString(MutedStyle.Render("  No repos configured - browse or create one below.") + "\n")
			default:
				c.WriteString(MutedStyle.Render("  No repos to add.") + "\n")
			}
		}

		// Action rows: Browse, then Create (if workspace roots exist).
		c.WriteString("\n")
		c.WriteString(m.renderActionRow(" + Browse for more...", m.whereFocus == whereFocusBrowse) + "\n")
		if m.whereCreateVisible() {
			c.WriteString(m.renderActionRow(" + Create new repo...", m.whereFocus == whereFocusCreate) + "\n")
		}

		// Continue button - the explicit CTA.
		c.WriteString("\n")
		c.WriteString(m.renderContinueButton(len(chips), inputBoxWidth) + "\n")

		// Validation error
		if m.repoError != "" {
			c.WriteString("\n" + WarningStyle.Render("  ✗ "+m.repoError) + "\n")
		}

	case wizardStepPipeline:
		c.WriteString(lipgloss.NewStyle().Foreground(colorSubtext).Render("How should this feature be built?") + "\n\n")

		type profileInfo struct {
			key     string
			icon    string
			name    string
			tagline string
		}
		profiles := []profileInfo{
			{feature.PipelineMedium.ConfigKey(), pipelineProfileIcon(feature.PipelineMedium), "Medium", "Best for quick fixes and small well-defined tasks"},
			{feature.PipelineLarge.ConfigKey(), pipelineProfileIcon(feature.PipelineLarge), "Large", "Best for regular feature work and multi-step tasks"},
			{feature.PipelineMoonshot.ConfigKey(), pipelineProfileIcon(feature.PipelineMoonshot), "Moonshot", "Best for moonshots, critical systems, and complex architecture"},
		}

		cardWidth := inputBoxWidth - 4 // leave room for cursor prefix

		for pi, opt := range m.pipelineOptions {
			var info profileInfo
			for _, p := range profiles {
				if p.key == opt {
					info = p
					break
				}
			}
			selected := pi == m.pipelineCursor

			// Build card content
			var card strings.Builder
			taglineStyle := MutedStyle
			phaseStyle := MutedStyle
			gateStyle := MutedStyle
			if selected {
				taglineStyle = lipgloss.NewStyle().Foreground(colorSubtext)
				phaseStyle = lipgloss.NewStyle()
				gateStyle = lipgloss.NewStyle().Foreground(colorSubtext)
			}

			card.WriteString(taglineStyle.Render(info.tagline) + "\n\n")

			// Phase flow
			phases := feature.PhasesForProfile(pipelineProfileFromKey(opt))
			var phaseNames []string
			for _, p := range phases {
				if !m.provisionalPublishable && p == feature.PhasePublish {
					continue
				}
				phaseNames = append(phaseNames, p.String())
			}
			card.WriteString(phaseStyle.Render(strings.Join(phaseNames, " ── ")) + "\n\n")

			// Gate options
			projection := m.projectedPipelineCheckpoints(opt)
			gates := availableGateLabels(projection)
			gatesStr := "None"
			if len(gates) > 0 {
				gatesStr = strings.Join(gates, ", ")
			}
			gatesLine := gateStyle.Render("Gate options: " + gatesStr)
			card.WriteString(gatesLine)

			// Render the box
			borderColor := colorOverlay
			if selected {
				borderColor = colorBrand
			}
			box := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(borderColor).
				Padding(0, 1).
				Width(cardWidth).
				Render(card.String())

			// Title: icon + name + recommended
			nameStyle := MutedStyle
			if selected {
				nameStyle = lipgloss.NewStyle().Foreground(colorBrand).Bold(true)
			}
			title := info.icon + " " + nameStyle.Render(info.name)
			titleStyle := lipgloss.NewStyle()
			if selected {
				titleStyle = titleStyle.Foreground(colorBrand)
			}
			box = renderBorderTitle(box, title, titleStyle)

			// Add cursor prefix
			cursor := "  "
			if selected {
				cursor = "▸ "
			}
			// Indent each line of the box with the cursor (first line) or spaces
			boxLines := strings.Split(box, "\n")
			for i, line := range boxLines {
				if i == 0 {
					c.WriteString(cursor + line + "\n")
				} else {
					c.WriteString("  " + line + "\n")
				}
			}
		}

	case wizardStepReview:
		var card strings.Builder

		// Helper: render a summary row with cursor, highlighting, and provenance.
		// When the row is selected the value is rendered in teal (SummarySelectedValueStyle).
		renderRow := func(field summaryField, label string, value string, provenance string) {
			selected := m.summaryCursor == field
			cursor := "  "
			if selected {
				cursor = "▸ "
			}
			valStr := value
			if selected && valStr != "" {
				valStr = SummarySelectedValueStyle.Render(valStr)
			}
			line := cursor + WizardLabelStyle.Render(label) + valStr
			if provenance != "" {
				line += " " + MutedStyle.Render(provenance)
			}
			card.WriteString(line + "\n")
		}

		// ── Name and Repos (above divider) ──
		renderRow(summaryFieldName, "Name", m.nameInput.Value(), "")
		descPreview := strings.ReplaceAll(m.descInput.Value(), "\n", " ")
		card.WriteString("  " + WizardLabelStyle.Render("Desc") + truncateString(descPreview, panelWidth-20) + "\n")
		if summary := m.attachmentSummary(); summary != "" {
			card.WriteString("  " + WizardLabelStyle.Render("Attach") + MutedStyle.Render(summary) + "\n")
		}
		var repos []string
		for _, r := range m.availRepos {
			if m.selectedRepos[r] {
				repos = append(repos, r)
			}
		}
		renderRow(summaryFieldRepos, "Repos", strings.Join(repos, ", "), "")
		if m.hasOffDefaultRepos() {
			countCurrent := 0
			countOffDefault := 0
			for _, bi := range m.branchInfos {
				if !bi.IsOffDefault {
					continue
				}
				countOffDefault++
				if m.branchChoices[bi.Name] {
					countCurrent++
				}
			}
			var baseVal string
			switch {
			case countCurrent == 0:
				baseVal = SuccessStyle.Render("default branch")
			case countCurrent == countOffDefault:
				baseVal = WarningStyle.Render("current branch")
			default:
				baseVal = WarningStyle.Render(fmt.Sprintf("mixed (%d of %d on current)", countCurrent, countOffDefault))
			}
			card.WriteString("  " + WizardLabelStyle.Render("Base") + baseVal + "\n")
		}

		// ── Divider ──
		card.WriteString("  ─────────────────────────────────────\n")

		// ── Pipeline (non-navigable display) ──
		card.WriteString("  " + WizardLabelStyle.Render("Pipeline") + m.pipelineOptions[m.pipelineCursor] + "\n")

		// ── Risk ──
		riskDescs := []string{"Low-impact, minor changes", "Standard feature work", "Critical systems, needs extra review"}
		{
			selected := m.summaryCursor == summaryFieldRisk
			provenance := ""
			if m.riskAutoDetected {
				provenance = "(auto-detected)"
			}
			renderRow(summaryFieldRisk, "Risk", m.riskOptions[m.riskCursor], provenance)
			if selected && m.summaryEditing {
				rendered := renderPillChoiceEditor("Risk", inputBoxWidth-4, m.riskOptions, m.riskCursor, riskDescs)
				card.WriteString("    " + strings.ReplaceAll(rendered, "\n", "\n    ") + "\n")
			}
		}

		// ── Models ──
		if m.summaryEditing && m.summaryCursor == summaryFieldModels {
			renderRow(summaryFieldModels, "Models", "", "")
			m.syncConfigEditorFromWizard()
			content := m.configEditor.renderModelsBox(reviewEditorContentWidth(inputBoxWidth - 4))
			rendered := renderReviewEditorBox("Model Selection", inputBoxWidth-4, content)
			card.WriteString("    " + strings.ReplaceAll(rendered, "\n", "\n    ") + "\n")
		} else {
			renderRow(summaryFieldModels, "Models",
				MutedStyle.Render(fmt.Sprintf("R:%s P:%s I:%s Rev:%s KB:%s",
					m.models.Research, m.models.Planning, m.models.Implementation, m.models.Review, m.models.KBBuild)),
				"")
		}

		// ── Inquiry ──
		{
			selected := m.summaryCursor == summaryFieldInquireness
			renderRow(summaryFieldInquireness, "Inquiry", m.inquirenessOptions[m.inquirenessCursor], "")
			if selected && m.summaryEditing {
				m.syncConfigEditorFromWizard()
				content := m.configEditor.renderInquirenessBox()
				rendered := renderReviewEditorBox("Inquiry", inputBoxWidth-4, content)
				card.WriteString("    " + strings.ReplaceAll(rendered, "\n", "\n    ") + "\n")
			}
		}

		// ── Checkpoints / Gates ──
		if m.summaryEditing && m.summaryCursor == summaryFieldCheckpoints {
			renderRow(summaryFieldCheckpoints, "Gates", "", "")
			m.syncConfigEditorFromWizard()
			content := m.configEditor.renderCheckpointsBox(reviewEditorContentWidth(inputBoxWidth - 4))
			rendered := renderReviewEditorBox("Gates", inputBoxWidth-4, content)
			card.WriteString("    " + strings.ReplaceAll(rendered, "\n", "\n    ") + "\n")
			card.WriteString(MutedStyle.Render("                space/tab: toggle  ↑/↓: navigate  enter/esc: done") + "\n")
		} else {
			projection := m.currentGateProjection()
			activeGates := gateLabels(projection)
			gatesVal := SuccessStyle.Render("Fully automatic")
			if len(activeGates) > 0 {
				gatesVal = strings.Join(activeGates, ", ")
			}
			renderRow(summaryFieldCheckpoints, "Gates", gatesVal, "")
		}

		// ── Exit Criteria ──
		if m.summaryEditing && m.summaryCursor == summaryFieldExitCriteria {
			renderRow(summaryFieldExitCriteria, "Exit Criteria", "", "")
			rendered := m.renderExitCriteriaEditor(inputBoxWidth - 4)
			card.WriteString("    " + strings.ReplaceAll(rendered, "\n", "\n    ") + "\n")
			card.WriteString(MutedStyle.Render("                ctrl+j: newline  enter: done  esc: cancel") + "\n")
		} else {
			exitPreview := strings.ReplaceAll(m.exitCriteria, "\n", " ")
			renderRow(summaryFieldExitCriteria, "Exit Criteria", MutedStyle.Render(truncateString(exitPreview, panelWidth-30)), "")
		}

		previewBox := panelStyle(true).Width(inputBoxWidth).Render(card.String())
		previewBox = renderBorderTitle(previewBox, "Feature Preview", lipgloss.NewStyle().Foreground(colorBrand))
		c.WriteString(previewBox + "\n\n")

		// Prominent G button with pipeline-aware label
		selectedPipeline := pipelineProfileFromKey(m.pipelineOptions[m.pipelineCursor])
		firstPhaseLabel := "knowledge base build"
		if selectedPipeline == feature.PipelineMedium {
			firstPhaseLabel = "planning"
		}
		gButton := lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSuccess).
			Render("  Shift + G  ")
		c.WriteString(gButton + " Create and start " + firstPhaseLabel)
	}

	// Wrap content in a main panel. The branch-selection screen lives inside
	// Step 2; surface that in the title bar so users know they're in a
	// sub-screen rather than a new step.
	stepTitle := titlePrefix + stepProgress
	if m.step == wizardStepWhere && m.showBranchWarning {
		stepTitle = titlePrefix + stepProgress + StepStyle.Render(" · Branch base")
	}
	contentBox = panelStyle(true).
		Width(panelWidth).
		Render(c.String())
	contentBox = renderBorderTitle(contentBox, stepTitle, TitleStyle)

	switch m.step {
	case wizardStepWhat:
		footer = KeyHelpStyle.Render(" [tab] Switch field   [enter] Next   [esc] Cancel")
	case wizardStepWhere:
		if m.showBranchWarning {
			footer = KeyHelpStyle.Render(" [↑↓/tab] Switch   [enter] Choose   [esc] Back")
		} else {
			footer = KeyHelpStyle.Render(" [enter] Add   [backspace] Remove last   [tab] Cycle focus   [ctrl+d] Continue   [esc] Back")
		}
	case wizardStepPipeline:
		footer = KeyHelpStyle.Render(" [↑↓] Select   [enter] Next   [esc] Back")
	case wizardStepReview:
		if m.summaryEditing {
			switch m.summaryCursor {
			case summaryFieldRisk, summaryFieldInquireness:
				footer = KeyHelpStyle.Render(" [←/→] Change   [enter/esc] Done")
			case summaryFieldModels:
				footer = KeyHelpStyle.Render(" [←/→/tab] Cycle model   [↑/↓] Navigate   [enter/esc] Done")
			case summaryFieldCheckpoints:
				footer = KeyHelpStyle.Render(" [space/tab] Toggle   [↑/↓] Navigate   [enter/esc] Done")
			case summaryFieldExitCriteria:
				footer = KeyHelpStyle.Render(" [ctrl+j] Newline   [enter] Done   [esc] Back")
			}
		} else {
			footer = KeyHelpStyle.Render(" [↑↓] Navigate   [enter] Edit   [Shift+G] Create   [esc] Back")
		}
	}
	return
}

// ViewModal returns the compact wizard panel for use as an overlay.
func (m WizardModel) ViewModal() string {
	contentBox, footer := m.wizardContent()
	return contentBox + "\n" + footer
}

func (m WizardModel) View() string {
	h := m.height
	if h < 10 {
		h = 24
	}

	contentBox, footer := m.wizardContent()

	// Center vertically
	boxH := lipgloss.Height(contentBox)
	footerH := lipgloss.Height(footer) + 2
	topPad := (h - boxH - footerH) / 2
	if topPad < 0 {
		topPad = 0
	}

	var out strings.Builder
	for i := 0; i < topPad; i++ {
		out.WriteString("\n")
	}
	out.WriteString(contentBox + "\n")

	// Fill remaining space to push footer to bottom
	remaining := h - topPad - boxH - footerH
	for i := 0; i < max(remaining, 0); i++ {
		out.WriteString("\n")
	}
	out.WriteString(footer + "\n")

	return out.String()
}

func (m WizardModel) renderStepProgress() string {
	current := m.currentStepNum()
	total := m.totalStepCount()
	return StepStyle.Render(fmt.Sprintf("Step %d of %d", current, total))
}

func (m WizardModel) totalStepCount() int {
	return 4
}

func (m WizardModel) currentStepNum() int {
	return int(m.step) + 1
}

func (m WizardModel) IsDone() bool {
	return m.result != nil
}

func (m WizardModel) IsCancelled() bool {
	return m.cancelled
}

func (m WizardModel) Result() *WizardResult {
	return m.result
}

// updateAtPrefix updates the text input to reflect a drilled directory path
// without completing the mention (picker stays active).
func (m *WizardModel) updateAtPrefix(dirPath string) {
	val := m.descInput.Value()
	idx := strings.LastIndex(val, "@")
	if idx >= 0 {
		newVal := val[:idx] + "@" + dirPath
		m.descInput.SetValue(newVal)
	}
}

// replaceAtMention replaces the trailing @... in the description input with the selected path.
func (m *WizardModel) replaceAtMention(path string) {
	val := m.descInput.Value()
	idx := strings.LastIndex(val, "@")
	if idx >= 0 {
		newVal := val[:idx] + "@" + path + " "
		m.descInput.SetValue(newVal)
	}
}

// trackSkillPrefix manages skill picker activation and prefix tracking.
func (m *WizardModel) trackSkillPrefix(prevVal, newVal string) {
	if !m.skillPicker.HasItems() {
		return
	}
	if m.skillPicker.IsActive() {
		idx := findSkillTriggerSlash(newVal)
		if idx < 0 {
			m.skillPicker.Deactivate()
		} else {
			m.skillPicker.SetPrefix(newVal[idx+1:])
		}
	} else if len(newVal) > len(prevVal) && strings.HasSuffix(newVal, "/") {
		slashIdx := len(newVal) - 1
		if slashIdx == 0 || newVal[slashIdx-1] == ' ' {
			m.skillPicker.Activate("")
		}
	}
}

// replaceSlashMention replaces the trailing /... in the active text input
// with the selected skill name.
// The current 3-step flow does not expose a slash-mention text field, but the
// hook remains so the picker integration has a single re-entry point.
func (m *WizardModel) replaceSlashMention(skillName string) {
	return
}

// SetWidth updates all text input widths based on terminal size.
func (m *WizardModel) SetWidth(w int) {
	m.width = w
	// Guard against zero-value WizardModel (textarea not yet initialized).
	if m.descInput.Width() == 0 {
		return
	}
	panelWidth := m.wizardPanelWidth()
	inputWidth := panelWidth - 6
	if inputWidth < 20 {
		inputWidth = 20
	}
	// In v2, inputWidth is the total rendered width of the inner box (border
	// included). The content area = inputWidth - 4 (border 2 + padding 2).
	tiWidth := inputWidth - 4
	if tiWidth < 20 {
		tiWidth = 20
	}
	m.nameInput.SetWidth(tiWidth)
	m.repoInput.SetWidth(tiWidth)
	// SimpleTextarea gets content-area width (inputWidth minus full frame:
	// border 2 + padding 2 = 4).
	taWidth := inputWidth - 4
	if taWidth < 20 {
		taWidth = 20
	}
	m.descInput.SetWidth(taWidth)
	m.exitInput.SetWidth(taWidth)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
