// Multi-line input helpers, mirroring Orchil's command input (a <textarea>).
// Kept pure so the routing rules are testable without mounting the component.

type KeyLike = Pick<KeyboardEvent, "key" | "shiftKey" | "ctrlKey" | "altKey">;

// insertsNewline reports whether a keystroke should add a line instead of
// sending. Orchil sends only on Enter with NO modifier (noModifier() at
// orchil.js:1258), so Shift/Ctrl/Alt+Enter all insert a newline.
export function insertsNewline(e: KeyLike): boolean {
  return e.key === "Enter" && (e.shiftKey || e.ctrlKey || e.altKey);
}

// caretOnFirstLine reports whether the caret sits on the first line, where
// ArrowUp should recall history rather than move within the text.
export function caretOnFirstLine(value: string, caret: number): boolean {
  return !value.slice(0, caret).includes("\n");
}

// caretOnLastLine reports whether the caret sits on the last line, where
// ArrowDown should walk history forward rather than move within the text.
export function caretOnLastLine(value: string, caret: number): boolean {
  return !value.slice(caret).includes("\n");
}
