<script lang="ts">
  import Modal from "../Modal.svelte";
  import { COMMANDS, commandHead, type CommandSpec } from "../../lib/commands";
  import { store } from "../../lib/store.svelte";

  // Match the output pane's text size, which is user-configurable
  // (ui.output_font_size). Help is a reference you sit and read; a fixed size
  // ignored the setting and came out smaller than the game text.
  const fontSize = $derived(store.config?.UI?.OutputFontSize || 14);

  const keys: [string, string][] = [
    ["Tab / Shift+Tab", "Next / previous tab"],
    ["Alt+1…9, Alt+0", "Jump to tab N"],
    ["Alt+S", "Toggle sidebar"],
    ["Alt+M", "Quick-cycle modes"],
    ["Esc", "Open menu"],
    ["↑ / ↓", "Command history"],
    ["Enter (empty)", "Send a blank line"],
  ];

  // "/mode (/sm) <name> [args…]" — one row per command, aliases inline.
  function label(c: CommandSpec): string {
    return c.args ? `${commandHead(c)} ${c.args}` : commandHead(c);
  }
</script>

<Modal title="Help" wide back>
  <div style="font-size:{fontSize}px">
    <div class="sect">
      <div class="h dim">Key bindings</div>
      <table>
        <tbody>
          {#each keys as [k, d] (k)}
            <tr><td class="k">{k}</td><td>{d}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    <div class="sect">
      <div class="h dim">Commands</div>
      <table>
        <tbody>
          {#each COMMANDS as c (c.name)}
            <tr><td class="k">{label(c)}</td><td>{c.desc}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </div>
</Modal>

<style>
  /* The two sections stack rather than sitting side by side. Side by side, each
     got half of 720px, and a key cell like "/notes [add|open|delete|list]
     [title]" ate ~300px of that — leaving so little for the description that
     "Show key bindings and commands" wrapped onto three lines. Full width gives
     the description column room, at the cost of a taller modal that scrolls. */
  .sect {
    margin-bottom: 20px;
  }
  .sect:last-child {
    margin-bottom: 0;
  }
  .h {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 1px;
    margin-bottom: 8px;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    /* font-size comes from the wrapper, set inline from ui.output_font_size. */
  }
  td {
    padding: 4px 6px;
    vertical-align: top;
  }
  .k {
    font-family: var(--mono);
    color: var(--accent);
    white-space: nowrap;
    padding-right: 16px;
    width: 1%; /* shrink-to-fit, so the description gets every remaining pixel */
  }
</style>
