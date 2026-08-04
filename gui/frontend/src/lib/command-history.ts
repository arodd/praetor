import type { CommandHistoryEntry, CommandHistoryUpdate } from "./types";

export const COMMAND_HISTORY_LIMIT = 1000;

export interface CommandHistoryState {
  epoch: number;
  revision: number;
  entries: CommandHistoryEntry[];
}

export interface HistoryBrowseState {
  selectedId: number | null;
  draft: string;
  draftCursor: number;
}

export interface HistoryBrowseResult {
  state: HistoryBrowseState;
  text: string;
  cursor: number;
}

export interface HistoryCompletionState {
  prefix: string;
  matchingIds: number[];
  index: number;
}

export interface HistoryCompletionResult {
  state: HistoryCompletionState;
  text: string;
  entryId: number | null;
  matched: boolean;
}

export const emptyHistoryState = (): CommandHistoryState => ({
  epoch: 0,
  revision: 0,
  entries: [],
});

export const emptyBrowseState = (): HistoryBrowseState => ({
  selectedId: null,
  draft: "",
  draftCursor: 0,
});

export const emptyCompletionState = (): HistoryCompletionState => ({
  prefix: "",
  matchingIds: [],
  index: -1,
});

// Fold both HTTP acknowledgements and WebSocket updates through one reducer.
// A response that arrives ahead of an intervening WebSocket append is ignored;
// the sequenced socket updates will close the revision gap.
export function applyCommandHistoryUpdate(
  current: CommandHistoryState,
  update: CommandHistoryUpdate,
): CommandHistoryState {
  const epoch = finiteNonNegativeInteger(update.epoch);
  const revision = finiteNonNegativeInteger(update.revision);
  if (epoch === null || revision === null) return current;

  if (update.replace) {
    return {
      epoch,
      revision,
      entries: normalizeEntries(update.entries ?? []),
    };
  }
  if (epoch !== current.epoch || revision <= current.revision) return current;
  const appended = update.entry;
  if (revision !== current.revision + 1 || !validEntry(appended)) return current;
  if (current.entries.some((entry) => entry.id === appended.id)) return current;
  return {
    epoch,
    revision,
    entries: [...current.entries, appended].slice(-COMMAND_HISTORY_LIMIT),
  };
}

export function appendLocalCommandHistory(
  current: CommandHistoryState,
  text: string,
): CommandHistoryState {
  if (text.trim() === "") return current;
  const revision = current.revision + 1;
  const id = Math.max(
    revision,
    current.entries.length ? current.entries[current.entries.length - 1].id + 1 : 1,
  );
  return {
    epoch: current.epoch || 1,
    revision,
    entries: [...current.entries, { id, text }].slice(-COMMAND_HISTORY_LIMIT),
  };
}

export function browsePrevious(
  entries: CommandHistoryEntry[],
  browse: HistoryBrowseState,
  currentText: string,
  currentCursor: number,
): HistoryBrowseResult {
  if (entries.length === 0) {
    return { state: browse, text: currentText, cursor: currentCursor };
  }
  let state = browse;
  let index: number;
  if (browse.selectedId === null) {
    state = {
      selectedId: null,
      draft: currentText,
      draftCursor: clampCursor(currentCursor, currentText),
    };
    index = entries.length - 1;
  } else {
    const selected = entries.findIndex((entry) => entry.id === browse.selectedId);
    if (selected < 0) {
      // The bounded server ring evicted the selected entry. Preserve recalled
      // text as a detached draft and begin from the new tail.
      state = {
        selectedId: null,
        draft: currentText,
        draftCursor: clampCursor(currentCursor, currentText),
      };
      index = entries.length - 1;
    } else {
      index = Math.max(0, selected - 1);
    }
  }
  const entry = entries[index];
  return {
    state: { ...state, selectedId: entry.id },
    text: entry.text,
    cursor: entry.text.length,
  };
}

export function browseNext(
  entries: CommandHistoryEntry[],
  browse: HistoryBrowseState,
  currentText: string,
  currentCursor: number,
): HistoryBrowseResult {
  if (browse.selectedId === null) {
    return { state: browse, text: currentText, cursor: currentCursor };
  }
  const selected = entries.findIndex((entry) => entry.id === browse.selectedId);
  if (selected < 0 || selected >= entries.length - 1) {
    return {
      state: { ...browse, selectedId: null },
      text: browse.draft,
      cursor: clampCursor(browse.draftCursor, browse.draft),
    };
  }
  const entry = entries[selected + 1];
  return {
    state: { ...browse, selectedId: entry.id },
    text: entry.text,
    cursor: entry.text.length,
  };
}

// Tab replaces the input with the newest case-insensitive history entry whose
// complete text starts with the current non-empty command. Further Tab presses
// walk older matches; Shift+Tab walks back toward newer matches. Matching IDs
// are frozen for a cycle so remote appends cannot move an active candidate.
export function completeFromHistory(
  entries: CommandHistoryEntry[],
  currentText: string,
  completion: HistoryCompletionState,
  direction: 1 | -1 = 1,
): HistoryCompletionResult {
  let state = completion;
  const currentEntry = completion.index >= 0
    ? entries.find((entry) => entry.id === completion.matchingIds[completion.index])
    : undefined;
  const continuing = completion.prefix !== "" && currentEntry?.text === currentText;

  if (!continuing) {
    if (currentText === "") {
      return { state: emptyCompletionState(), text: currentText, entryId: null, matched: false };
    }
    const prefix = currentText.toLocaleLowerCase();
    const matchingIds = entries
      .filter((entry) => entry.text.toLocaleLowerCase().startsWith(prefix))
      .map((entry) => entry.id)
      .reverse();
    if (matchingIds.length === 0) {
      return { state: emptyCompletionState(), text: currentText, entryId: null, matched: false };
    }
    // A fresh cycle always begins at the newest match. Shift only reverses an
    // established cycle; beginning Shift+Tab at the oldest retained command
    // would be surprising and makes the useful recent match expensive to
    // reach on a long history.
    state = { prefix: currentText, matchingIds, index: 0 };
  } else {
    const length = completion.matchingIds.length;
    state = {
      ...completion,
      index: (completion.index + direction + length) % length,
    };
  }

  const id = state.matchingIds[state.index];
  const entry = entries.find((candidate) => candidate.id === id);
  if (!entry) {
    return { state: emptyCompletionState(), text: currentText, entryId: null, matched: false };
  }
  return { state, text: entry.text, entryId: entry.id, matched: true };
}

function normalizeEntries(entries: CommandHistoryEntry[]): CommandHistoryEntry[] {
  const seen = new Set<number>();
  const normalized: CommandHistoryEntry[] = [];
  for (const entry of entries) {
    if (!validEntry(entry) || seen.has(entry.id)) continue;
    seen.add(entry.id);
    normalized.push({ id: entry.id, text: entry.text });
  }
  return normalized.slice(-COMMAND_HISTORY_LIMIT);
}

function validEntry(entry: CommandHistoryEntry | undefined): entry is CommandHistoryEntry {
  return !!entry && Number.isSafeInteger(entry.id) && entry.id > 0 && typeof entry.text === "string";
}

function finiteNonNegativeInteger(value: number): number | null {
  return Number.isSafeInteger(value) && value >= 0 ? value : null;
}

function clampCursor(cursor: number, text: string): number {
  return Math.max(0, Math.min(Number.isFinite(cursor) ? Math.trunc(cursor) : text.length, text.length));
}
