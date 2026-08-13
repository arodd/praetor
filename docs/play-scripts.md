# Play Script Reference

`/play` performs a script file into the game: a sequence of game commands (`emote`, `say`, `@mail`, whatever you'd normally type) interleaved with timed pauses, waits for another player's cue, and manual holds. It's built for staged scenes — a herald's announcement, a scripted duel, an RP scene with beats that need to land at the right moment — where `/send`'s "get the text there as fast as pacing allows" behavior isn't what you want.

**`/play` vs `/send`:** `/send` picks a plain text file and fires it at the game in batches — split on blank lines first, and additionally chunked every 20 lines once a block runs longer than 50 — 250ms apart, with no way to wait on anything in between. `/play` picks a **script** file — its own small format described below — that can pause for a fixed or random duration, block until specific text appears in the game, or hold until you personally say "go." Nothing in a play script goes through the Lua engine or the `send()` command queue; the performer drives it directly, one line at a time. Consecutive lines of game text still respect `commands.min_interval` as a floor between sends (the same setting the Lua command queue uses), even with no `%wait` between them — a script isn't a way to blast text faster than the game will accept it, only to pace it out deliberately.

## File Format

- One instruction or one line of game text per line.
- A line whose **first character** is `%` is an instruction (see below).
- A line whose **first character** is `#` is a comment: it is dropped entirely — never sent, never shown, not even counted as a step.
- Everything else is sent to the game verbatim — including a line starting with `/` (see below).
- The sigil check is column-1 only. Indenting a comment or instruction (`  # note` or `  %wait:1s`) defeats it — the line falls through to "ordinary game text" and is sent (leading whitespace and all). Keep `%` and `#` flush against the left margin.
- Blank lines are **sent**, not skipped. In TEC, an empty line ends an in-game writing prompt (composing `@mail`, for instance), so a script that stages a prompt needs those blank terminators to actually reach the server. A blank line in the middle of a script, or right after your last line of content, is preserved.
- An empty file, or a file that's just a single blank line, is treated as having nothing to play (no steps at all) — it's not sent as one blank line. Only the file's own final line terminator is trimmed, so a file ending `text\n` doesn't produce a spurious extra blank step — but a genuine trailing blank line (`text\n\n`) survives as its own step and is sent, exactly like a blank line anywhere else in the script.
- A line starting with `/` is not interpreted as a client command. It's ordinary game text, same as any other non-`%`, non-`#` line, and reaches the game exactly as written. `%` is the only escape — a script can never accidentally fire `/mode` or `/stop` at the client instead of the game.

## Why `%` and Not `@`

`@` already belongs to the game: `@mail`, `@request`, `@macro`, `@clientpref`, and friends are real in-game/client commands. A script that stages an in-game letter or a macro edit will naturally contain lines starting with `@` — so `@` can't *also* mean "this is a play instruction" without creating an unresolvable ambiguity. `%` was picked because the game doesn't use it for anything, so it can be reserved entirely for the script format.

The practical upshot: every line in your script that starts with `@` — `@mail Aldric`, `@request`, whatever — reaches the game exactly as written, untouched by the player. Only a line starting with `%` is intercepted as an instruction.

## Instructions

Instruction names are matched case-insensitively (`%WAIT:5s` works the same as `%wait:5s`), but everything after the name — durations, patterns, note text — is taken exactly as written.

### `%wait:<duration>`

Pause for a fixed duration before moving on to the next line.

```
say Hear me, citizens of the Eternal City!
%wait:3s
```

### `%wait-random:<min>-<max>`

Pause for a uniform-random duration between `<min>` and `<max>` (both required; `<min>` must be strictly less than `<max>`). Keeps repeated performances from feeling metronomic.

```
%wait-random:4s-7s
```

### `%wait-for:<pattern>[:<timeout>]`

Block until an incoming line of game text matches `<pattern>` (see Cue Matching, below), or until the timeout expires — whichever comes first.

The optional `:<timeout>` is found by looking at the **last** colon-separated field of the argument: if that field parses as a Go duration, it's treated as a per-step timeout override; if it doesn't, the whole argument — colons included — is just the pattern, and the configured default applies. So:

- `%wait-for:mutters:45s` — pattern is `mutters`, timeout is `45s`.
- `%wait-for:Aldric draws` — pattern is `Aldric draws`, using the configured default timeout.
- `%wait-for:He says: run!` — `run!` isn't duration-shaped, so the whole thing (`He says: run!`) is the pattern, using the default timeout. (A pattern that genuinely ends in something duration-shaped can't be expressed this way — shorten the pattern instead.)

A given timeout must be greater than zero; `%wait-for:pattern:0s` is a validation error, not a way to opt out of a limit.

```
%wait-for:mutters:45s
```

### `%wait-key`

Hold indefinitely until the performer sends `/next`. Takes no argument. Useful for starting (or resuming) a beat on your own cue instead of a clock — see the worked example below, which opens with one.

```
%wait-key
```

### `%note:<text>`

Print `<text>` locally, in the output pane, and never send anything to the game. The rest of the line after the first colon is taken verbatim, including any further colons — `%note:done — Aldric usually replies here` is the whole note text, colon and all.

A note renders in **Skotos orange italic**, the client's accent colour, so it stands apart from game text at a glance mid-scene.

A note also **holds a half-second beat** before the scene moves on, so you have a moment to read it. That beat is deliberate: a note that passed through instantly made scripts read as though their waits were being skipped.

```
%note:check that Aldric is in the room before you begin
```

## Duration Syntax

Durations use Go's duration syntax: a number followed by a unit (`ms`, `s`, `m`, `h`, and combinations like `1m30s`). `500ms`, `5s`, and `2m` are all valid. Every duration in a script — `%wait`, both bounds of `%wait-random`, and a `%wait-for` timeout — must be strictly greater than zero. `0s` and negative durations are rejected at validation time; a wait that doesn't wait isn't worth writing.

## Cue Matching (`%wait-for`)

- **Case-insensitive.** `%wait-for:MUTTERS` matches "the crowd mutters uneasily." This is a deliberate difference from Lua reaction matching (case-sensitive; see [docs/lua-api.md](lua-api.md)) — a missed Lua reaction just fires again on the next line, but a missed cue stalls the whole performance and eventually gives up, so play scripts err on the side of matching.
- **Substring by default**, same as Lua: the pattern matches if it appears anywhere in an incoming line.
- **Wildcards**: `*` matches any run of characters, `?` matches exactly one character — same syntax as Lua's `match` field.
- **Matches game text only.** Your own commands — typed or sent by the script itself — are echoed back into the output pane, but echoed lines never satisfy a `%wait-for`; a script waiting on a phrase it just sent would otherwise match itself instantly.
- **Ignorelisted text never arrives.** A cue spoken on a channel you've ignorelisted is suppressed before it reaches the output — and suppressed text never reaches `%wait-for` either. Waiting on a pattern that only ever appears in chat you've muted will time out even though it was technically "said."
- Only text that arrives **after** the step begins counts; anything already sitting in the buffer from an earlier step is discarded first, so a stale line can't retroactively satisfy a fresh wait.

## Commands

| Command | Effect |
|---------|--------|
| `/play` | Opens a file picker, parses the chosen script, and shows a preview: step count, a rough time estimate, and every validation error if there are any. |
| `/pause` | Holds the performance in place, mid-step. |
| `/resume` | Continues a held performance. |
| `/stop` | Ends the performance outright and discards its state. |
| `/next` | Releases a `%wait-key` hold, advancing to the next step. |
| Alt+X | The same panic key that aborts a `/send` and stops an automation mode. During a performance it also stops it immediately — equivalent to `/stop`, and the input goes back to accepting normal commands right away. |

In the picker's preview: a script with errors has no Save button and nothing is sent; a script with none shows step count and estimate, and Save starts it.

**`/resume` re-runs the interrupted instruction from the start, not from where it left off.** The performance only advances past a step once that step *completes* — pausing mid-step just stops the clock without remembering progress within it. A `%wait:30s` paused after 25 seconds costs its full 30 seconds again after `/resume`. A `%wait-for` that gets paused (including one that timed out — see below) re-arms its matcher and, if a timeout applies, gets a fresh full timeout window, not whatever was left.

**A `%wait-for` that times out halts the performance but keeps its place** — it is *not* the same as `/stop`. The notice names the line and the pattern it gave up on; `/resume` tries that exact same `%wait-for` again, from scratch. `/stop`, by contrast, discards the whole performance; there's no resuming it, only starting the script over with `/play`.

**While a performance is running, only `/pause`, `/resume`, `/stop`, and `/next` (typed or via Alt+X) are accepted.** This is enforced centrally, not just in the command input: numpad movement, the sidebar's compass and action-set buttons, and every other control that can send something to the game are blocked too, not only what you type. The command input shows a toast explaining why; the other paths are silently ignored instead — a held numpad key repeats while you walk, and a toast on every repeat would just spam the screen. UI-only actions (tabs, scrollback, search, opening menus) are unaffected, since none of them reach the game. `/play` itself also requires an active connection to start, and refuses to start — with a message explaining why — if a `/send` is still in flight or a Lua automation mode other than `disable` is currently running, since either would interleave with the performance; stop it first (Alt+X, or `/mode disable`).

## Validation

The whole script is parsed and checked **before anything is sent** — a script with a typo in step 40 fails before step 1 ever reaches the game. Every error is collected and reported together, tagged with its 1-based source line, so you fix the whole script in one pass instead of discovering faults one performance at a time.

| Instruction | Problem | Message |
|-------------|---------|---------|
| `%wait` | no duration given | `%wait: missing duration` |
| `%wait` | not a valid duration | `%wait: "5x" is not a duration (try 5s, 500ms, 2m)` |
| `%wait` | duration ≤ 0 | `%wait: duration -5s must be greater than zero` |
| `%wait-random` | no `-` separator | `%wait-random needs a range like 3s-7s, got "..."` |
| `%wait-random` | bad lower/upper bound | `%wait-random lower bound: ...` / `%wait-random upper bound: ...` (same duration errors as `%wait`) |
| `%wait-random` | min ≥ max | `%wait-random range 7s-3s is inverted or empty` |
| `%wait-for` | timeout ≤ 0 | `%wait-for: timeout 0s must be greater than zero` |
| `%wait-for` | empty pattern | `%wait-for needs a pattern to wait for` |
| `%wait-key` | takes an argument | `%wait-key takes no argument, got "..."` |
| any | unrecognized instruction name | `unknown instruction %foo` |

`%note` has no validation — the rest of the line is always accepted as-is.

## Configuration

```yaml
play:
  wait_for_timeout: 60s   # default ceiling for every %wait-for that doesn't specify its own
```

`play.wait_for_timeout` only applies to a `%wait-for` step that doesn't carry its own `:<timeout>` suffix. It defaults to `60s`; a non-positive value in `config.yaml` is treated as unset and falls back to the default. There's no menu setting for this yet — edit `config.yaml` directly.

## A Complete Example

```
# The Herald's Announcement — town square, dusk
# Stand near the fountain before starting.

%note:check that Aldric is in the room before you begin
%wait-key

emote unrolls a heavy vellum scroll, clearing his throat.
%wait:3s
say Hear me, citizens of the Eternal City!
%wait-random:4s-7s
say By decree of the Council, the north gate shall close at moonrise.

# Wait for the crowd to react before the next beat.
%wait-for:mutters:45s
emote raises a hand for silence.
%wait:2s

say Those beyond the wall at that hour will remain beyond it until dawn.
%wait-random:3s-6s
emote rolls the scroll closed and bows shallowly.
%note:done — Aldric usually replies here
```

Walking through it:

- The three `#` lines (the two stage-direction comments at the top, and the "wait for the crowd" note before `%wait-for`) never reach the game and aren't even part of the performance — they're pure authoring notes.
- `%note:check that Aldric is in the room before you begin` prints to your own output pane only; the game never sees it.
- `%wait-key` holds the performance immediately, before a single line is sent — you send `/next` yourself once Aldric is actually in the room and you're ready to start. This is what lets the performer begin on their own cue rather than a fixed delay.
- The `emote` and `say` lines are ordinary game text — no sigil, sent exactly as written.
- `%wait:3s` and the two `%wait-random` steps pace the beats out; the random ones vary run to run so the performance doesn't feel identical every time.
- The blank line after "shall close at moonrise." is sent to the game as an empty line, same as any other blank line in the script.
- `%wait-for:mutters:45s` blocks until some incoming line contains "mutters" (case-insensitively), for up to 45 seconds. If the crowd never mutters, the performance doesn't race ahead — it gives up after 45 seconds, halts in place, and tells you which line and pattern it was waiting on. `/resume` tries that same `%wait-for:mutters:45s` again, with a fresh 45-second window, not the remaining time from before.
- The closing `%note` is, again, local only — a reminder to the performer that Aldric usually has a line here, never sent to the game.
