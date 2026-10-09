import { describe, it, expect, vi, beforeEach } from "vitest";
import { ref, type Ref } from "vue";
import { flushPromises } from "@vue/test-utils";

const mockListDir = vi.fn();
vi.mock("@/composables/useFileApi", () => ({
  useFileApi: () => ({ listDir: mockListDir }),
}));

import { useWorkspaceBrowser } from "./useWorkspaceBrowser";
import type { FileEntry } from "./useFileApi";

function entry(name: string, isDir = false): FileEntry {
  return { name, is_dir: isDir, size: 0, modified: "" };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function setup(opts: { userId?: Ref<string> } = {}) {
  const userId = opts.userId ?? ref("u1");
  const browser = useWorkspaceBrowser({
    userId,
    showHiddenFiles: ref(false),
  });
  return { browser, userId };
}

beforeEach(() => {
  mockListDir.mockReset();
});

describe("useWorkspaceBrowser", () => {
  it("keeps the newest navigation when an earlier listing resolves late", async () => {
    const slow = deferred<{ entries: FileEntry[] }>();
    const fast = deferred<{ entries: FileEntry[] }>();
    mockListDir.mockReturnValueOnce(slow.promise).mockReturnValueOnce(fast.promise);
    const { browser } = setup();

    const first = browser.loadDir("A");
    const second = browser.loadDir("B");
    fast.resolve({ entries: [entry("b.txt")] });
    await second;
    slow.resolve({ entries: [entry("a.txt")] });
    await first;

    expect(browser.currentPath.value).toBe("B");
    expect(browser.entries.value.map((e) => e.name)).toEqual(["b.txt"]);
  });

  it("drops a listing that resolves after the panel switched agents", async () => {
    const pending = deferred<{ entries: FileEntry[] }>();
    mockListDir.mockReturnValueOnce(pending.promise);
    const userId = ref("u1");
    const { browser } = setup({ userId });

    const load = browser.loadDir("src");
    userId.value = "u2";
    pending.resolve({ entries: [entry("agentA.txt")] });
    await load;

    expect(browser.entries.value).toEqual([]);
    expect(browser.currentPath.value).toBe(".");
  });

  it("surfaces the failure when a missing subdirectory falls back to the root", async () => {
    mockListDir
      .mockRejectedValueOnce(new Error("list dir: 404"))
      .mockResolvedValueOnce({ entries: [entry("README.md")] });
    const { browser } = setup();

    await browser.loadDir("gone");
    await flushPromises();

    expect(browser.currentPath.value).toBe(".");
    expect(browser.error.value).toBe("list dir: 404");
  });
});
