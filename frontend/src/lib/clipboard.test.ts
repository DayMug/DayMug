import { afterEach, describe, expect, it, vi } from "vitest";

import { copyText } from "./clipboard";

// navigator.clipboard is read-only in happy-dom; defineProperty bypasses
// the getter so tests can stub (or remove) the API surface per case.
function stubClipboard(value: unknown) {
  Object.defineProperty(navigator, "clipboard", {
    value,
    configurable: true,
  });
}

describe("copyText", () => {
  afterEach(() => {
    stubClipboard(undefined);
    vi.restoreAllMocks();
  });

  it("uses the async Clipboard API when available and reports success", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    stubClipboard({ writeText });

    await expect(copyText("hello")).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith("hello");
  });

  it("falls back to a hidden textarea + execCommand when the API is missing", async () => {
    stubClipboard(undefined);
    const execCommand = vi.fn().mockReturnValue(true);
    document.execCommand = execCommand as typeof document.execCommand;

    await expect(copyText("fallback text")).resolves.toBe(true);
    expect(execCommand).toHaveBeenCalledWith("copy");
    // The helper must clean up after itself so repeated copies don't
    // accumulate invisible textareas in the DOM.
    expect(document.querySelector("textarea")).toBeNull();
  });

  it("falls back to execCommand when the Clipboard API write rejects", async () => {
    stubClipboard({ writeText: vi.fn().mockRejectedValue(new Error("denied")) });
    const execCommand = vi.fn().mockReturnValue(true);
    document.execCommand = execCommand as typeof document.execCommand;

    await expect(copyText("retry")).resolves.toBe(true);
    expect(execCommand).toHaveBeenCalledWith("copy");
  });

  it("resolves false (never throws) when both paths fail", async () => {
    stubClipboard(undefined);
    document.execCommand = vi.fn(() => {
      throw new Error("unsupported");
    }) as typeof document.execCommand;

    await expect(copyText("doomed")).resolves.toBe(false);
    expect(document.querySelector("textarea")).toBeNull();
  });
});
