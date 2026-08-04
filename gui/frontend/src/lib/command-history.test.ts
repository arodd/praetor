import { describe, expect, it } from "vitest";
import {
  COMMAND_HISTORY_LIMIT,
  appendLocalCommandHistory,
  applyCommandHistoryUpdate,
  browseNext,
  browsePrevious,
  completeFromHistory,
  emptyBrowseState,
  emptyCompletionState,
  emptyHistoryState,
} from "./command-history";

describe("command history synchronization", () => {
  it("folds snapshots, ordered appends, response/socket duplicates, and epoch resets", () => {
    let state = applyCommandHistoryUpdate(emptyHistoryState(), {
      epoch: 4,
      revision: 2,
      replace: true,
      entries: [
        { id: 1, text: "look" },
        { id: 2, text: "skills" },
      ],
    });
    state = applyCommandHistoryUpdate(state, {
      epoch: 4,
      revision: 3,
      entry: { id: 3, text: "cond" },
    });
    const once = state;
    state = applyCommandHistoryUpdate(state, {
      epoch: 4,
      revision: 3,
      entry: { id: 3, text: "cond" },
    });
    expect(state).toBe(once);
    expect(state.entries.map((entry) => entry.text)).toEqual(["look", "skills", "cond"]);

    // An HTTP response cannot jump over a concurrent append that the ordered
    // WebSocket still needs to deliver.
    state = applyCommandHistoryUpdate(state, {
      epoch: 4,
      revision: 5,
      entry: { id: 5, text: "inventory" },
    });
    expect(state.revision).toBe(3);

    state = applyCommandHistoryUpdate(state, {
      epoch: 5,
      revision: 0,
      replace: true,
      entries: [],
    });
    expect(state).toEqual({ epoch: 5, revision: 0, entries: [] });
  });

  it("retains exactly the newest 1000 deliberate local submissions, including duplicates", () => {
    let state = emptyHistoryState();
    state = appendLocalCommandHistory(state, "");
    for (let i = 0; i < COMMAND_HISTORY_LIMIT + 5; i++) {
      state = appendLocalCommandHistory(state, i === 1004 ? "same" : `command ${i}`);
    }
    state = appendLocalCommandHistory(state, "same");
    expect(state.entries).toHaveLength(COMMAND_HISTORY_LIMIT);
    expect(state.entries[0].text).toBe("command 6");
    expect(state.entries.slice(-2).map((entry) => entry.text)).toEqual(["same", "same"]);
  });

  it("bounds an authoritative oversized snapshot to its newest 1000 valid IDs", () => {
    const entries = Array.from(
      { length: COMMAND_HISTORY_LIMIT + 4 },
      (_, index) => ({ id: index + 1, text: `command ${index + 1}` }),
    );
    entries.splice(2, 0, { id: 2, text: "duplicate-id" });
    const state = applyCommandHistoryUpdate(emptyHistoryState(), {
      epoch: 2,
      revision: COMMAND_HISTORY_LIMIT + 4,
      replace: true,
      entries,
    });
    expect(state.entries).toHaveLength(COMMAND_HISTORY_LIMIT);
    expect(state.entries[0]).toEqual({ id: 5, text: "command 5" });
    expect(state.entries.at(-1)).toEqual({
      id: COMMAND_HISTORY_LIMIT + 4,
      text: `command ${COMMAND_HISTORY_LIMIT + 4}`,
    });
  });
});

describe("command history navigation", () => {
  const entries = [
    { id: 1, text: "look" },
    { id: 2, text: "skills" },
    { id: 3, text: "cond" },
  ];

  it("preserves and restores a partial draft and cursor", () => {
    let browse = emptyBrowseState();
    let result = browsePrevious(entries, browse, "attack th", 6);
    browse = result.state;
    expect(result).toMatchObject({ text: "cond", cursor: 4 });
    result = browsePrevious(entries, browse, result.text, result.cursor);
    browse = result.state;
    expect(result.text).toBe("skills");
    result = browseNext(entries, browse, result.text, result.cursor);
    browse = result.state;
    expect(result.text).toBe("cond");
    result = browseNext(entries, browse, result.text, result.cursor);
    expect(result).toMatchObject({ text: "attack th", cursor: 6 });
    expect(result.state.selectedId).toBeNull();
  });

  it("pins the selected stable ID across a remote append", () => {
    const selected = browsePrevious(entries, emptyBrowseState(), "draft", 2);
    const withRemote = [...entries, { id: 4, text: "inventory" }];
    const older = browsePrevious(withRemote, selected.state, selected.text, selected.cursor);
    expect(older.text).toBe("skills");
    const newer = browseNext(withRemote, older.state, older.text, older.cursor);
    expect(newer.text).toBe("cond");
  });

  it("preserves recalled text as a draft when retention evicts its stable ID", () => {
    const selected = browsePrevious(entries, emptyBrowseState(), "draft", 3);
    const afterEviction = entries.slice(0, -1);
    const result = browsePrevious(
      afterEviction,
      selected.state,
      "cond edited",
      5,
    );
    expect(result.text).toBe("skills");
    expect(result.state.draft).toBe("cond edited");
    expect(result.state.draftCursor).toBe(5);
  });
});

describe("Tab command-history completion", () => {
  const entries = [
    { id: 1, text: "attack rat" },
    { id: 2, text: "look" },
    { id: 3, text: "Attack bear" },
    { id: 4, text: "attack footpad" },
  ];

  it("uses a case-insensitive prefix and cycles newest-to-oldest without submitting", () => {
    let completion = emptyCompletionState();
    let result = completeFromHistory(entries, "att", completion);
    expect(result).toMatchObject({ matched: true, text: "attack footpad", entryId: 4 });
    completion = result.state;
    result = completeFromHistory(entries, result.text, completion);
    expect(result.text).toBe("Attack bear");
    completion = result.state;
    result = completeFromHistory(entries, result.text, completion, -1);
    expect(result.text).toBe("attack footpad");
  });

  it("starts a fresh Shift+Tab cycle at the newest match, then walks newer", () => {
    let result = completeFromHistory(entries, "att", emptyCompletionState(), -1);
    expect(result.text).toBe("attack footpad");
    result = completeFromHistory(entries, result.text, result.state);
    expect(result.text).toBe("Attack bear");
    result = completeFromHistory(entries, result.text, result.state, -1);
    expect(result.text).toBe("attack footpad");
  });

  it("keeps an active completion cycle stable across a remote append", () => {
    const first = completeFromHistory(entries, "att", emptyCompletionState());
    const withRemote = [...entries, { id: 5, text: "attack newcomer" }];
    const second = completeFromHistory(withRemote, first.text, first.state);
    expect(first.text).toBe("attack footpad");
    expect(second.text).toBe("Attack bear");
    expect(second.state.matchingIds).not.toContain(5);
  });

  it("does nothing for an empty command or an unmatched prefix", () => {
    expect(completeFromHistory(entries, "", emptyCompletionState()).matched).toBe(false);
    expect(completeFromHistory(entries, "xyz", emptyCompletionState())).toMatchObject({
      matched: false,
      text: "xyz",
    });
  });
});
