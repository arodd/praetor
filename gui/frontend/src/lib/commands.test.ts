import { describe, it, expect } from "vitest";
import { COMMANDS, matchCommands } from "./commands";

const names = (r: ReturnType<typeof matchCommands>) => r.map((c) => c.name);

describe("COMMANDS", () => {
  it("has no duplicate names or aliases", () => {
    const seen = new Set<string>();
    for (const c of COMMANDS) {
      for (const n of [c.name, ...(c.aliases ?? [])]) {
        expect(seen.has(n), `${n} appears twice`).toBe(false);
        seen.add(n);
      }
    }
  });

  it("every entry starts with a slash and has a description", () => {
    for (const c of COMMANDS) {
      expect(c.name.startsWith("/")).toBe(true);
      for (const a of c.aliases ?? []) expect(a.startsWith("/")).toBe(true);
      expect(c.desc.length).toBeGreaterThan(0);
    }
  });
});

describe("matchCommands", () => {
  it("returns nothing for ordinary game text", () => {
    expect(matchCommands("look")).toEqual([]);
    expect(matchCommands("")).toEqual([]);
    expect(matchCommands("say /play is fun")).toEqual([]);
  });

  it("lists everything for a bare slash", () => {
    expect(matchCommands("/")).toHaveLength(COMMANDS.length);
  });

  it("narrows by prefix", () => {
    expect(names(matchCommands("/pl"))).toEqual(["/play"]);
    expect(names(matchCommands("/p"))).toEqual(
      expect.arrayContaining(["/play", "/pause"]),
    );
  });

  it("matches case-insensitively", () => {
    expect(names(matchCommands("/PL"))).toEqual(["/play"]);
  });

  it("matches an alias without listing it as a separate entry", () => {
    const r = matchCommands("/sm");
    expect(names(r)).toEqual(["/mode"]);
    expect(r).toHaveLength(1);
  });

  // The signature must stay visible while arguments are typed — that is the
  // whole point of the hint.
  it("keeps the exact match once a space is typed", () => {
    expect(names(matchCommands("/mode "))).toEqual(["/mode"]);
    expect(names(matchCommands("/mode aggro"))).toEqual(["/mode"]);
    expect(names(matchCommands("/sm aggro"))).toEqual(["/mode"]);
  });

  it("returns nothing for an unknown command with arguments", () => {
    expect(matchCommands("/nope ")).toEqual([]);
    expect(matchCommands("/nope arg")).toEqual([]);
  });

  it("returns nothing when the input has a newline (a pasted block is prose)", () => {
    expect(matchCommands("/play\nlook")).toEqual([]);
  });

  it("filters to the control commands during a performance", () => {
    expect(names(matchCommands("/", { playing: true })).sort()).toEqual(
      ["/next", "/pause", "/resume", "/stop"].sort(),
    );
    expect(matchCommands("/mode", { playing: true })).toEqual([]);
    expect(names(matchCommands("/pa", { playing: true }))).toEqual(["/pause"]);
  });
});
