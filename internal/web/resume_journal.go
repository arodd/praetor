package web

import (
	"encoding/json"

	appgui "github.com/cyber-godzilla/praetor/internal/gui"
)

type resumeJournalEntry struct {
	envelope       Envelope
	estimatedBytes int
	eventCount     int
}

func (h *Hub) appendResumeJournalLocked(envelope Envelope, estimatedBytes int) {
	entry := resumeJournalEntry{
		envelope:       cloneJournalEnvelope(envelope),
		estimatedBytes: estimatedBytes,
		eventCount:     envelopeEventCount(envelope),
	}
	h.journal = append(h.journal, entry)
	h.journalBytes += entry.estimatedBytes
	h.journalEvents += entry.eventCount
	for len(h.journal) > resumeJournalMaxEnvelopes ||
		h.journalEvents > resumeJournalMaxEvents ||
		h.journalBytes > resumeJournalMaxBytes {
		expired := h.journal[0]
		h.journal[0] = resumeJournalEntry{}
		h.journal = h.journal[1:]
		h.journalBytes -= expired.estimatedBytes
		h.journalEvents -= expired.eventCount
	}
	if len(h.journal) == 0 {
		h.journal = nil
		h.journalBytes = 0
		h.journalEvents = 0
	}
}

func (h *Hub) resumeEntriesLocked(
	serverID string,
	afterSequence uint64,
) ([]resumeJournalEntry, bool) {
	if serverID != h.serverID || afterSequence > h.sequence {
		return nil, false
	}
	if afterSequence == h.sequence {
		return nil, true
	}

	expected := afterSequence + 1
	start := -1
	totalEvents, totalBytes := 0, 0
	for index, entry := range h.journal {
		sequence := entry.envelope.Sequence
		if sequence <= afterSequence {
			continue
		}
		if start < 0 {
			start = index
		}
		if sequence != expected {
			return nil, false
		}
		expected++
		totalEvents += entry.eventCount
		totalBytes += entry.estimatedBytes
		if totalEvents > subscriberBacklogMaxEvents ||
			totalBytes > subscriberBacklogMaxBytes {
			return nil, false
		}
	}
	if start < 0 || expected != h.sequence+1 {
		return nil, false
	}
	return h.journal[start:], true
}

func cloneJournalEnvelope(source Envelope) Envelope {
	result := source
	result.Events = append([]appgui.WireEvent(nil), source.Events...)
	if raw, ok := source.Config.(json.RawMessage); ok {
		result.Config = append(json.RawMessage(nil), raw...)
	}
	result.ModeNames = append([]string(nil), source.ModeNames...)
	if source.Accounts != nil {
		accounts := append([]string(nil), (*source.Accounts)...)
		result.Accounts = &accounts
	}
	if source.CredentialStore != nil {
		credentialStore := *source.CredentialStore
		result.CredentialStore = &credentialStore
	}
	if source.CommandHistory != nil {
		history := *source.CommandHistory
		history.Entries = append([]CommandHistoryEntry(nil), history.Entries...)
		if source.CommandHistory.Entry != nil {
			entry := *source.CommandHistory.Entry
			history.Entry = &entry
		}
		result.CommandHistory = &history
	}
	if source.Result != nil {
		operation := *source.Result
		result.Result = &operation
	}
	return result
}
