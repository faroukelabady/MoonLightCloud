<script lang="ts">
  import { onDestroy, tick } from "svelte";
  import Modal from "./ui/Modal.svelte";
  import { msg } from "../lib/i18n.svelte";
  import { ApiError, dashboardApi } from "../lib/api";
  let {
    kind,
    storeID = "",
    storeName = "",
    deviceID = "",
    deviceName = "",
    onclose,
    onchanged,
  }: {
    kind: "store" | "device" | "rotate" | "revoke";
    storeID?: string;
    storeName?: string;
    deviceID?: string;
    deviceName?: string;
    onclose: () => void;
    onchanged: () => void;
  } = $props();
  let name = $state(""),
    retailID = $state(""),
    timezone = $state("Africa/Cairo"),
    code = $state(""),
    busy = $state(false),
    error = $state(""),
    secret = $state(""),
    copied = $state(false),
    disposed = false;
  let otpInput: HTMLInputElement | undefined = $state();
  let secretInput: HTMLTextAreaElement | undefined = $state();
  let title = $derived(msg(`deviceManagement.${kind}`));
  function close() {
    if (!busy) {
      secret = "";
      code = "";
      onclose();
    }
  }
  onDestroy(() => {
    disposed = true;
    secret = "";
    code = "";
  });
  function failure(e: unknown) {
    const detail = e instanceof ApiError ? e.detail : "";
    const reason = [
      "MFA_REQUIRED",
      "MFA_INVALID",
      "STORE_ALREADY_EXISTS",
      "STORE_NOT_ACTIVE",
      "DEVICE_NOT_ACTIVE",
    ].includes(detail)
      ? detail
      : e instanceof ApiError && e.status === 429
        ? "RATE_LIMITED"
        : "failed";
    return msg(`deviceManagement.errors.${reason}`);
  }
  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (busy || !/^\d{6}$/.test(code)) return;
    busy = true;
    error = "";
    try {
      if (kind === "store") {
        await dashboardApi.createStore({
          store_id: retailID.trim(),
          display_name: name.trim(),
          timezone: timezone.trim(),
          mfa_code: code,
        });
      } else if (kind === "device") {
        const result = await dashboardApi.createDevice({
          store_id: storeID,
          name: name.trim(),
          mfa_code: code,
        });
        if (!disposed) secret = result.credential;
      } else if (kind === "rotate") {
        const result = await dashboardApi.rotateDevice(deviceID, code);
        if (!disposed) secret = result.credential;
      } else {
        await dashboardApi.revokeDevice(deviceID, code);
      }
      if (disposed) return;
      onchanged();
      if (kind === "store" || kind === "revoke") onclose();
      else {
        await tick();
        if (!disposed) secretInput?.focus();
      }
    } catch (e) {
      if (!disposed) error = failure(e);
    } finally {
      code = "";
      if (!disposed) {
        busy = false;
        if (error) {
          await tick();
          otpInput?.focus();
        }
      }
    }
  }
  async function copy() {
    try {
      await navigator.clipboard.writeText(secret);
      if (!disposed) copied = true;
    } catch {
      if (!disposed) error = msg("deviceManagement.copyFailed");
    }
  }
</script>

<Modal
  title={secret ? msg("deviceManagement.credentialsTitle") : title}
  onclose={close}
  closeLabel={msg("deviceManagement.close")}
  testid="device-management-dialog"
>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if secret}
    <p class="once" role="status">{msg("deviceManagement.once")}</p>
    <label
      >{msg("deviceManagement.credential")}<textarea
        bind:this={secretInput}
        readonly
        value={secret}
        dir="ltr"
        spellcheck="false"
        rows="4"
      ></textarea></label
    >
    <p class="hint">{msg("deviceManagement.pasteInRetail")}</p>
    <div class="actions">
      <button type="button" class="btn-primary" onclick={copy}
        >{copied
          ? msg("deviceManagement.copied")
          : msg("deviceManagement.copy")}</button
      ><button type="button" class="btn-secondary" onclick={close}
        >{msg("deviceManagement.done")}</button
      >
    </div>
  {:else}
    <form onsubmit={submit}>
      <fieldset disabled={busy}>
        {#if kind === "store"}
          <label
            >{msg("deviceManagement.storeName")}<input
              bind:value={name}
              required
              maxlength="200"
            /></label
          >
          <label
            >{msg("deviceManagement.retailID")}<input
              bind:value={retailID}
              dir="ltr"
              required
              pattern={"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"}
            /></label
          >
          <p class="hint">{msg("deviceManagement.retailIDHint")}</p>
          <label
            >{msg("deviceManagement.timezone")}<input
              bind:value={timezone}
              dir="ltr"
              required
              maxlength="64"
            /></label
          >
        {:else if kind === "device"}
          <p>
            {msg("deviceManagement.selectedStore")}:
            <strong>{storeName || storeID}</strong>
          </p>
          <label
            >{msg("deviceManagement.deviceName")}<input
              bind:value={name}
              required
              maxlength="200"
            /></label
          >
        {:else}
          <p><strong>{deviceName || deviceID}</strong></p>
          <p class:danger={kind === "revoke"}>
            {msg(
              kind === "revoke"
                ? "deviceManagement.revokeHint"
                : "deviceManagement.rotateHint",
            )}
          </p>
        {/if}
        <label
          >{msg("deviceManagement.otp")}<input
            bind:this={otpInput}
            type="password"
            bind:value={code}
            inputmode="numeric"
            autocomplete="one-time-code"
            pattern={"[0-9]{6}"}
            maxlength="6"
            required
            dir="ltr"
          /></label
        >
        <p class="hint">{msg("deviceManagement.otpHint")}</p>
      </fieldset>
      <div class="actions">
        <button
          type="button"
          class="btn-secondary"
          disabled={busy}
          onclick={close}>{msg("deviceManagement.cancel")}</button
        ><button
          type="submit"
          class="btn-primary"
          class:danger-button={kind === "revoke"}
          disabled={busy || !/^\d{6}$/.test(code)}
          >{busy ? msg("deviceManagement.working") : title}</button
        >
      </div>
    </form>
  {/if}
</Modal>

<style>
  form,
  fieldset {
    display: grid;
    gap: 14px;
  }
  fieldset {
    border: 0;
    padding: 0;
    margin: 0;
  }
  label {
    display: grid;
    gap: 6px;
    font-weight: 600;
    font-size: 0.88rem;
  }
  p {
    margin: 0;
    line-height: 1.6;
  }
  .hint {
    color: var(--text-muted);
    font-size: 0.82rem;
  }
  .actions {
    display: flex;
    justify-content: end;
    gap: 10px;
    margin-top: 18px;
  }
  textarea {
    font-family: ui-monospace, monospace;
    width: 100%;
    box-sizing: border-box;
    resize: vertical;
  }
  .once {
    padding: 12px;
    background: var(--surface-muted);
    border-radius: 8px;
    margin-bottom: 16px;
  }
  .danger,
  .error {
    color: #b91c1c;
  }
  .error {
    margin-bottom: 12px;
  }
  .danger-button {
    background: #b91c1c;
    border-color: #b91c1c;
  }
</style>
