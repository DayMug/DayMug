import { computed, onScopeDispose, ref, toValue, type MaybeRefOrGetter } from "vue";
import { pollWeChatPairing, startWeChatPairing } from "@/composables/apiUsers";
import { errorMessage } from "@/lib/errorMessage";

// Both halves of a WeChat credential come from a QR handshake: the token, and
// the per-account gateway it must be sent to. This composable drives that
// handshake; the caller decides where the resulting pair is stored.

export type WeChatPairingState =
  | "idle"
  | "waiting"
  | "scanned"
  | "confirmed"
  | "expired"
  | "blocked";

export interface WeChatCredentials {
  botToken: string;
  baseUrl: string;
}

export interface WeChatPairingOptions {
  agentId: MaybeRefOrGetter<string>;
  // Receives the confirmed pair. Returning false means there is nowhere to put
  // it right now, and the pairing keeps waiting instead of settling.
  onConfirmed: (credentials: WeChatCredentials) => boolean;
}

// Only paces *retries after a failure*. A successful poll already blocked for
// the gateway's long-poll window, so it is re-issued immediately.
export const WECHAT_PAIRING_RETRY_MS = 2000;

export function useWeChatPairing(options: WeChatPairingOptions) {
  const challenge = ref("");
  const qrImage = ref("");
  const scanURL = ref("");
  const status = ref<WeChatPairingState>("idle");
  const error = ref("");
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  // Set when the owning scope goes away. The poll chain re-issues itself after
  // every long-poll, so clearing the retry timer alone would leave a request
  // in flight that keeps the chain alive after the editor is gone.
  let disposed = false;

  // A poll in flight counts as still pairing; the status endpoint long-polls
  // for ~30s, so "waiting" and "scanned" are both live states.
  const inFlight = computed(() => status.value === "waiting" || status.value === "scanned");

  function stop() {
    if (retryTimer !== null) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
  }

  function reset() {
    stop();
    challenge.value = "";
    qrImage.value = "";
    scanURL.value = "";
    status.value = "idle";
    error.value = "";
  }

  async function start() {
    reset();
    status.value = "waiting";
    try {
      const started = await startWeChatPairing(toValue(options.agentId));
      if (disposed) return;
      challenge.value = started.challenge;
      qrImage.value = started.qr_image ?? "";
      scanURL.value = started.qr_content ?? "";
      // No initial delay: the request itself parks until the status changes.
      void pollOnce();
    } catch (err) {
      if (disposed) return;
      status.value = "idle";
      error.value = errorMessage(err);
    }
  }

  function scheduleRetry() {
    stop();
    retryTimer = setTimeout(() => {
      retryTimer = null;
      void pollOnce();
    }, WECHAT_PAIRING_RETRY_MS);
  }

  async function pollOnce() {
    const current = challenge.value;
    if (disposed || !current || !inFlight.value) return;
    try {
      const result = await pollWeChatPairing(toValue(options.agentId), current);
      if (disposed || challenge.value !== current) return; // unmounted, or superseded by a restart
      if (
        result.status === "confirmed" &&
        options.onConfirmed({ botToken: result.bot_token ?? "", baseUrl: result.base_url ?? "" })
      ) {
        status.value = "confirmed";
        qrImage.value = "";
        stop();
        return;
      }
      if (result.status === "expired" || result.status === "blocked") {
        status.value = result.status;
        qrImage.value = "";
        stop();
        return;
      }
      error.value = "";
      status.value = result.status === "scanned" ? "scanned" : "waiting";
      // The call above already waited out the gateway's window.
      void pollOnce();
    } catch (err) {
      if (disposed) return;
      // A single failed poll is usually a blip; surface it but keep waiting so
      // a flaky network does not force the user to rescan.
      error.value = errorMessage(err);
      scheduleRetry();
    }
  }

  onScopeDispose(() => {
    disposed = true;
    stop();
  });

  return { challenge, qrImage, scanURL, status, error, inFlight, start, reset };
}
