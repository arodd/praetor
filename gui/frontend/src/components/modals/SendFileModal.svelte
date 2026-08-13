<script lang="ts">
  import Modal from "../Modal.svelte";
  import { store } from "../../lib/store.svelte";
  import * as api from "../../lib/bridge";

  const p = $derived(store.sendPreview);

  async function send() {
    const path = p?.path;
    store.openModal = null;
    store.sendPreview = null;
    if (!path) return;
    try {
      await api.startFileSend(path);
    } catch (e) {
      store.addToast("Send failed", String(e));
    }
  }
</script>

<Modal title="Send File" onsave={send}>
  {#if p}
    <p>
      <strong>{p.name}</strong> — {p.lines} line{p.lines === 1 ? "" : "s"},
      sent as {p.batches} batch{p.batches === 1 ? "" : "es"}.
    </p>
    <p class="hint dim">Press Alt+X at any time to abort the send.</p>
  {:else}
    <p class="hint dim">No file selected.</p>
  {/if}
</Modal>
