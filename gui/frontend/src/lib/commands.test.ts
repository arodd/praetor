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

  // A command with no `args` dispatches on exact string equality, so trailing
  // text means it is no longer that command — showing its signature would
  // promise something the input will not do.
  it("rejects trailing text on a command that takes no arguments", () => {
    expect(matchCommands("/play foo")).toEqual([]);
    expect(matchCommands("/help x")).toEqual([]);
  });

  it("rejects trailing text on a niladic command during a performance", () => {
    expect(matchCommands("/stop x", { playing: true })).toEqual([]);
  });

  // Guard against over-correcting: commands that declare `args` still match
  // past the space, including through an alias.
  it("still matches commands that take arguments past the space", () => {
    expect(names(matchCommands("/mode aggro"))).toEqual(["/mode"]);
    expect(names(matchCommands("/notes list"))).toEqual(["/notes"]);
    expect(names(matchCommands("/sm aggro"))).toEqual(["/mode"]);
  });

  // Dispatch trims the input before checking it (InputLine does
  // `line.trim()`), so the matcher must see the same thing — otherwise the
  // hint hides while the command still works.
  it("tolerates leading whitespace, mirroring InputLine's trim before dispatch", () => {
    expect(names(matchCommands(" /play"))).toEqual(["/play"]);
    expect(names(matchCommands("  /pl"))).toEqual(["/play"]);
  });

  it("still returns nothing for a newline even with the leading-space trim", () => {
    expect(matchCommands("/play\nlook")).toEqual([]);
  });
});
