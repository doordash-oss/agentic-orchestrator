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

package slack

import (
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const resolutionRestart = "restart-reconciliation"

func scrubReloadedRecord(token string, record *featureRecord) {
	for key, entry := range record.Destinations {
		entry.DisplayName = scrub(token, entry.DisplayName)
		if entry.Failure != nil {
			entry.Failure.SlackError = scrub(token, entry.Failure.SlackError)
		}
		for i := range entry.PostingIndex {
			if resolution := entry.PostingIndex[i].Resolution; resolution != nil {
				resolution.ResponderName = scrub(token, resolution.ResponderName)
			}
		}
		record.Destinations[key] = entry
	}
	scrubItems := func(items []pendingInputRecord) {
		for i := range items {
			items[i].ReviewID = scrub(token, items[i].ReviewID)
			items[i].ReviewMode = scrub(token, items[i].ReviewMode)
			items[i].TargetPhase = scrub(token, items[i].TargetPhase)
			items[i].ArtifactID = scrub(token, items[i].ArtifactID)
			items[i].SourceRevision = scrub(token, items[i].SourceRevision)
			if items[i].Resolution != nil {
				items[i].Resolution.ResponderName = scrub(token, items[i].Resolution.ResponderName)
			}
		}
	}
	scrubItems(record.Pending)
	scrubItems(record.Resolved)
}

func destinationFingerprint(settings ports.SlackRuntimeSettings) string {
	var b strings.Builder
	b.WriteString(strconv.FormatBool(settings.Enabled))
	b.WriteByte(':')
	b.WriteString(strconv.FormatBool(settings.Token != ""))
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(settings.CredentialGeneration, 10))
	for _, recipient := range settings.Recipients {
		b.WriteByte('\x00')
		b.WriteString(string(recipient.Kind))
		b.WriteByte(':')
		b.WriteString(recipient.ID)
	}
	return b.String()
}

func terminalFeature(status feature.Status) bool {
	return status == feature.StatusDone ||
		status == feature.StatusPublished ||
		status == feature.StatusFailed
}

func (n *Notifier) restartFeatures() []*feature.Feature {
	features := make(map[string]*feature.Feature)
	if lister, ok := n.store.(featureLister); ok {
		listed, err := lister.List()
		if err != nil {
			log.Printf("slack-notifier: listing features for reconciliation failed: %v", err)
		}
		for _, f := range listed {
			if f != nil && !f.IsChild() {
				features[f.ID] = f
			}
		}
	} else {
		log.Printf("slack-notifier: feature enumeration unavailable; using Slack records")
	}
	n.recordMu.Lock()
	recordIDs := make([]string, 0, len(n.records))
	for id := range n.records {
		recordIDs = append(recordIDs, id)
	}
	n.recordMu.Unlock()
	for _, id := range recordIDs {
		if _, exists := features[id]; exists {
			continue
		}
		f, err := n.store.Load(id)
		if err != nil || f == nil {
			log.Printf("slack-notifier: skipping missing feature during reconciliation")
			continue
		}
		if !f.IsChild() {
			features[id] = f
		}
	}
	ids := make([]string, 0, len(features))
	for id := range features {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]*feature.Feature, 0, len(ids))
	for _, id := range ids {
		result = append(result, features[id])
	}
	return result
}

func (n *Notifier) runRestartPass() bool {
	settings := n.settings.SlackSettings()
	if !settings.Enabled || settings.Token == "" || len(settings.Recipients) == 0 {
		return true
	}
	if n.pending == nil || n.resolvedServerName() == "" {
		return false
	}
	var scanned, dispatched int
	allReadable := true
	for _, owner := range n.restartFeatures() {
		select {
		case <-n.stopCh:
			return false
		default:
		}
		record, err := n.recordFor(owner.ID)
		if err != nil {
			log.Printf("slack-notifier: skipping unreadable Slack record during reconciliation: %v", err)
			continue
		}
		n.recordMu.Lock()
		hadPending := len(record.Pending) > 0
		oldCount := len(record.Pending)
		hadOwed := false
		for _, resolved := range record.Resolved {
			for _, recipient := range settings.Recipients {
				if resolved.closureOwed(destinationKey(string(recipient.Kind), recipient.ID)) {
					hadOwed = true
				}
			}
		}
		n.recordMu.Unlock()
		if terminalFeature(owner.Status) && !hadPending && !hadOwed {
			continue
		}
		scanned++
		work, readable := n.reconcilePendingWithPolicy(
			settings, owner, owner, record, resolutionRestart, true,
		)
		if !readable {
			allReadable = false
			continue
		}
		n.recordMu.Lock()
		changed := len(record.Pending) != oldCount
		n.recordMu.Unlock()
		if !terminalFeature(owner.Status) || changed {
			for _, recipient := range settings.Recipients {
				key := destinationKey(string(recipient.Kind), recipient.ID)
				n.recordMu.Lock()
				entry := record.Destinations[key]
				n.recordMu.Unlock()
				if entry.RootTS == "" {
					channelID, err := n.resolveDestination(settings, record, owner.ID, recipient)
					if err != nil {
						log.Printf("slack-notifier: destination unavailable during reconciliation: %v", err)
						continue
					}
					work = append(work, workItem{
						featureID: owner.ID, sourceFeatureID: owner.ID,
						destinationKey: key, channelID: channelID,
						kind: string(recipient.Kind), needsCard: true,
					})
				} else if !hasRefreshWork(work, key) {
					work = append(work, workItem{
						featureID: owner.ID, sourceFeatureID: owner.ID,
						destinationKey: key, channelID: entry.ChannelID,
						kind: string(recipient.Kind), refresh: true,
					})
				}
			}
		}
		if len(work) == 0 {
			continue
		}
		dispatched += len(work)
		if !n.dispatchRestartWork(owner.ID, work) {
			return false
		}
	}
	log.Printf("slack-notifier: reconciliation complete: features=%d deliveries=%d", scanned, dispatched)
	return allReadable
}

func (n *Notifier) closureWork(
	settings ports.SlackRuntimeSettings, featureID string,
	record *featureRecord, queued []workItem, onlyIdentities map[string]bool,
) []workItem {
	n.recordMu.Lock()
	defer n.recordMu.Unlock()
	var work []workItem
	for _, inputs := range [][]pendingInputRecord{record.Pending, record.Resolved} {
		for _, tracked := range inputs {
			if onlyIdentities != nil && !onlyIdentities[tracked.Identity] {
				continue
			}
			for _, recipient := range settings.Recipients {
				key := destinationKey(string(recipient.Kind), recipient.ID)
				if !tracked.closureOwed(key) {
					continue
				}
				alreadyQueued := false
				for _, item := range queued {
					if item.destinationKey == key && item.reply.closure &&
						item.reply.identity == tracked.Identity {
						alreadyQueued = true
						break
					}
				}
				entry := record.Destinations[key]
				if alreadyQueued || entry.ChannelID == "" || entry.RootTS == "" ||
					!n.reserveClosureDelivery(featureID, tracked.Identity, key) {
					continue
				}
				line := tracked.Tag + " was resolved in Agentico."
				if tracked.Resolution.Kind == resolutionCleared {
					line = tracked.Tag + " is no longer pending."
				}
				work = append(work, workItem{
					featureID: featureID, sourceFeatureID: tracked.SourceFeatureID,
					destinationKey: key, channelID: entry.ChannelID,
					kind: string(recipient.Kind), responder: true,
					closureReserved: true,
					reply: replyPayload{
						kind: kindNeedsInput, identity: tracked.Identity,
						closure: true, fallback: scrub(settings.Token, line),
					},
				})
			}
		}
	}
	return work
}

func (n *Notifier) dispatchRestartWork(featureID string, work []workItem) bool {
	if len(work) == 0 {
		return true
	}
	done := make(chan struct{})
	item := queueItem{
		kind:  kindNeedsInput,
		event: ports.Event{Type: ports.SessionOutput, FeatureID: featureID},
	}
	item.reservation = n.queue.reserveProtected(item.event)
	n.dispatchDeliveryGroupWithDone(item, work, func() { close(done) })
	select {
	case <-done:
		return true
	case <-n.stopCh:
		return false
	}
}

// sweepDestinations runs only when the effective recipient set changes. The
// reconciliation path itself is idempotent for existing cards and item posts.
func (n *Notifier) sweepDestinations() {
	if !n.startupDone.Load() {
		return
	}
	settings := n.settings.SlackSettings()
	key := destinationFingerprint(settings)
	n.sweepMu.Lock()
	defer n.sweepMu.Unlock()
	if key == n.sweepKey {
		return
	}
	if !settings.Enabled || settings.Token == "" || len(settings.Recipients) == 0 {
		n.sweepKey = key
		return
	}
	if n.pending == nil {
		return
	}
	readableSweep := true
	for _, owner := range n.restartFeatures() {
		if n.stopped.Load() {
			return
		}
		if terminalFeature(owner.Status) {
			continue
		}
		record, err := n.recordFor(owner.ID)
		if err != nil {
			log.Printf("slack-notifier: skipping unreadable Slack record during bootstrap: %v", err)
			continue
		}
		work, readable := n.reconcilePendingWithPolicy(
			settings, owner, owner, record, resolutionAgentico, true,
		)
		if !readable {
			readableSweep = false
			continue
		}
		for _, recipient := range settings.Recipients {
			destination := destinationKey(string(recipient.Kind), recipient.ID)
			n.recordMu.Lock()
			entry := record.Destinations[destination]
			n.recordMu.Unlock()
			if entry.RootTS != "" {
				continue
			}
			alreadyEnsuresCard := false
			for _, item := range work {
				if item.destinationKey == destination && item.needsCard {
					alreadyEnsuresCard = true
					break
				}
			}
			if alreadyEnsuresCard {
				continue
			}
			channelID, err := n.resolveDestination(settings, record, owner.ID, recipient)
			if err != nil {
				log.Printf("slack-notifier: destination unavailable during bootstrap: %v", err)
				continue
			}
			work = append(work, workItem{
				featureID: owner.ID, sourceFeatureID: owner.ID,
				destinationKey: destination, channelID: channelID,
				kind: string(recipient.Kind), needsCard: true,
			})
		}
		if !n.dispatchRestartWork(owner.ID, work) {
			return
		}
	}
	if readableSweep {
		n.sweepKey = key
	}
}

func hasRefreshWork(work []workItem, destination string) bool {
	for _, item := range work {
		if item.destinationKey == destination && item.refresh {
			return true
		}
	}
	return false
}
