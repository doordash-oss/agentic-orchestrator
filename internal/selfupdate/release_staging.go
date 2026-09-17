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

package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Release-staging object names inside a tx dir. The downloaded archive and
// the extracted release executable are recognized transaction objects so
// validated cleanup and abandoned-staging reconciliation cover them.
const (
	releaseArchiveName   = "archive"
	releaseCandidateName = "candidate"
)

// Hard bounds for one release package download, enforced across redirects
// and streaming. The exported constants stay fixed documentation; the vars
// are the working copies tests may shrink to exercise the boundary checks
// cheaply.
const (
	// ArchiveMaxCompressedBytes caps the compressed tarball download.
	ArchiveMaxCompressedBytes = 64 << 20
	// DownloadInactivityTimeout bounds progress-free time while streaming.
	DownloadInactivityTimeout = 60 * time.Second
	// DownloadTotalTimeout bounds the whole package operation.
	DownloadTotalTimeout = 10 * time.Minute
)

var (
	archiveMaxCompressedBytes = int64(ArchiveMaxCompressedBytes)
	downloadInactivityTimeout = DownloadInactivityTimeout
	downloadTotalTimeout      = DownloadTotalTimeout
)

// StageReleaseOptions carries the installed-build context the staging gate
// requires: only an eligible, unsuppressed, strictly newer release may stage.
type StageReleaseOptions struct {
	// CurrentVersion is the installed version; the target must be strictly
	// greater (a non-increasing target never stages).
	CurrentVersion string
	// Eligibility is the classified install eligibility of the running
	// build; unsupported installs never stage.
	Eligibility Eligibility
}

// StagedRelease is one authenticated, safely extracted release executable in
// updater-owned staging. Its provenance is immutable and is carried into the
// verified candidate.
type StagedRelease struct {
	txID            string
	txDir           string
	candidatePath   string
	candidateDigest string
	release         VerifiedRelease
}

// TxID returns the staging transaction's id.
func (s *StagedRelease) TxID() string { return s.txID }

// TxDir returns the private staging directory.
func (s *StagedRelease) TxDir() string { return s.txDir }

// CandidatePath returns the extracted executable's path inside staging.
func (s *StagedRelease) CandidatePath() string { return s.candidatePath }

// CandidateDigest returns the sha256 hex digest of the extracted executable.
func (s *StagedRelease) CandidateDigest() string { return s.candidateDigest }

// Release returns the authenticated release provenance.
func (s *StagedRelease) Release() VerifiedRelease { return s.release }

// ValidateReleaseTarget enforces the staging admission rules for one
// verified release against the installed build: a clean parseable target,
// an eligible install, a strictly increasing version, and no exact-version
// suppression.
func ValidateReleaseTarget(exec Executable, release VerifiedRelease, opts StageReleaseOptions) error {
	target := release.Resolved().Version
	if target == "" {
		return errors.New("release target version is empty")
	}
	if _, ok := ParseReleaseVersion(target); !ok {
		return fmt.Errorf("release target version %q is not a clean release version", target)
	}
	if !opts.Eligibility.Supported {
		return fmt.Errorf("installation is unsupported (%s); release staging refused", opts.Eligibility.Reason)
	}
	cmp, ordered := CompareReleaseVersions(target, opts.CurrentVersion)
	if !ordered {
		return fmt.Errorf("release target %q is not orderable against the current version %q", target, opts.CurrentVersion)
	}
	if cmp <= 0 {
		return fmt.Errorf("release target %q is not newer than the current version %q", target, opts.CurrentVersion)
	}
	lookup, err := LookupSuppression(exec.Path, target)
	if err != nil {
		return fmt.Errorf("reading suppression state: %w", err)
	}
	if lookup.Suppressed {
		return fmt.Errorf("release target %q is suppressed after a previous failed installation", target)
	}
	return nil
}

// StageVerifiedRelease downloads the verified release's platform tarball
// into updater-owned staging on the executable's filesystem, authenticates
// its complete bytes against the signed manifest digest, and extracts only
// the intended executable. Durable staging ownership is established before
// any download byte is written, so a failure, cancellation, or ordinary
// restart can clean the recognized abandoned files under the existing
// recovery rules without an install receipt. Staging never writes into the
// install root and never changes installed bytes, permissions, or receipts.
func (c *FeedClient) StageVerifiedRelease(ctx context.Context, exec Executable, release VerifiedRelease, opts StageReleaseOptions) (*StagedRelease, error) {
	if err := ValidateReleaseTarget(exec, release, opts); err != nil {
		return nil, err
	}
	matches, err := exec.PathStillMatches()
	if err != nil {
		return nil, err
	}
	if !matches {
		return nil, fmt.Errorf("installed executable %s no longer matches captured identity", exec.Path)
	}
	if err := ensureLeaseDir(exec.Path); err != nil {
		return nil, err
	}
	txID, err := newTransactionID()
	if err != nil {
		return nil, err
	}
	txDir := filepath.Join(LeaseDir(exec.Path), txDirPrefix+txID)
	if err := os.Mkdir(txDir, 0o700); err != nil {
		return nil, fmt.Errorf("create release staging dir: %w", err)
	}
	if err := os.Chmod(txDir, 0o700); err != nil {
		return nil, fmt.Errorf("repair release staging dir permissions: %w", err)
	}
	if err := SyncDir(LeaseDir(exec.Path)); err != nil {
		return nil, err
	}
	// Durable staging ownership precedes every downloaded byte: a later
	// launch recognizes this dir as owned abandoned staging even though no
	// install receipt exists.
	if err := writeStagingRecord(txDir, StagingRecord{
		TransactionID:    txID,
		ExecutablePath:   exec.Path,
		ExecutableDigest: exec.Digest,
		CreatedAt:        time.Now(),
	}); err != nil {
		return nil, err
	}
	staged := &StagedRelease{
		txID:    txID,
		txDir:   txDir,
		release: release,
	}
	// Any failure leaves only recognized owned staging objects behind; the
	// validated cleanup path can retry later, and an ordinary restart
	// reconciles the dir under the existing recovery rules.
	if err := c.downloadAndExtract(ctx, exec, release, staged); err != nil {
		_ = CleanupSettledTransaction(exec.Path, txID, CleanupSeams{})
		return nil, err
	}
	return staged, nil
}

// downloadAndExtract streams the release archive into staging, verifies its
// complete digest against the signed manifest entry, and extracts the single
// intended executable.
func (c *FeedClient) downloadAndExtract(ctx context.Context, exec Executable, release VerifiedRelease, staged *StagedRelease) error {
	archivePath := filepath.Join(staged.txDir, releaseArchiveName)
	digest, err := c.downloadArchive(ctx, release, archivePath)
	if err != nil {
		return err
	}
	if digest != release.TarballDigest() {
		return fmt.Errorf("downloaded archive digest %s does not match the signed manifest entry %s", digest, release.TarballDigest())
	}
	candidatePath := filepath.Join(staged.txDir, releaseCandidateName)
	candidateDigest, err := ExtractExecutable(archivePath, candidatePath)
	if err != nil {
		return err
	}
	staged.candidatePath = candidatePath
	staged.candidateDigest = candidateDigest
	return nil
}

// downloadArchive streams the release tarball into dst (0600, O_EXCL) under
// the package bounds: a 64-MiB compressed cap enforced while streaming
// regardless of Content-Length, a sixty-second inactivity bound, a
// ten-minute total deadline across redirects and streaming, and the same
// destination policy as every other release request. Cancellation and
// failures close the response and the file and never leave a candidate.
func (c *FeedClient) downloadArchive(ctx context.Context, release VerifiedRelease, dst string) (string, error) {
	totalCtx, cancel := context.WithTimeout(ctx, downloadTotalTimeout)
	defer cancel()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create archive staging file: %w", err)
	}
	// Every failure path closes and removes the partial file: no unverified
	// archive byte survives a failed download.
	cleanup := func() {
		_ = out.Close()
		_ = os.Remove(dst)
	}

	dest := release.Resolved().Tarball.URL
	resp, reqCancel, err := c.doValidated(totalCtx, dest, c.downloadClient(), packageFetchSpec)
	if err != nil {
		cleanup()
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		reqCancel()
		cleanup()
		return "", &FeedError{
			Reason:     fmt.Sprintf("release package request returned status %s", resp.Status),
			StatusCode: resp.StatusCode,
		}
	}
	// A declared length beyond the cap is rejected before a byte is
	// read; a lying smaller length is caught by the streamed cap below.
	if resp.ContentLength > archiveMaxCompressedBytes {
		_ = resp.Body.Close()
		reqCancel()
		cleanup()
		return "", &FeedError{Reason: fmt.Sprintf("release package declares more than the %d-MiB compressed limit", archiveMaxCompressedBytes>>20)}
	}
	// The watchdog cancels the final request context when the body stops
	// making progress, aborting a stalled read.
	watchdog := &inactivityWatchdog{cancel: reqCancel, timeout: downloadInactivityTimeout}
	watchdog.timer = time.AfterFunc(downloadInactivityTimeout, reqCancel)
	hash := sha256.New()
	limited := io.LimitReader(watchdog.wrap(resp.Body), archiveMaxCompressedBytes+1)
	_, copyErr := io.Copy(io.MultiWriter(out, hash), limited)
	_ = resp.Body.Close()
	watchdog.stop()
	if copyErr != nil {
		cleanup()
		return "", &FeedError{Reason: fmt.Sprintf("downloading release package: %v", copyErr)}
	}
	info, err := out.Stat()
	if err != nil {
		cleanup()
		return "", fmt.Errorf("stat staged archive: %w", err)
	}
	if info.Size() > archiveMaxCompressedBytes {
		cleanup()
		return "", &FeedError{Reason: fmt.Sprintf("release package exceeds the %d-MiB compressed limit", archiveMaxCompressedBytes>>20)}
	}
	if err := out.Chmod(0o600); err != nil {
		cleanup()
		return "", fmt.Errorf("repair staged archive permissions: %w", err)
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("sync staged archive: %w", err)
	}
	if err := out.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("close staged archive: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// downloadClient returns the client used for package streaming: the header
// wait is bounded by the inactivity timeout, while body inactivity and the
// total deadline are enforced explicitly by downloadArchive.
func (c *FeedClient) downloadClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The body watchdog arms only once headers arrive; bound that wait too.
	transport.ResponseHeaderTimeout = downloadInactivityTimeout
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// inactivityWatchdog cancels the request context when the wrapped body
// stops making progress within its timeout.
type inactivityWatchdog struct {
	cancel  context.CancelFunc
	timer   *time.Timer
	timeout time.Duration
}

func (w *inactivityWatchdog) stop() {
	w.timer.Stop()
	w.cancel()
}

type inactivityReader struct {
	r     io.Reader
	watch *inactivityWatchdog
}

func (i *inactivityReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.watch.timer.Reset(i.watch.timeout)
	}
	return n, err
}

func (w *inactivityWatchdog) wrap(body io.Reader) io.Reader {
	return &inactivityReader{r: body, watch: w}
}
