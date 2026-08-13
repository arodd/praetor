import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { COMMANDS } from "./commands";

// Slash commands are dispatched in two places and documented in a third. Those
// drifted once already — /help sat four features out of date without anyone
// noticing. Scanning the dispatch sites is unconventional for a frontend test,
// and it is the only thing that makes the catalog self-maintaining: adding a
// command without a catalog entry fails here, naming the command.

const read = (rel: string) => readFileSync(new URL(rel, import.meta.url), "utf8");

function known(): Set<string> {
  const s = new Set<string>();
  for (const c of COMMANDS) {
    s.add(c.name);
    for (const a of c.aliases ?? []) s.add(a);
  }
  return s;
}

function matchAll(src: string, re: RegExp): string[] {
  return [...src.matchAll(re)].map((m) => m[1].toLowerCase());
}

describe("catalog covers every dispatched command", () => {
  it("covers the frontend commands in InputLine.svelte", () => {
    const src = read("../components/InputLine.svelte");
    // if (lower === "/play") { …   and   lower.startsWith("/notes ")
    const found = new Set([
      ...matchAll(src, /lower === "(\/[a-z]+)"/g),
      ...matchAll(src, /lower\.startsWith\("(\/[a-z]+)[ "]/g),
    ]);
    expect(found.size, "the scan found no commands — the regex has rotted").toBeGreaterThan(5);

    const k = known();
    const missing = [...found].filter((c) => !k.has(c));
    expect(missing, `dispatched in InputLine.svelte but absent from COMMANDS: ${missing.join(", ")}`).toEqual([]);
  });

  it("covers the core commands in handleLocalCommand", () => {
    const src = read("../../../../internal/client/client.go");
    // Scan only the dispatch function, so an unrelated "/foo" string elsewhere
    // in the file cannot be mistaken for a command.
    const start = src.indexOf("func (c *Client) handleLocalCommand");
    expect(start, "handleLocalCommand not found — client.go was restructured").toBeGreaterThan(-1);
    // Bound the slice at the next top-level func, or the scan would run to EOF
    // and pick up slash-looking strings from unrelated helpers below.
    const rest = src.slice(start);
    const end = rest.indexOf("\nfunc ", 1);
    const body = end === -1 ? rest : rest.slice(0, end);

    // Cases look like:  case "/mode", "/sm":
    const found = new Set(matchAll(body, /"(\/[a-z]+)"/g));
    expect(found.size, "the scan found no commands — the regex has rotted").toBeGreaterThan(3);

    const k = known();
    const missing = [...found].filter((c) => !k.has(c));
    expect(missing, `dispatched in client.go but absent from COMMANDS: ${missing.join(", ")}`).toEqual([]);
  });
});
