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

package feature

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrSetupAlreadyRunning = errors.New("setup already running")

type SetupEventKind string

const (
	SetupEventStarted       SetupEventKind = "started"
	SetupEventTaskStarted   SetupEventKind = "task_started"
	SetupEventTaskCompleted SetupEventKind = "task_completed"
	SetupEventCompleted     SetupEventKind = "completed"
	SetupEventFailed        SetupEventKind = "failed"
)

type SetupEvent struct {
	Kind      SetupEventKind
	FeatureID string
	RunNumber int
	Attempt   int
	LogPath   string

	TaskKey    string
	TaskKind   SetupTaskKind
	TaskStatus SetupStatus
	Repo       string
	Path       string
	Branch     string
	Error      string
}

type SetupRunnerOptions struct {
	OnEvent func(SetupEvent)
}

func (m *Manager) setupLock(featureID string) (func(), error) {
	m.setupMu.Lock()
	defer m.setupMu.Unlock()
	if m.setupLocks == nil {
		m.setupLocks = make(map[string]struct{})
	}
	if _, ok := m.setupLocks[featureID]; ok {
		return nil, ErrSetupAlreadyRunning
	}
	m.setupLocks[featureID] = struct{}{}
	return func() {
		m.setupMu.Lock()
		delete(m.setupLocks, featureID)
		m.setupMu.Unlock()
	}, nil
}

func (m *Manager) RunSetup(featureID string, opts ...SetupRunnerOptions) error {
	return m.runSetup(featureID, false, opts...)
}

func (m *Manager) RetrySetup(featureID string, opts ...SetupRunnerOptions) error {
	return m.runSetup(featureID, true, opts...)
}

func (m *Manager) runSetup(featureID string, retry bool, opts ...SetupRunnerOptions) error {
	unlock, err := m.setupLock(featureID)
	if err != nil {
		return err
	}
	defer unlock()

	var opt SetupRunnerOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	emit := func(ev SetupEvent) {
		if opt.OnEvent != nil {
			opt.OnEvent(ev)
		}
	}

	f, err := m.Store.Load(featureID)
	if err != nil {
		return fmt.Errorf("loading feature: %w", err)
	}
	setup := f.Run().Setup
	if setup == nil {
		return fmt.Errorf("feature %s has no setup state", featureID)
	}
	if setup.Status == SetupStatusDone {
		return nil
	}
	if retry {
		if f.Status != StatusFailed || f.FailureType != FailureWorktreeSetup || setup.Status != SetupStatusFailed {
			return fmt.Errorf("setup retry requires Failed (%s) feature with failed setup state", FailureWorktreeSetup)
		}
	} else if f.Status != StatusSettingUpWorktrees || setup.Status != SetupStatusRunning {
		return fmt.Errorf("setup can only run from active setup state")
	}

	attempt := setup.Attempt
	if retry {
		attempt++
		if err := m.prepareSetupRetry(featureID, attempt); err != nil {
			return err
		}
	} else if attempt <= 0 {
		attempt = 1
	}

	f, err = m.Store.Load(featureID)
	if err != nil {
		return fmt.Errorf("reloading feature: %w", err)
	}
	setup = f.Run().Setup
	logPath := setupAttemptLogPath(m.Store, f, attempt)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		_ = m.failSetupTask(featureID, "", err.Error())
		return fmt.Errorf("creating setup log directory: %w", err)
	}
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		_ = m.failSetupTask(featureID, "", err.Error())
		return fmt.Errorf("creating setup log: %w", err)
	}
	if err := m.Store.Modify(featureID, func(ff *Feature) error {
		s := ff.Run().Setup
		if s == nil {
			return fmt.Errorf("feature %s has no setup state", featureID)
		}
		now := time.Now()
		s.Status = SetupStatusRunning
		s.Attempt = attempt
		s.StartedAt = &now
		s.CompletedAt = nil
		s.LatestLogPath = logPath
		s.LastError = ""
		ff.Status = StatusSettingUpWorktrees
		ff.FailureType = ""
		ff.LastError = ""
		return nil
	}); err != nil {
		return err
	}
	appendSetupLog(logPath, "setup attempt %d started for feature %s", attempt, featureID)
	emit(SetupEvent{Kind: SetupEventStarted, FeatureID: featureID, RunNumber: f.ActiveRun, Attempt: attempt, LogPath: logPath})

	for _, key := range setupTaskOrder(setup) {
		current, err := m.Store.Load(featureID)
		if err != nil {
			return fmt.Errorf("loading feature before setup task: %w", err)
		}
		task, ok := current.Run().Setup.Tasks[key]
		if !ok {
			continue
		}
		if task.Status == SetupStatusDone {
			appendSetupLog(logPath, "skip %s: already done", task.Key)
			continue
		}
		task.Attempt = attempt
		if err := m.markSetupTaskRunning(featureID, task, logPath); err != nil {
			return err
		}
		emit(setupTaskEvent(SetupEventTaskStarted, featureID, current.ActiveRun, attempt, logPath, task, ""))

		completed, err := m.executeSetupTask(current, task, logPath)
		if err != nil {
			msg := err.Error()
			if markErr := m.failSetupTask(featureID, task.Key, msg); markErr != nil {
				return errors.Join(err, markErr)
			}
			completed.Status = SetupStatusFailed
			completed.LastError = msg
			appendSetupLog(logPath, "setup failed on %s: %s", task.Key, msg)
			emit(setupTaskEvent(SetupEventFailed, featureID, current.ActiveRun, attempt, logPath, completed, msg))
			return err
		}
		if err := m.completeSetupTask(featureID, completed); err != nil {
			return err
		}
		appendSetupLog(logPath, "task %s completed", completed.Key)
		emit(setupTaskEvent(SetupEventTaskCompleted, featureID, current.ActiveRun, attempt, logPath, completed, ""))
	}

	if err := m.completeSetup(featureID); err != nil {
		return err
	}
	appendSetupLog(logPath, "setup attempt %d completed", attempt)
	emit(SetupEvent{Kind: SetupEventCompleted, FeatureID: featureID, RunNumber: f.ActiveRun, Attempt: attempt, LogPath: logPath})
	return nil
}

func (m *Manager) prepareSetupRetry(featureID string, attempt int) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		setup := f.Run().Setup
		if setup == nil {
			return fmt.Errorf("feature %s has no setup state", featureID)
		}
		now := time.Now()
		setup.Status = SetupStatusRunning
		setup.Attempt = attempt
		setup.StartedAt = &now
		setup.CompletedAt = nil
		setup.LastError = ""
		for _, key := range setupTaskOrder(setup) {
			task := setup.Tasks[key]
			if task.Status == SetupStatusDone {
				continue
			}
			task.Status = SetupStatusQueued
			task.Attempt = attempt
			task.StartedAt = nil
			task.EndedAt = nil
			task.LastError = ""
			setup.Tasks[key] = task
		}
		f.Status = StatusSettingUpWorktrees
		f.FailureType = ""
		f.LastError = ""
		return nil
	})
}

func (m *Manager) markSetupTaskRunning(featureID string, task SetupTask, logPath string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		setup := f.Run().Setup
		if setup == nil {
			return fmt.Errorf("feature %s has no setup state", featureID)
		}
		now := time.Now()
		task.Status = SetupStatusRunning
		task.StartedAt = &now
		task.EndedAt = nil
		task.LastError = ""
		setup.LatestLogPath = logPath
		setup.Tasks[task.Key] = task
		return nil
	})
}

func (m *Manager) executeSetupTask(f *Feature, task SetupTask, logPath string) (SetupTask, error) {
	appendSetupLog(logPath, "task %s started: kind=%s repo=%s branch=%s path=%s", task.Key, task.Kind, task.Repo, task.Branch, task.Path)
	switch task.Kind {
	case SetupTaskWorktree:
		return m.executeWorktreeSetupTask(f, task, logPath)
	case SetupTaskImage:
		return m.executeImageSetupTask(f, task, logPath)
	case SetupTaskAttachment:
		return m.executeAttachmentSetupTask(f, task, logPath)
	default:
		return task, fmt.Errorf("unknown setup task kind %q", task.Kind)
	}
}

func (m *Manager) executeWorktreeSetupTask(f *Feature, task SetupTask, logPath string) (SetupTask, error) {
	if m.Worktrees == nil {
		return task, fmt.Errorf("worktree manager not configured")
	}
	idx := repoIndexByName(f, task.Repo)
	if idx < 0 {
		return task, fmt.Errorf("repo %q no longer exists on feature", task.Repo)
	}
	repo := f.Repos[idx]
	workspaceSlug, branch := setupWorkspaceSlug(f, repo, task)
	if workspaceSlug == "" {
		workspaceSlug = f.WorkspaceSlug()
	}
	if branch == "" || branch == defaultBranchName(workspaceSlug) {
		branch = m.branchName(workspaceSlug)
	}
	task.Branch = branch
	if task.StartPoint == "" && !task.UseCurrentBranch {
		task.StartPoint = repo.BaseBranch
	}
	if task.Path == "" {
		if pather, ok := m.Worktrees.(interface {
			ExpectedPath(featureSlug, repoName string) string
		}); ok {
			task.Path = pather.ExpectedPath(workspaceSlug, repo.Name)
		}
	}
	if task.Path != "" {
		if err := m.reuseExpectedWorktree(task); err == nil {
			appendSetupLog(logPath, "reused expected worktree path %s for branch %s", task.Path, task.Branch)
			return task, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return task, err
		}
	}
	startPoint := task.StartPoint
	if task.UseCurrentBranch {
		startPoint = ""
	}
	appendSetupLog(logPath, "creating worktree repo=%s branch=%s start_point=%s", repo.Name, task.Branch, startPoint)
	path, err := m.Worktrees.Create(repo.Path, workspaceSlug, repo.Name, startPoint)
	if err != nil {
		return task, fmt.Errorf("creating worktree for %s: %w", repo.Name, err)
	}
	task.Path = path
	appendSetupLog(logPath, "created worktree repo=%s path=%s branch=%s", repo.Name, task.Path, task.Branch)
	return task, nil
}

func (m *Manager) reuseExpectedWorktree(task SetupTask) error {
	info, err := os.Stat(task.Path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("expected worktree path %s exists but is not a directory", task.Path)
	}
	if m.Branches != nil {
		branch, err := m.Branches.CurrentBranch(task.Path)
		if err != nil {
			return fmt.Errorf("checking branch for expected worktree path %s: %w", task.Path, err)
		}
		if task.Branch != "" && branch != task.Branch {
			return fmt.Errorf("expected worktree path %s is on branch %q, want %q", task.Path, branch, task.Branch)
		}
	}
	return nil
}

func (m *Manager) executeImageSetupTask(f *Feature, task SetupTask, logPath string) (SetupTask, error) {
	if task.SourcePath == "" {
		return task, fmt.Errorf("image setup task %s is missing source path", task.Key)
	}
	n, err := setupTaskOrdinal(task.Key, "image:")
	if err != nil {
		return task, err
	}
	if task.Path == "" {
		task.Path = filepath.Join(m.Store.BaseDir, f.ID, "images", fmt.Sprintf("image-%d.png", n))
	}
	if err := os.MkdirAll(filepath.Dir(task.Path), 0o755); err != nil {
		return task, fmt.Errorf("creating images directory: %w", err)
	}
	appendSetupLog(logPath, "copying image source=%s destination=%s", task.SourcePath, task.Path)
	if err := copyFile(task.SourcePath, task.Path); err != nil {
		return task, fmt.Errorf("copying image %d: %w", n, err)
	}
	return task, nil
}

func (m *Manager) executeAttachmentSetupTask(f *Feature, task SetupTask, logPath string) (SetupTask, error) {
	if task.SourcePath == "" {
		return task, fmt.Errorf("attachment setup task %s is missing source path", task.Key)
	}
	if _, err := setupTaskOrdinal(task.Key, "attachment:"); err != nil {
		return task, err
	}
	if task.Path == "" {
		task.Path = filepath.Join(m.Store.BaseDir, f.ID, "attachments", filepath.Base(task.SourcePath))
	}
	if err := os.MkdirAll(filepath.Dir(task.Path), 0o755); err != nil {
		return task, fmt.Errorf("creating attachments directory: %w", err)
	}
	appendSetupLog(logPath, "copying attachment source=%s destination=%s", task.SourcePath, task.Path)
	if err := copyFile(task.SourcePath, task.Path); err != nil {
		return task, fmt.Errorf("copying attachment %s: %w", filepath.Base(task.SourcePath), err)
	}
	return task, nil
}

func (m *Manager) completeSetupTask(featureID string, task SetupTask) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		setup := f.Run().Setup
		if setup == nil {
			return fmt.Errorf("feature %s has no setup state", featureID)
		}
		now := time.Now()
		task.Status = SetupStatusDone
		task.EndedAt = &now
		task.LastError = ""
		setup.Tasks[task.Key] = task
		switch task.Kind {
		case SetupTaskWorktree:
			idx := repoIndexByName(f, task.Repo)
			if idx >= 0 {
				f.Repos[idx].WorktreePath = task.Path
				if task.Branch != "" {
					f.Repos[idx].Branch = task.Branch
				}
			}
		case SetupTaskImage:
			f.Images = appendUniqueString(f.Images, task.Path)
		case SetupTaskAttachment:
			f.Attachments = appendUniqueString(f.Attachments, task.Path)
		}
		return nil
	})
}

func (m *Manager) failSetupTask(featureID, taskKey, errMsg string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		setup := f.Run().Setup
		if setup == nil {
			return fmt.Errorf("feature %s has no setup state", featureID)
		}
		now := time.Now()
		setup.Status = SetupStatusFailed
		setup.CompletedAt = &now
		setup.LastError = errMsg
		if task, ok := setup.Tasks[taskKey]; ok {
			task.Status = SetupStatusFailed
			task.EndedAt = &now
			task.LastError = errMsg
			setup.Tasks[taskKey] = task
		}
		f.Status = StatusFailed
		f.FailureType = FailureWorktreeSetup
		f.LastError = errMsg
		return nil
	})
}

func (m *Manager) completeSetup(featureID string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		setup := f.Run().Setup
		if setup == nil {
			return fmt.Errorf("feature %s has no setup state", featureID)
		}
		now := time.Now()
		setup.Status = SetupStatusDone
		setup.CompletedAt = &now
		setup.LastError = ""
		for _, key := range setupTaskOrder(setup) {
			task := setup.Tasks[key]
			if task.Status != SetupStatusDone {
				return fmt.Errorf("setup task %s is %s, want done", key, task.Status)
			}
		}
		if err := f.Transition(StatusCreated); err != nil {
			return err
		}
		f.FailureType = ""
		f.LastError = ""
		return nil
	})
}

func (m *Manager) ReconcileAbandonedSetups() ([]string, error) {
	features, err := m.List()
	if err != nil && !IsPartialLoadError(err) {
		return nil, err
	}
	var reconciled []string
	for _, f := range features {
		setup := f.Run().Setup
		if setup == nil || setup.Status != SetupStatusRunning || f.Status != StatusSettingUpWorktrees {
			continue
		}
		msg := "setup was interrupted by shutdown or crash; retry setup to continue"
		if err := m.failAbandonedSetup(f.ID, msg); err != nil {
			return reconciled, err
		}
		reconciled = append(reconciled, f.ID)
	}
	return reconciled, nil
}

func (m *Manager) failAbandonedSetup(featureID, msg string) error {
	return m.Store.Modify(featureID, func(f *Feature) error {
		setup := f.Run().Setup
		if setup == nil || setup.Status != SetupStatusRunning {
			return nil
		}
		now := time.Now()
		setup.Status = SetupStatusFailed
		setup.CompletedAt = &now
		setup.LastError = msg
		for _, key := range setupTaskOrder(setup) {
			task := setup.Tasks[key]
			if task.Status == SetupStatusRunning {
				task.Status = SetupStatusFailed
				task.EndedAt = &now
				task.LastError = msg
				setup.Tasks[key] = task
				break
			}
		}
		f.Status = StatusFailed
		f.FailureType = FailureWorktreeSetup
		f.LastError = msg
		return nil
	})
}

func setupAttemptLogPath(store *Store, f *Feature, attempt int) string {
	return filepath.Join(store.RunDir(f.ID, f.ActiveRun), "setup", fmt.Sprintf("attempt-%02d-output.txt", attempt))
}

func appendSetupLog(path, format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	line = fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), line)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line)
}

func setupTaskOrder(setup *SetupState) []string {
	if setup == nil {
		return nil
	}
	seen := make(map[string]bool, len(setup.Tasks))
	var order []string
	for _, key := range setup.TaskOrder {
		if _, ok := setup.Tasks[key]; ok && !seen[key] {
			order = append(order, key)
			seen[key] = true
		}
	}
	var rest []string
	for key := range setup.Tasks {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(order, rest...)
}

func setupTaskEvent(kind SetupEventKind, featureID string, runNumber, attempt int, logPath string, task SetupTask, errMsg string) SetupEvent {
	return SetupEvent{
		Kind:       kind,
		FeatureID:  featureID,
		RunNumber:  runNumber,
		Attempt:    attempt,
		LogPath:    logPath,
		TaskKey:    task.Key,
		TaskKind:   task.Kind,
		TaskStatus: task.Status,
		Repo:       task.Repo,
		Path:       task.Path,
		Branch:     task.Branch,
		Error:      errMsg,
	}
}

func setupTaskOrdinal(key, prefix string) (int, error) {
	raw, ok := strings.CutPrefix(key, prefix)
	if !ok || raw == "" {
		return 0, fmt.Errorf("setup task %q has invalid key for prefix %q", key, prefix)
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("setup task %q has invalid ordinal", key)
	}
	return n, nil
}

func repoIndexByName(f *Feature, name string) int {
	if f == nil {
		return -1
	}
	for i, repo := range f.Repos {
		if repo.Name == name {
			return i
		}
	}
	return -1
}

func appendUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
