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
	"bytes"
	"errors"
	"fmt"
)

// VerifiedCandidate couples one staged release executable with its
// authenticated provenance and the successful isolated version probe. It is
// constructed only by AdmitStagedRelease after the probe accepted the exact
// target banner, so a caller-provided path, digest, or fabricated verified
// flag can never stand in for the trust chain.
type VerifiedCandidate struct {
	release          VerifiedRelease
	executablePath   string
	executableDigest string
	probe            ProbeResult
}

// Release returns the authenticated release provenance.
func (c VerifiedCandidate) Release() VerifiedRelease { return c.release }

// ExecutablePath returns the staged executable's path.
func (c VerifiedCandidate) ExecutablePath() string { return c.executablePath }

// ExecutableDigest returns the sha256 hex digest of the staged executable.
func (c VerifiedCandidate) ExecutableDigest() string { return c.executableDigest }

// Probe returns the successful isolated probe result.
func (c VerifiedCandidate) Probe() ProbeResult { return c.probe }

// AdmitStagedRelease admits one staged release as a verified candidate after
// its isolated probe succeeded. The probe's banner version must equal the
// release target, and the probe's recorded executable digest must equal the
// staged candidate digest, so no unverified manifest, archive, envelope, or
// unsafe extracted entry can reach execution through this path.
func AdmitStagedRelease(staged *StagedRelease, probe ProbeResult) (VerifiedCandidate, error) {
	if staged == nil {
		return VerifiedCandidate{}, errors.New("no staged release to admit")
	}
	if staged.candidatePath == "" || staged.candidateDigest == "" {
		return VerifiedCandidate{}, errors.New("staged release is incomplete")
	}
	target := staged.release.Resolved().Version
	if probe.Version != target {
		return VerifiedCandidate{}, fmt.Errorf("probe reported version %q, want the release target %q", probe.Version, target)
	}
	if probe.ExecutableDigest != staged.candidateDigest {
		return VerifiedCandidate{}, fmt.Errorf("probe digest %s does not match the staged candidate digest %s", probe.ExecutableDigest, staged.candidateDigest)
	}
	return VerifiedCandidate{
		release:          staged.release,
		executablePath:   staged.candidatePath,
		executableDigest: staged.candidateDigest,
		probe:            probe,
	}, nil
}

// RevalidateVerifiedCandidate re-runs every trust check from the retained
// provenance immediately before the candidate is copied into transaction
// staging: the manifest signature is re-verified over the retained bytes
// with the retained trust root — which must be one of the two embeddings —
// the asset/digest bindings are re-derived from the manifest bytes, the
// staged executable's actual bytes are re-digested, and the target must
// still be eligible, unsuppressed, and strictly newer. Nothing is refetched
// and no probe is re-run.
func RevalidateVerifiedCandidate(exec Executable, cand VerifiedCandidate, opts StageReleaseOptions) error {
	if err := revalidateReleaseProvenance(exec, cand, opts); err != nil {
		return err
	}
	// The staged executable must still be exactly the admitted bytes.
	if err := VerifyCandidate(cand.executablePath, cand.executableDigest); err != nil {
		return fmt.Errorf("verified candidate executable: %w", err)
	}
	return nil
}

// revalidateReleaseProvenance is the commit-boundary form: it re-runs every
// retained-provenance check (signature, bindings, target admission) without
// touching the original release-staging candidate path — the transaction's
// own staged copy is re-verified directly by Commit, and the release staging
// directory may already have been cleaned once the candidate was copied.
func revalidateReleaseProvenance(exec Executable, cand VerifiedCandidate, opts StageReleaseOptions) error {
	rel := cand.release
	if len(rel.manifestBytes) == 0 || rel.manifestSignature == "" || rel.tarballDigest == "" {
		return errors.New("verified candidate carries incomplete manifest provenance")
	}
	if cand.executableDigest == "" || cand.probe.Version == "" {
		return errors.New("verified candidate carries incomplete admission state")
	}
	production, err := ProductionReleasePublicKey()
	if err != nil {
		return err
	}
	fixture, err := FixtureReleasePublicKey()
	if err != nil {
		return err
	}
	if !bytes.Equal(rel.trustRoot, production) && !bytes.Equal(rel.trustRoot, fixture) {
		return errors.New("verified candidate trust root is not a known release embedding")
	}
	if err := VerifyReleaseSignature(rel.manifestBytes, rel.manifestSignature, rel.trustRoot); err != nil {
		return fmt.Errorf("retained manifest provenance: %w", err)
	}
	entries, err := parseChecksumManifest(rel.manifestBytes)
	if err != nil {
		return fmt.Errorf("retained manifest provenance: %w", err)
	}
	tarballDigest, ok := entries[rel.resolved.Tarball.Name]
	if !ok || tarballDigest != rel.tarballDigest {
		return fmt.Errorf("retained manifest no longer binds %q to its digest", rel.resolved.Tarball.Name)
	}
	if rel.resolved.Envelope != nil {
		envelopeDigest, ok := entries[rel.resolved.Envelope.Name]
		if !ok || envelopeDigest != rel.envelopeDigest {
			return fmt.Errorf("retained manifest no longer binds %q to its digest", rel.resolved.Envelope.Name)
		}
	}
	if err := ValidateReleaseTarget(exec, rel, opts); err != nil {
		return fmt.Errorf("verified candidate target: %w", err)
	}
	return nil
}

// BeginVerifiedRelease prepares the durable replacement transaction for one
// verifier-produced candidate. Unlike the local-candidate Begin, the
// release-backed path requires complete verifier provenance: the retained
// manifest signature and asset/digest bindings are re-verified, the staged
// executable's bytes are re-digested, and the target must still be eligible,
// unsuppressed, and strictly newer before any backup or receipt is written.
// The provenance rides along on the transaction and is re-validated again
// immediately before commit.
func BeginVerifiedRelease(exec Executable, cand VerifiedCandidate, opts BeginOptions, stageOpts StageReleaseOptions, seams FileOps) (*Transaction, error) {
	if err := RevalidateVerifiedCandidate(exec, cand, stageOpts); err != nil {
		return nil, err
	}
	target := cand.release.Resolved().Version
	if opts.ToVersion != target {
		return nil, fmt.Errorf("transaction target version %q does not match the verified release %q", opts.ToVersion, target)
	}
	opts.CandidatePath = cand.executablePath
	opts.CandidateDigest = cand.executableDigest
	t, err := Begin(exec, opts, seams)
	if err != nil {
		return nil, err
	}
	t.releaseProvenance = &releaseProvenance{candidate: cand, stageOpts: stageOpts}
	return t, nil
}

// releaseProvenance is the commit-boundary revalidation state attached to a
// release-backed transaction.
type releaseProvenance struct {
	candidate VerifiedCandidate
	stageOpts StageReleaseOptions
}
