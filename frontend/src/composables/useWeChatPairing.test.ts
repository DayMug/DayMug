import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { effectScope, ref } from "vue";

import type { WeChatPairingStatus } from "@/composables/apiTypes";

const api = vi.hoisted(() => ({
  startWeChatPairing: vi.fn(),
  pollWeChatPairing: vi.fn(),
}));

vi.mock("@/composables/apiUsers", () => api);

import {
  WECHAT_PAIRING_RETRY_MS,
  useWeChatPairing,
  type WeChatCredentials,
} from "./useWeChatPairing";

// A poll that stays parked until the test releases it, standing in for the
// gateway's ~30s long-poll.
function parkedPoll() {
  let release!: (status: WeChatPairingStatus) => void;
  const promise = new Promise<WeChatPairingStatus>((resolve) => {
    release = resolve;
  });
  return { promise, release };
}

function setup(accept = true) {
  const scope = effectScope();
  const confirmed: WeChatCredentials[] = [];
  const agentId = ref("agent-1");
  const pairing = scope.run(() =>
    useWeChatPairing({
      agentId,
      onConfirmed: (credentials) => {
        confirmed.push(credentials);
        return accept;
      },
    }),
  )!;
  return { scope, pairing, confirmed, agentId };
}

beforeEach(() => {
  vi.useFakeTimers();
  api.startWeChatPairing.mockReset();
  api.pollWeChatPairing.mockReset();
  api.startWeChatPairing.mockResolvedValue({
    challenge: "chal-1",
    qr_image: "UE5H",
    qr_content: "https://weixin.example/qr",
  });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useWeChatPairing", () => {
  it("shows the QR and polls straight away, re-polling after each long-poll", async () => {
    api.pollWeChatPairing
      .mockResolvedValueOnce({ status: "pending" })
      .mockResolvedValueOnce({ status: "scanned" })
      .mockResolvedValue({ status: "confirmed", bot_token: "tok", base_url: "https://gw" });
    const { pairing, confirmed } = setup();

    await pairing.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(api.startWeChatPairing).toHaveBeenCalledWith("agent-1");
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(3);
    expect(api.pollWeChatPairing).toHaveBeenCalledWith("agent-1", "chal-1");
    expect(confirmed).toEqual([{ botToken: "tok", baseUrl: "https://gw" }]);
    expect(pairing.status.value).toBe("confirmed");
    expect(pairing.qrImage.value).toBe("");
    expect(pairing.scanURL.value).toBe("https://weixin.example/qr");
    expect(pairing.inFlight.value).toBe(false);
  });

  it("reports the scanned state while the phone has yet to confirm", async () => {
    const parked = parkedPoll();
    api.pollWeChatPairing
      .mockResolvedValueOnce({ status: "scanned" })
      .mockReturnValue(parked.promise);
    const { pairing } = setup();

    await pairing.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(pairing.status.value).toBe("scanned");
    expect(pairing.inFlight.value).toBe(true);
    expect(pairing.qrImage.value).toBe("UE5H");
  });

  it("paces a retry after a failed poll and clears the error once one succeeds", async () => {
    const parked = parkedPoll();
    api.pollWeChatPairing
      .mockRejectedValueOnce(new Error("network blip"))
      .mockResolvedValueOnce({ status: "pending" })
      .mockReturnValue(parked.promise);
    const { pairing } = setup();

    await pairing.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(pairing.error.value).toBe("network blip");
    expect(pairing.status.value).toBe("waiting");
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(WECHAT_PAIRING_RETRY_MS - 1);
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(1);
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(3);
    expect(pairing.error.value).toBe("");
  });

  it.each(["expired", "blocked"] as const)("stops polling once the code is %s", async (state) => {
    api.pollWeChatPairing.mockResolvedValue({ status: state });
    const { pairing } = setup();

    await pairing.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(pairing.status.value).toBe(state);
    expect(pairing.qrImage.value).toBe("");

    await vi.advanceTimersByTimeAsync(60_000);
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);
  });

  it("returns to idle with the error when the pairing cannot start", async () => {
    api.startWeChatPairing.mockRejectedValue(new Error("not allowed"));
    const { pairing } = setup();

    await pairing.start();

    expect(pairing.status.value).toBe("idle");
    expect(pairing.error.value).toBe("not allowed");
    expect(api.pollWeChatPairing).not.toHaveBeenCalled();
  });

  it("keeps waiting when the caller has nowhere to store a confirmed pair", async () => {
    const parked = parkedPoll();
    api.pollWeChatPairing
      .mockResolvedValueOnce({ status: "confirmed", bot_token: "tok", base_url: "https://gw" })
      .mockReturnValue(parked.promise);
    const { pairing, confirmed } = setup(false);

    await pairing.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(confirmed).toHaveLength(1);
    expect(pairing.status.value).toBe("waiting");
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(2);
  });

  it("ignores a poll answer for a challenge that a restart superseded", async () => {
    const first = parkedPoll();
    const second = parkedPoll();
    api.pollWeChatPairing.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    api.startWeChatPairing
      .mockResolvedValueOnce({ challenge: "chal-1", qr_image: "ONE" })
      .mockResolvedValueOnce({ challenge: "chal-2", qr_image: "TWO" });
    const { pairing, confirmed } = setup();

    await pairing.start();
    await pairing.start();
    first.release({ status: "confirmed", bot_token: "stale", base_url: "https://old" });
    await vi.advanceTimersByTimeAsync(0);

    expect(confirmed).toEqual([]);
    expect(pairing.challenge.value).toBe("chal-2");
    expect(pairing.qrImage.value).toBe("TWO");
    expect(pairing.status.value).toBe("waiting");
  });

  it("cancels a pending retry when its scope is disposed", async () => {
    api.pollWeChatPairing.mockRejectedValue(new Error("network blip"));
    const { scope, pairing } = setup();

    await pairing.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);

    scope.stop();
    await vi.advanceTimersByTimeAsync(WECHAT_PAIRING_RETRY_MS * 5);
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("does not re-issue the long-poll that was in flight when its scope was disposed", async () => {
    const parked = parkedPoll();
    api.pollWeChatPairing
      .mockReturnValueOnce(parked.promise)
      .mockResolvedValue({ status: "pending" });
    const { scope, pairing, confirmed } = setup();

    await pairing.start();
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);

    scope.stop();
    parked.release({ status: "pending" });
    await vi.advanceTimersByTimeAsync(10_000);

    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);
    expect(confirmed).toEqual([]);
  });

  it("drops a start that resolves after its scope was disposed", async () => {
    let resolveStart!: (value: { challenge: string }) => void;
    api.startWeChatPairing.mockReturnValue(
      new Promise((resolve) => {
        resolveStart = resolve;
      }),
    );
    const { scope, pairing } = setup();

    const started = pairing.start();
    scope.stop();
    resolveStart({ challenge: "chal-1" });
    await started;
    await vi.advanceTimersByTimeAsync(0);

    expect(api.pollWeChatPairing).not.toHaveBeenCalled();
  });

  it("reset clears the QR and stops the chain", async () => {
    const parked = parkedPoll();
    api.pollWeChatPairing
      .mockReturnValueOnce(parked.promise)
      .mockResolvedValue({ status: "pending" });
    const { pairing } = setup();

    await pairing.start();
    pairing.reset();
    parked.release({ status: "pending" });
    await vi.advanceTimersByTimeAsync(0);

    expect(pairing.status.value).toBe("idle");
    expect(pairing.qrImage.value).toBe("");
    expect(pairing.scanURL.value).toBe("");
    expect(api.pollWeChatPairing).toHaveBeenCalledTimes(1);
  });
});
