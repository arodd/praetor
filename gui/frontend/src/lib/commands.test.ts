import { describe, it, expect } from "vitest";
import { COMMANDS, matchCommands } from "./commands";
import { isAllowedDuringPlay } from "./playcmd";

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

  // Same trim, trailing side: a space after a niladic command still dispatches
  // (InputLine's `line.trim()` strips it before comparing), so the hint must
  // not disappear the instant the user presses space.
  it("tolerates trailing whitespace on a niladic command, mirroring InputLine's trim", () => {
    expect(names(matchCommands("/play "))).toEqual(["/play"]);
    expect(names(matchCommands("/help "))).toEqual(["/help"]);
    expect(names(matchCommands(" /play "))).toEqual(["/play"]);
  });

  // Guard against over-correcting the trailing-whitespace fix: real trailing
  // text (not just whitespace) on a niladic command is still rejected, and a
  // command that takes arguments still matches past its trailing space.
  it("still rejects real trailing text on a niladic command after the trim fix", () => {
    expect(matchCommands("/play foo")).toEqual([]);
  });
  it("still matches a command with arguments through trailing whitespace", () => {
    expect(names(matchCommands("/mode aggro "))).toEqual(["/mode"]);
  });

  it("still returns nothing for a newline even with the leading-space trim", () => {
    expect(matchCommands("/play\nlook")).toEqual([]);
  });

  // playcmd.ALLOWED and this catalog's DURING_PLAY both enumerate the same
  // four control commands independently — nothing else pins them together, so
  // this proves they still agree in both directions: everything the hint
  // shows during a performance is accepted by the input lockout, and nothing
  // else in the catalog is secretly accepted that the hint doesn't show.
  it("agrees with playcmd's isAllowedDuringPlay on the play-control commands", () => {
    const shown = matchCommands("/", { playing: true });
    for (const c of shown) {
      expect(
        isAllowedDuringPlay(c.name),
        `${c.name} is shown during a performance but rejected by isAllowedDuringPlay`,
      ).toBe(true);
    }

    const shownNames = shown.map((c) => c.name).sort();
    const alsoAllowed = COMMANDS.filter((c) => isAllowedDuringPlay(c.name))
      .map((c) => c.name)
      .sort();
    expect(alsoAllowed, "isAllowedDuringPlay accepts a catalog command the hint doesn't show during a performance").toEqual(
      shownNames,
    );
  });
});
