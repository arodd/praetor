package web

import (
	"errors"
	"strings"
)

const (
	// Command history is intentionally process-memory state. It is shared by
	// authenticated browsers for one explicit game-login epoch, but is never
	// written to config, browser storage, or the session transcript.
	maxCommandHistoryEntries = 1000
	maxCommandHistoryDedupe  = 2048
)

var errCommandSubmissionIDReuse = errors.New(
	"command submission ID was reused with different content",
)

// CommandHistoryEntry is one deliberately submitted gameplay-input line.
// IDs are server-owned and stable only within Epoch.
type CommandHistoryEntry struct {
	ID   uint64 `json:"id"`
	Text string `json:"text"`
}

// CommandHistoryUpdate is either an authoritative replacement (snapshots and
// explicit game-login resets) or one ordered append. Revision advances once
// per non-empty entry; Epoch changes atomically when a new login starts.
type CommandHistoryUpdate struct {
	Epoch    uint64                `json:"epoch"`
	Revision uint64                `json:"revision"`
	Replace  bool                  `json:"replace,omitempty"`
	Entries  []CommandHistoryEntry `json:"entries,omitempty"`
	Entry    *CommandHistoryEntry  `json:"entry,omitempty"`
}

type typedCommandResult struct {
	History CommandHistoryUpdate `json:"history"`
}

type commandSubmissionKey struct {
	browserID    string
	submissionID string
}

type commandSubmissionRecord struct {
	input       string
	disposition string
	result      typedCommandResult
}

// commandHistoryAuthority has no internal lock. Hub owns it and calls every
// method while holding Hub.mu so snapshot registration, appends, resets, and
// WebSocket sequencing share one synchronization boundary.
type commandHistoryAuthority struct {
	epoch       uint64
	revision    uint64
	nextEntryID uint64
	entries     []CommandHistoryEntry
	dedupe      map[commandSubmissionKey]commandSubmissionRecord
	dedupeOrder []commandSubmissionKey
}

func newCommandHistoryAuthority() *commandHistoryAuthority {
	return &commandHistoryAuthority{
		epoch:   1,
		entries: make([]CommandHistoryEntry, 0, maxCommandHistoryEntries),
		dedupe:  make(map[commandSubmissionKey]commandSubmissionRecord),
	}
}

func (h *commandHistoryAuthority) snapshot() CommandHistoryUpdate {
	return CommandHistoryUpdate{
		Epoch:    h.epoch,
		Revision: h.revision,
		Replace:  true,
		Entries:  append([]CommandHistoryEntry(nil), h.entries...),
	}
}

func (h *commandHistoryAuthority) lookup(
	browserID, submissionID, disposition, input string,
) (typedCommandResult, bool, error) {
	record, ok := h.dedupe[commandSubmissionKey{
		browserID: browserID, submissionID: submissionID,
	}]
	if !ok {
		return typedCommandResult{}, false, nil
	}
	if record.input != input || record.disposition != disposition {
		return typedCommandResult{}, false, errCommandSubmissionIDReuse
	}
	return cloneTypedCommandResult(record.result), true, nil
}

func (h *commandHistoryAuthority) commit(
	browserID, submissionID, disposition, input string,
) (typedCommandResult, error) {
	if result, ok, err := h.lookup(
		browserID, submissionID, disposition, input,
	); ok || err != nil {
		return result, err
	}

	update := CommandHistoryUpdate{
		Epoch:    h.epoch,
		Revision: h.revision,
	}
	if strings.TrimSpace(input) != "" {
		h.nextEntryID++
		h.revision++
		entry := CommandHistoryEntry{ID: h.nextEntryID, Text: input}
		h.entries = append(h.entries, entry)
		if overflow := len(h.entries) - maxCommandHistoryEntries; overflow > 0 {
			copy(h.entries, h.entries[overflow:])
			h.entries = h.entries[:maxCommandHistoryEntries]
		}
		update.Revision = h.revision
		update.Entry = &entry
	}

	result := typedCommandResult{History: update}
	key := commandSubmissionKey{
		browserID: browserID, submissionID: submissionID,
	}
	h.dedupe[key] = commandSubmissionRecord{
		input:       input,
		disposition: disposition,
		result:      cloneTypedCommandResult(result),
	}
	h.dedupeOrder = append(h.dedupeOrder, key)
	if overflow := len(h.dedupeOrder) - maxCommandHistoryDedupe; overflow > 0 {
		for _, expired := range h.dedupeOrder[:overflow] {
			delete(h.dedupe, expired)
		}
		copy(h.dedupeOrder, h.dedupeOrder[overflow:])
		h.dedupeOrder = h.dedupeOrder[:len(h.dedupeOrder)-overflow]
	}
	return result, nil
}

func (h *commandHistoryAuthority) reset() CommandHistoryUpdate {
	h.epoch++
	h.revision = 0
	h.nextEntryID = 0
	h.entries = make([]CommandHistoryEntry, 0, maxCommandHistoryEntries)
	h.dedupe = make(map[commandSubmissionKey]commandSubmissionRecord)
	h.dedupeOrder = nil
	return h.snapshot()
}

func cloneTypedCommandResult(source typedCommandResult) typedCommandResult {
	result := source
	result.History.Entries = append(
		[]CommandHistoryEntry(nil), source.History.Entries...,
	)
	if source.History.Entry != nil {
		entry := *source.History.Entry
		result.History.Entry = &entry
	}
	return result
}
