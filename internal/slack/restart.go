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
	"reflect"
	"slices"
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

type slackSectionRevisionSource interface {
	SlackNotificationsRevision() uint64
}

func (n *Notifier) sectionRevision() uint64 {
	if source, ok := n.store.(slackSectionRevisionSource); ok {
		return source.SlackNotificationsRevision()
	}
	return 0
}

func (n *Notifier) captureSweepStates(settings ports.SlackRuntimeSettings) {
	states := make(map[string]feature.EffectiveSlack)
	for _, owner := range n.restartFeatures() {
		_, states[owner.ID] = n.effectiveSettings(settings, owner)
	}
	n.sweepStates = states
	n.sweepFeatureRevision = n.sectionRevision()
}

func effectiveRecipientUnion(n *Notifier, settings ports.SlackRuntimeSettings, owners []*feature.Feature) ports.SlackRuntimeSettings {
	union := settings
	union.Recipients = append([]ports.SlackRecipient(nil), settings.Recipients...)
	seen := make(map[string]bool, len(union.Recipients))
	for _, recipient := range union.Recipients {
		seen[destinationKey(string(recipient.Kind), recipient.ID)] = true
	}
	for _, owner := range owners {
		effective, state := n.effectiveSettings(settings, owner)
		if state.Muted {
			continue
		}
		for _, recipient := range effective.Recipients {
			key := destinationKey(string(recipient.Kind), recipient.ID)
			if seen[key] {
				continue
			}
			seen[key] = true
			union.Recipients = append(union.Recipients, ports.SlackRecipient{
				TypedText: recipient.TypedText, Kind: ports.SlackRecipientKind(recipient.Kind),
				ID: recipient.ID, DisplayName: recipient.DisplayName,
			})
		}
	}
	return union
}

func (n *Notifier) settingsForPass(settings, permitted ports.SlackRuntimeSettings, owner *feature.Feature) (ports.SlackRuntimeSettings, feature.EffectiveSlack) {
	effective, state := n.effectiveSettings(settings, owner)
	allowed := make(map[string]bool, len(permitted.Recipients))
	for _, recipient := range permitted.Recipients {
		allowed[destinationKey(string(recipient.Kind), recipient.ID)] = true
	}
	recipients := effective.Recipients[:0:0]
	for _, recipient := range effective.Recipients {
		if allowed[destinationKey(string(recipient.Kind), recipient.ID)] {
			recipients = append(recipients, recipient)
		}
	}
	effective.Recipients = recipients
	return effective, state
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
	if !settings.Enabled || settings.Token == "" {
		return true
	}
	if n.pending == nil || n.resolvedServerName() == "" {
		return false
	}
	owners := n.restartFeatures()
	union := effectiveRecipientUnion(n, settings, owners)
	if len(union.Recipients) == 0 {
		return true
	}
	ready, opening := n.partitionOpeningRecipients(union, owners)
	if len(opening.Recipients) == 0 {
		readable, scanned, dispatched := n.runRestartPassFor(union, owners)
		log.Printf("slack-notifier: reconciliation complete: features=%d deliveries=%d", scanned, dispatched)
		return readable
	}
	type passResult struct {
		readable bool
		scanned  int
		sent     int
	}
	done := make(chan passResult, len(opening.Recipients))
	for _, recipient := range opening.Recipients {
		destination := opening
		destination.Recipients = []ports.SlackRecipient{recipient}
		go func() {
			readable, scanned, sent := n.runRestartPassFor(destination, owners)
			done <- passResult{readable, scanned, sent}
		}()
	}
	readable, scanned, dispatched := n.runRestartPassFor(ready, owners)
	for i := 0; i < len(opening.Recipients); i++ {
		select {
		case opened := <-done:
			scanned = max(scanned, opened.scanned)
			dispatched += opened.sent
			readable = readable && opened.readable
		case <-n.stopCh:
			for ; i < len(opening.Recipients); i++ {
				<-done
			}
			return false
		}
	}
	log.Printf("slack-notifier: reconciliation complete: features=%d deliveries=%d", scanned, dispatched)
	return readable
}

// A new DM is resolved in its own pass so its pacing and retry waits cannot
// withhold writes to destinations whose channel is already known.
func (n *Notifier) partitionOpeningRecipients(
	settings ports.SlackRuntimeSettings, owners []*feature.Feature,
) (ports.SlackRuntimeSettings, ports.SlackRuntimeSettings) {
	ready, opening := settings, settings
	ready.Recipients = nil
	opening.Recipients = nil
	for _, recipient := range settings.Recipients {
		unresolved := false
		if recipient.Kind == ports.SlackRecipientUser {
			key := destinationKey(string(recipient.Kind), recipient.ID)
			n.openingMu.Lock()
			unresolved = n.openingTail[key] != nil
			n.openingMu.Unlock()
			for _, owner := range owners {
				if unresolved {
					break
				}
				if terminalFeature(owner.Status) {
					continue
				}
				effective, state := n.effectiveSettings(settings, owner)
				if state.Muted || !recipientEligible(effective, key) {
					continue
				}
				record, err := n.recordFor(owner.ID)
				if err != nil {
					continue
				}
				n.recordMu.Lock()
				entry := record.Destinations[key]
				n.recordMu.Unlock()
				if entry.ChannelID == "" {
					unresolved = true
					break
				}
			}
		}
		if unresolved {
			opening.Recipients = append(opening.Recipients, recipient)
		} else {
			ready.Recipients = append(ready.Recipients, recipient)
		}
	}
	return ready, opening
}

func (n *Notifier) runRestartPassFor(settings ports.SlackRuntimeSettings, owners []*feature.Feature) (bool, int, int) {
	var scanned, dispatched int
	allReadable := true
	failedResolutions := make(map[string]bool)
	for _, owner := range owners {
		effective, state := n.settingsForPass(n.settings.SlackSettings(), settings, owner)
		if len(effective.Recipients) == 0 || state.Muted {
			continue
		}
		select {
		case <-n.stopCh:
			return false, scanned, dispatched
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
			for _, recipient := range effective.Recipients {
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
		work, readable, unreadable, _ := n.reconcilePendingWithPolicy(
			effective, owner, owner, record, resolutionRestart, true, failedResolutions,
		)
		if !readable {
			allReadable = false
			continue
		}
		if len(unreadable) > 0 {
			allReadable = false
		}
		n.recordMu.Lock()
		changed := len(record.Pending) != oldCount
		n.recordMu.Unlock()
		if terminalFeature(owner.Status) {
			work = slices.DeleteFunc(work, func(item workItem) bool {
				n.recordMu.Lock()
				entry := record.Destinations[item.destinationKey]
				n.recordMu.Unlock()
				return entry.RootTS == ""
			})
		}
		if !terminalFeature(owner.Status) || changed || hadOwed || hasClosureWorkForAny(work) {
			for _, recipient := range effective.Recipients {
				key := destinationKey(string(recipient.Kind), recipient.ID)
				n.recordMu.Lock()
				entry := record.Destinations[key]
				n.recordMu.Unlock()
				if entry.RootTS != "" && !hasRefreshWork(work, key) {
					work = append(work, workItem{
						featureID: owner.ID, sourceFeatureID: owner.ID,
						destinationKey: key, channelID: entry.ChannelID,
						kind: string(recipient.Kind), refresh: true,
					})
				}
			}
		}
		if !terminalFeature(owner.Status) {
			work = n.appendMissingCardWork(effective, owner.ID, record, work, failedResolutions)
		}
		if len(work) == 0 {
			continue
		}
		dispatched += len(work)
		if !n.dispatchRestartWork(owner.ID, work) {
			return false, scanned, dispatched
		}
	}
	return allReadable, scanned, dispatched
}

func (n *Notifier) closureWork(
	settings ports.SlackRuntimeSettings, featureID string,
	record *featureRecord, onlyIdentities map[string]bool,
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
				entry := record.Destinations[key]
				if entry.ChannelID == "" || entry.RootTS == "" ||
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
	revision := n.sectionRevision()
	n.sweepMu.Lock()
	defer n.sweepMu.Unlock()
	if key == n.sweepKey && revision == n.sweepFeatureRevision {
		return
	}
	if !settings.Enabled || settings.Token == "" {
		n.sweepKey = key
		n.sweepFeatureRevision = revision
		n.sweepStates = make(map[string]feature.EffectiveSlack)
		return
	}
	if n.pending == nil {
		return
	}
	owners := n.restartFeatures()
	readableSweep := true
	next := make(map[string]feature.EffectiveSlack, len(owners))
	var bootstrap []*feature.Feature
	var refresh []*feature.Feature
	for _, owner := range owners {
		_, state := n.effectiveSettings(settings, owner)
		next[owner.ID] = state
		if terminalFeature(owner.Status) {
			continue
		}
		previous, existed := n.sweepStates[owner.ID]
		if existed && reflect.DeepEqual(previous, state) {
			continue
		}
		if state.Muted {
			if (!existed || !previous.Muted) && !n.refreshTransitionCards(settings, owner, true) {
				readableSweep = false
			}
			continue
		}
		added := !existed || hasNewEffectiveDestination(previous, state)
		enabled := existed && (previous.Muted ||
			(!previous.NeedsInput.Enabled && state.NeedsInput.Enabled) ||
			(!previous.Progress.Enabled && state.Progress.Enabled) ||
			(!previous.Problems.Enabled && state.Problems.Enabled))
		if added || enabled {
			bootstrap = append(bootstrap, owner)
		}
		if enabled || (existed && previous.NeedsInput.Enabled != state.NeedsInput.Enabled) {
			refresh = append(refresh, owner)
		}
	}
	if len(bootstrap) > 0 && !n.sweepChangedDestinations(settings, bootstrap) {
		readableSweep = false
	}
	for _, owner := range refresh {
		if !n.refreshTransitionCards(settings, owner, false) {
			readableSweep = false
		}
	}
	if readableSweep {
		n.sweepKey = key
		n.sweepFeatureRevision = revision
		n.sweepStates = next
	}
}

func (n *Notifier) sweepChangedDestinations(settings ports.SlackRuntimeSettings, owners []*feature.Feature) bool {
	union := effectiveRecipientUnion(n, settings, owners)
	ready, opening := n.partitionOpeningRecipients(union, owners)
	done := make(chan bool, len(opening.Recipients))
	for _, recipient := range opening.Recipients {
		destination := opening
		destination.Recipients = []ports.SlackRecipient{recipient}
		go func() { done <- n.sweepDestinationsFor(destination, owners) }()
	}
	readable := n.sweepDestinationsFor(ready, owners)
	for range opening.Recipients {
		select {
		case success := <-done:
			readable = readable && success
		case <-n.stopCh:
			return false
		}
	}
	return readable
}

func hasNewEffectiveDestination(before, after feature.EffectiveSlack) bool {
	known := make(map[string]bool, len(before.Recipients))
	for _, recipient := range before.Recipients {
		known[destinationKey(recipient.Kind, recipient.ID)] = true
	}
	for _, recipient := range after.Recipients {
		if !known[destinationKey(recipient.Kind, recipient.ID)] {
			return true
		}
	}
	return false
}

func (n *Notifier) refreshTransitionCards(settings ports.SlackRuntimeSettings, owner *feature.Feature, paused bool) bool {
	effective, _ := n.effectiveSettings(settings, owner)
	record, err := n.recordFor(owner.ID)
	if err != nil {
		return false
	}
	work := make([]workItem, 0, len(effective.Recipients))
	for _, recipient := range effective.Recipients {
		key := destinationKey(string(recipient.Kind), recipient.ID)
		n.recordMu.Lock()
		entry := record.Destinations[key]
		n.recordMu.Unlock()
		if entry.RootTS == "" {
			continue
		}
		work = append(work, workItem{
			featureID: owner.ID, sourceFeatureID: owner.ID,
			destinationKey: key, channelID: entry.ChannelID,
			kind: string(recipient.Kind), refresh: true, pauseCard: paused,
		})
	}
	return n.dispatchRestartWork(owner.ID, work)
}

func (n *Notifier) sweepDestinationsFor(settings ports.SlackRuntimeSettings, owners []*feature.Feature) bool {
	readableSweep := true
	failedResolutions := make(map[string]bool)
	for _, owner := range owners {
		if n.stopped.Load() {
			return false
		}
		effective, state := n.settingsForPass(n.settings.SlackSettings(), settings, owner)
		if state.Muted || len(effective.Recipients) == 0 {
			continue
		}
		if terminalFeature(owner.Status) {
			continue
		}
		record, err := n.recordFor(owner.ID)
		if err != nil {
			log.Printf("slack-notifier: skipping unreadable Slack record during bootstrap: %v", err)
			continue
		}
		work, readable, unreadable, _ := n.reconcilePendingWithPolicy(
			effective, owner, owner, record, resolutionAgentico, true, failedResolutions,
		)
		if !readable {
			readableSweep = false
			continue
		}
		if len(unreadable) > 0 {
			readableSweep = false
		}
		work = n.appendMissingCardWork(effective, owner.ID, record, work, failedResolutions)
		if !n.dispatchRestartWork(owner.ID, work) {
			return false
		}
	}
	return readableSweep
}

func (n *Notifier) appendMissingCardWork(
	settings ports.SlackRuntimeSettings, featureID string,
	record *featureRecord, work []workItem, failedResolutions map[string]bool,
) []workItem {
	for _, recipient := range settings.Recipients {
		key := destinationKey(string(recipient.Kind), recipient.ID)
		if failedResolutions[key] {
			continue
		}
		n.recordMu.Lock()
		entry := record.Destinations[key]
		n.recordMu.Unlock()
		if entry.RootTS != "" {
			continue
		}
		ensured := false
		for _, item := range work {
			if item.destinationKey == key && item.needsCard {
				ensured = true
				break
			}
		}
		if ensured {
			continue
		}
		channelID, err := n.resolveDestination(settings, record, featureID, recipient)
		if err != nil {
			failedResolutions[key] = true
			log.Printf("slack-notifier: destination unavailable during bootstrap: %v", err)
			continue
		}
		work = append(work, workItem{
			featureID: featureID, sourceFeatureID: featureID,
			destinationKey: key, channelID: channelID,
			kind: string(recipient.Kind), needsCard: true,
		})
	}
	return work
}

func hasRefreshWork(work []workItem, destination string) bool {
	for _, item := range work {
		if item.destinationKey == destination && item.refresh {
			return true
		}
	}
	return false
}

func hasClosureWork(work []workItem, destination string) bool {
	for _, item := range work {
		if item.destinationKey == destination && item.reply.closure {
			return true
		}
	}
	return false
}

func hasClosureWorkForAny(work []workItem) bool {
	for _, item := range work {
		if item.reply.closure {
			return true
		}
	}
	return false
}
