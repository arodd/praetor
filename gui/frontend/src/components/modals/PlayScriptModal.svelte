<script lang="ts">
  import Modal from "../Modal.svelte";
  import { store } from "../../lib/store.svelte";
  import * as api from "../../lib/bridge";

  const p = $derived(store.playPreview);
  const errs = $derived(p?.errors ?? []);
  const ok = $derived(!!p && errs.length === 0);

  // The estimate covers fixed waits only; %wait-for and %wait-key are unbounded,
  // so saying "about 2m" without that caveat would be a lie.
  function estimate(ms: number, hasCues: boolean): string {
    const s = Math.round(ms / 1000);
    const body = s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
    return hasCues ? `~${body} plus waits for cues` : `~${body}`;
  }

  async function play() {
    const path = p?.path;
    store.openModal = null;
    store.playPreview = null;
    if (!path) return;
    try {
      await api.startPlay(path);
      store.playActive = true;
    } catch (e) {
      store.addToast("Play failed", String(e));
    }
  }
</script>

<Modal title="Play Script" onsave={ok ? play : undefined}>
  {#if !p}
    <p class="hint dim">No script selected.</p>
  {:else if errs.length}
    <p><strong>{p.name}</strong> cannot be played — {errs.length} error{errs.length === 1 ? "" : "s"}:</p>
    <ul>
      {#each errs as e}
        <li>line {e.line}: {e.message}</li>
      {/each}
    </ul>
  {:else}
    <p>
      <strong>{p.name}</strong> — {p.steps} step{p.steps === 1 ? "" : "s"},
      {estimate(p.fixedMs, p.hasCues)}.
    </p>
    <p class="hint dim">
      Only /pause, /resume, /stop and /next are accepted while playing. Alt+X stops.
    </p>
  {/if}
</Modal>
