import { describe, it, expect } from "vitest";
import { useConfirm } from "./useConfirm";

describe("useConfirm", () => {
  it("exposes the pending request via confirmState until settled", async () => {
    const { confirm, confirmState, settle } = useConfirm();
    const pending = confirm({ message: "Delete this?", variant: "destructive" });

    expect(confirmState.value).not.toBeNull();
    expect(confirmState.value?.message).toBe("Delete this?");
    expect(confirmState.value?.variant).toBe("destructive");

    settle(false);
    await pending;
  });

  it("resolves true and clears state when settled positively", async () => {
    const { confirm, confirmState, settle } = useConfirm();
    const pending = confirm({ message: "Proceed?" });

    settle(true);

    await expect(pending).resolves.toBe(true);
    expect(confirmState.value).toBeNull();
  });

  it("resolves false when settled negatively (cancel / escape)", async () => {
    const { confirm, settle } = useConfirm();
    const pending = confirm({ message: "Proceed?" });

    settle(false);

    await expect(pending).resolves.toBe(false);
  });

  it("cancels a still-pending request when a new one is opened", async () => {
    const { confirm, settle } = useConfirm();
    const first = confirm({ message: "First" });
    const second = confirm({ message: "Second" });

    // Opening the second dialog must not leave the first caller hanging.
    await expect(first).resolves.toBe(false);

    settle(true);
    await expect(second).resolves.toBe(true);
  });
});
