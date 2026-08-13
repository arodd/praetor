import { describe, it, expect } from "vitest";
import { insertsNewline, caretOnFirstLine, caretOnLastLine } from "./multiline";

const key = (over: Partial<KeyboardEvent> = {}) =>
  ({ key: "Enter", shiftKey: false, ctrlKey: false, altKey: false, ...over }) as KeyboardEvent;

describe("insertsNewline", () => {
  it("is false for plain Enter, which sends", () => {
    expect(insertsNewline(key())).toBe(false);
  });

  // Orchil gates sending on noModifier(), so Shift/Ctrl/Alt+Enter all add a line.
  it("is true for Enter with any modifier", () => {
    expect(insertsNewline(key({ shiftKey: true }))).toBe(true);
    expect(insertsNewline(key({ ctrlKey: true }))).toBe(true);
    expect(insertsNewline(key({ altKey: true }))).toBe(true);
  });

  it("is false for other keys", () => {
    expect(insertsNewline(key({ key: "a", shiftKey: true }))).toBe(false);
  });
});

describe("caret line position", () => {
  it("detects the first line", () => {
    expect(caretOnFirstLine("one\ntwo", 2)).toBe(true);
    expect(caretOnFirstLine("one\ntwo", 5)).toBe(false);
    expect(caretOnFirstLine("single", 3)).toBe(true);
  });

  it("detects the last line", () => {
    expect(caretOnLastLine("one\ntwo", 5)).toBe(true);
    expect(caretOnLastLine("one\ntwo", 2)).toBe(false);
    expect(caretOnLastLine("single", 3)).toBe(true);
  });
});
