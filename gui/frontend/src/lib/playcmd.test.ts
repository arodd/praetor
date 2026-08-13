import { describe, it, expect } from "vitest";
import { isAllowedDuringPlay } from "./playcmd";

describe("isAllowedDuringPlay", () => {
  it("allows exactly the four control commands", () => {
    for (const c of ["/pause", "/resume", "/stop", "/next"]) {
      expect(isAllowedDuringPlay(c)).toBe(true);
    }
  });

  it("is case-insensitive and tolerates surrounding whitespace", () => {
    expect(isAllowedDuringPlay("  /STOP  ")).toBe(true);
  });

  it("rejects everything else, including game text and other slash commands", () => {
    for (const c of ["look", "/mode aggro", "/play", "/send", "", "say hello"]) {
      expect(isAllowedDuringPlay(c)).toBe(false);
    }
  });

  it("rejects a control command with arguments", () => {
    expect(isAllowedDuringPlay("/stop now")).toBe(false);
  });
});
