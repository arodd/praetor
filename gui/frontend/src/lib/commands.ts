// The single catalog of slash commands, shared by the input hint and /help.
//
// It exists because those two drifted: /help carried a hand-maintained list
// that silently went four features out of date. The catalog is display-only —
// dispatch still lives in InputLine.svelte (frontend commands) and
// internal/client/client.go (core commands, shared with the TUI). A
// source-scanning test guards against an entry going missing.
//
// A command-registration pattern that folds dispatch and metadata into one
// entry is a planned pre-1.0 cleanup (todo/p2-command-registration-pattern.md);
// CommandSpec is shaped to become its metadata half.

export interface CommandSpec {
  name: string; // "/mode"
  aliases?: string[]; // ["/sm"] — matched and displayed, never a separate row
  args?: string; // "<name> [args…]" — omitted when the command takes none
  desc: string; // one line
}

// Commands accepted while a /play performance is running. Everything else is
// rejected by the input lockout, so the hint filters to these.
const DURING_PLAY = ["/pause", "/resume", "/stop", "/next"];

export const COMMANDS: CommandSpec[] = [
  { name: "/help", desc: "Show key bindings and commands" },
  { name: "/list", desc: "List available Lua modes" },
  { name: "/mode", aliases: ["/sm"], args: "<name> [args…]", desc: "Switch Lua mode" },
  { name: "/toggle", args: "<label>", desc: "Toggle a mode state value" },
  { name: "/set", args: "<label> <value>", desc: "Set a mode state value" },
  { name: "/calc", aliases: ["/rb"], desc: "Rank-bonus calculator" },
  { name: "/wiki", args: "[name]", desc: "Open a wiki bookmark, or list them" },
  { name: "/maps", args: "[name]", desc: "Open a map bookmark, or list them" },
  { name: "/kudos", args: "[name] [message]", desc: "Kudos menu, add a favourite, or queue one" },
  { name: "/notes", args: "[add|open|delete|list] [title]", desc: "Notepad" },
  { name: "/send", desc: "Pick a text file and send it to the game" },
  { name: "/play", desc: "Pick a script and perform it" },
  { name: "/pause", desc: "Hold the running performance" },
  { name: "/resume", desc: "Continue a held performance" },
  { name: "/stop", desc: "End the performance and drop its state" },
  { name: "/next", desc: "Release a %wait-key hold" },
];

// matchCommands returns the catalog entries to show for the current input.
// Returns [] whenever the hint should not appear at all, so callers need only
// check the length.
export function matchCommands(
  input: string,
  opts?: { playing?: boolean },
): CommandSpec[] {
  // A pasted multi-line block whose first line starts with "/" is prose, not a
  // command — it goes to the game verbatim, so hinting at it would mislead.
  if (input.includes("\n") || input.includes("\r")) return [];
  if (!input.startsWith("/")) return [];

  const pool = opts?.playing
    ? COMMANDS.filter((c) => DURING_PLAY.includes(c.name))
    : COMMANDS;

  const spaceAt = input.indexOf(" ");
  const token = (spaceAt === -1 ? input : input.slice(0, spaceAt)).toLowerCase();
  const namesOf = (c: CommandSpec) => [c.name, ...(c.aliases ?? [])];

  // Past the first space the command is committed: show only an exact match, so
  // the signature stays up while the arguments are typed and a typo shows
  // nothing rather than a stale list.
  if (spaceAt !== -1) {
    return pool.filter((c) => namesOf(c).some((n) => n === token));
  }
  return pool.filter((c) => namesOf(c).some((n) => n.startsWith(token)));
}
