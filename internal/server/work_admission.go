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

package server

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/git"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// admissionRetryAfterSeconds is the Retry-After hint carried by 503
// update_in_progress work-start refusals.
const admissionRetryAfterSeconds = 5

// ProbeActivity counts read-launched background work for the repository-work
// detector. It is shared with the CLI wiring so Git freshness refreshes
// launched by feature projections participate in the count.
type ProbeActivity struct {
	count atomic.Int64
}

// NewProbeActivity creates an empty probe counter.
func NewProbeActivity() *ProbeActivity { return &ProbeActivity{} }

// enter registers one in-flight probe and returns its release.
func (p *ProbeActivity) enter() func() {
	if p == nil {
		return func() {}
	}
	p.count.Add(1)
	return func() { p.count.Add(-1) }
}

// probeAdmissionBegin reserves one unit of read-launched background probe
// work (provider CLI probes, workspace Git inspection, catalog discovery).
// A closed boundary refuses the launch.
func (h *apiHandler) probeAdmissionBegin() (func(), error) {
	return newAdmittedProbe(h.admission, h.probeActivity).begin()
}

// refuseAdmissionClosed writes the canonical 503 update_in_progress refusal
// when the admission boundary is closed. It returns whether the response
// was written; callers use it for work-shaping requests whose launch point
// lives outside this handler (durable queueing, session state changes that
// immediately count as activity).
func (h *apiHandler) refuseAdmissionClosed(w http.ResponseWriter) bool {
	if h.admission == nil || !h.admission.Closed() {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(admissionRetryAfterSeconds))
	writeAPIError(w, http.StatusServiceUnavailable, errcat.UpdateInProgress,
		errcat.WithParams(errcat.UpdateInProgressParams{
			RetryAfterSeconds: admissionRetryAfterSeconds,
		}))
	return true
}

// acquireAdmission reserves one unit of admitted work. A nil boundary (no
// install support configured) always succeeds with a nil reservation that
// releases safely.
func (h *apiHandler) acquireAdmission(cat workadmission.Category) (*workadmission.Reservation, error) {
	if h.admission == nil {
		return nil, nil
	}
	return h.admission.Acquire(cat)
}

// writeAdmissionRefusal renders the canonical 503 update_in_progress refusal
// with a Retry-After hint when err reports a closed admission boundary. It
// returns whether the response was written.
func (h *apiHandler) writeAdmissionRefusal(w http.ResponseWriter, err error) bool {
	closed, ok := workadmission.AsClosed(err)
	if !ok {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(admissionRetryAfterSeconds))
	writeAPIError(w, http.StatusServiceUnavailable, errcat.UpdateInProgress,
		errcat.WithParams(errcat.UpdateInProgressParams{
			RetryAfterSeconds: admissionRetryAfterSeconds,
		}),
		errcat.WithDiagnostics(closed.Error()))
	return true
}

// admissionActivitySnapshot discovers the observed work state for the update
// snapshot's active-work summary. Any detection failure reports
// detection_failed: uncertainty never reads as zero activity.
func (h *apiHandler) admissionActivitySnapshot(ctx context.Context) (workadmission.Activity, bool, error) {
	if h.admission == nil {
		return workadmission.Activity{}, false, nil
	}
	activity, err := h.admission.Detect(ctx)
	if err != nil {
		return workadmission.Activity{}, true, nil
	}
	return activity, false, nil
}

// admissionPendingCount returns the number of held admission reservations.
func (h *apiHandler) admissionPendingCount() int {
	total, _ := h.admissionPendingCounts()
	return total
}

// admissionPendingCounts returns the held admission reservations with their
// per-category breakdown. Categories outside feature and chat are protected
// or unknown work an explicit-stop install must refuse on.
func (h *apiHandler) admissionPendingCounts() (int, map[workadmission.Category]int) {
	if h.admission == nil {
		return 0, nil
	}
	return h.admission.Held()
}

// registerAdmissionDetectors installs the handler-owned activity detectors
// on the shared runtime boundary. The same coordinator instance is shared
// with orchestration, sessions, and repository work, so detector ownership
// staying here keeps discovery outside the boundary's state mutex.
func (h *apiHandler) registerAdmissionDetectors(coordinator *workadmission.Coordinator) {
	if coordinator == nil {
		return
	}
	coordinator.SetDetectors([]workadmission.Detector{
		h.detectFeatureActivity,
		h.detectChatActivity,
		h.detectCloneActivity,
		h.detectUploadActivity,
		h.detectOriginActivity,
		h.detectRepositoryWork,
	})
}

// detectFeatureActivity counts features through the same projection that
// enables the pause-stop action: a running or need-user-input status for
// ordinary features, and a running status for children whose relationship
// is still open. The count intentionally fails toward blocking.
func (h *apiHandler) detectFeatureActivity(context.Context) (workadmission.Activity, error) {
	if h.featureDetectFail != nil {
		// Test-only seam armed by the selfupdate driver: a deterministic
		// detection failure for detection-failure journeys. Production
		// never sets it.
		if err := h.featureDetectFail(); err != nil {
			return workadmission.Activity{}, err
		}
	}
	if h.features == nil {
		return workadmission.Activity{}, nil
	}
	features, err := h.features.List()
	if err != nil {
		return workadmission.Activity{}, err
	}
	activity := workadmission.Activity{}
	for _, f := range features {
		if f == nil {
			continue
		}
		if f.IsChild() && !f.IsActiveChild() {
			// A closed relationship hands the child to automatic
			// reconciliation; that work is covered by its reservations.
			continue
		}
		if f.Status.IsRunning() || f.Status == feature.StatusNeedUserInput {
			activity.Features++
		}
	}
	return activity, nil
}

// detectChatActivity reports whether the singleton chat session is active,
// using the existing active-chat semantics.
func (h *apiHandler) detectChatActivity(context.Context) (workadmission.Activity, error) {
	activity := workadmission.Activity{}
	if h.sessions == nil {
		return activity, nil
	}
	if sess := h.sessions.GetSession(ChatSessionID); sess != nil && sess.IsActive() {
		activity.ChatActive = true
	}
	return activity, nil
}

// detectCloneActivity counts queued/running clone work plus operations whose
// cleanup has not settled, through the clone service's own projection. Test
// fakes without the projection count zero.
func (h *apiHandler) detectCloneActivity(context.Context) (workadmission.Activity, error) {
	activity := workadmission.Activity{}
	if h.clones == nil {
		return activity, nil
	}
	if counter, ok := h.clones.(interface{ ActiveWork() int }); ok {
		activity.Clones = counter.ActiveWork()
	}
	return activity, nil
}

// detectUploadActivity counts actual in-flight uploads: staging requests in
// progress plus consumption claims. Completed upload references and their
// tombstones never count.
func (h *apiHandler) detectUploadActivity(context.Context) (workadmission.Activity, error) {
	activity := workadmission.Activity{}
	if h.uploads != nil {
		activity.Uploads = h.uploads.inflightWork()
	}
	return activity, nil
}

// detectOriginActivity counts active origin-comparison attempts.
func (h *apiHandler) detectOriginActivity(context.Context) (workadmission.Activity, error) {
	activity := workadmission.Activity{}
	if h.originChecks != nil {
		activity.OriginChecks = h.originChecks.inflightAttempts()
	}
	return activity, nil
}

// detectRepositoryWork counts admitted Update-from-origin attempts and
// read-launched background probes (readiness, catalog discovery, Git
// freshness/dirtiness refreshes, reconciliation).
func (h *apiHandler) detectRepositoryWork(context.Context) (workadmission.Activity, error) {
	activity := workadmission.Activity{}
	if h.sourceUpdates != nil {
		activity.RepositoryWork += h.sourceUpdates.total()
	}
	if h.probeActivity != nil {
		activity.RepositoryWork += int(h.probeActivity.count.Load())
	}
	return activity, nil
}

// admittedProbe ties one unit of read-launched background work to the
// admission boundary: a reservation held for the work's full lifetime plus
// detector visibility through the probe counter.
type admittedProbe struct {
	admission *workadmission.Coordinator
	probes    *ProbeActivity
}

func newAdmittedProbe(admission *workadmission.Coordinator, probes *ProbeActivity) *admittedProbe {
	return &admittedProbe{admission: admission, probes: probes}
}

// begin reserves one probe. A closed boundary refuses the launch; a nil
// boundary admits trivially.
func (a *admittedProbe) begin() (func(), error) {
	if a == nil {
		return func() {}, nil
	}
	releaseProbe := a.probes.enter()
	if a.admission == nil {
		return releaseProbe, nil
	}
	res, err := a.admission.Acquire(workadmission.CategoryRepository)
	if err != nil {
		releaseProbe()
		return nil, err
	}
	return func() {
		releaseProbe()
		res.Release()
	}, nil
}

// admittedCleanliness wraps the cached worktree-dirtiness inspector so every
// probe it launches owns an admission reservation for its full lifetime; a
// stale-cache response therefore never releases protection while a detached
// cache refresh continues.
type admittedCleanliness struct {
	base     git.CleanlinessInspector
	admitted *admittedProbe
}

// InspectCleanliness reserves admission around one dirtiness probe.
func (c admittedCleanliness) InspectCleanliness(worktreePath string, maxPerCategory int) (*git.CleanlinessReport, error) {
	if c.admitted != nil {
		release, err := c.admitted.begin()
		if err != nil {
			return nil, err
		}
		defer release()
	}
	return c.base.InspectCleanliness(worktreePath, maxPerCategory)
}
