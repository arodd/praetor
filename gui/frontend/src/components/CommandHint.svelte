<script lang="ts">
  import { commandHead, type CommandSpec } from "../lib/commands";
  import { store } from "../lib/store.svelte";

  let { matches }: { matches: CommandSpec[] } = $props();

  // Match the output pane's text size, which is user-configurable
  // (ui.output_font_size). A hint you read at a glance mid-scene has to be as
  // legible as the game text it sits above — a fixed size ignored the setting
  // and came out noticeably smaller.
  const fontSize = $derived(store.config?.UI?.OutputFontSize || 14);
</script>

<div class="hint" style="font-size:{fontSize}px">
  {#each matches as c (c.name)}
    <div class="row">
      <span class="name">{commandHead(c)}</span>
      {#if c.args}<span class="args">{c.args}</span>{/if}
      <span class="desc">{c.desc}</span>
    </div>
  {/each}
</div>

<style>
  /* Sits in the same slot as .rsearch, directly above the input, overlaying the
     output pane. InputLine guarantees the two are never shown together. Muted
     palette on purpose: neither the orange accent nor the play indicator's
     electric blue, both of which already carry meaning elsewhere. */
  .hint {
    position: absolute;
    bottom: 100%;
    left: 0;
    right: 0;
    max-height: 40vh;
    overflow-y: auto;
    background: var(--bg-elevated);
    border-top: 1px solid var(--border);
    font-family: var(--mono);
    /* font-size is set inline from ui.output_font_size — see the script block. */
    padding: 4px 0;
  }
  .row {
    display: flex;
    gap: 8px;
    padding: 2px 12px;
    white-space: nowrap;
  }
  .name {
    color: var(--fg);
  }
  .args {
    color: var(--fg-dim);
  }
  .desc {
    color: var(--fg-dim);
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
