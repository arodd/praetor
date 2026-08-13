// Input routing while a performance is running. Everything except these four
// commands is rejected with a notice: a script mid-scene must not be interleaved
// with typed input. Alt+X is handled separately, in GameView's key handler.
const ALLOWED = new Set(["/pause", "/resume", "/stop", "/next"]);

export function isAllowedDuringPlay(input: string): boolean {
  return ALLOWED.has(input.trim().toLowerCase());
}
