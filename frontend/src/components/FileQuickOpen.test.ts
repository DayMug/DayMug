import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { DOMWrapper, mount, flushPromises } from "@vue/test-utils";

import FileQuickOpen from "./FileQuickOpen.vue";
import { i18n } from "@/i18n";

const mockSearchFiles = vi.fn();

vi.mock("@/composables/useFileApi", () => ({
  useFileApi: () => ({
    searchFiles: (...args: unknown[]) => mockSearchFiles(...args),
  }),
}));

function hit(path: string, line?: number, text = "") {
  return {
    path,
    name: path.split("/").pop() ?? path,
    size: 1,
    modified: "2026-01-01T00:00:00Z",
    matches: line ? [{ line, text }] : [],
  };
}

function response(results: ReturnType<typeof hit>[], truncated = false) {
  return { query: "q", mode: "name", truncated, results };
}

// The dialog renders through a portal, so its content lands on document.body
// rather than inside the wrapper — every query has to go through the document.
function el(selector: string) {
  const node = document.body.querySelector(selector);
  if (!node) throw new Error(`not found: ${selector}`);
  return new DOMWrapper(node as Element);
}

function count(selector: string) {
  return document.body.querySelectorAll(selector).length;
}

function bodyText() {
  return document.body.textContent ?? "";
}

const INPUT = '[data-testid="quick-open-input"]';
const RESULT = '[data-testid="quick-open-result"]';

// The palette debounces keystrokes; tests drive that clock directly rather
// than sleeping, so they stay fast and deterministic.
async function typeAndSettle(text: string) {
  await el(INPUT).setValue(text);
  await vi.advanceTimersByTimeAsync(200);
  await flushPromises();
}

let active: ReturnType<typeof mount> | null = null;

function mountPalette(props: Record<string, unknown> = {}) {
  const wrapper = mount(FileQuickOpen, {
    props: { userId: "u1", open: true, ...props },
    global: { plugins: [i18n] },
    attachTo: document.body,
  });
  active = wrapper;
  return wrapper;
}

beforeEach(() => {
  vi.useFakeTimers();
  mockSearchFiles.mockReset();
  mockSearchFiles.mockResolvedValue(response([]));
});

afterEach(() => {
  // Portalled content outlives the wrapper, so an un-unmounted palette would
  // leak into the next test's document queries.
  active?.unmount();
  active = null;
  document.body.innerHTML = "";
  vi.useRealTimers();
});

describe("FileQuickOpen", () => {
  it("searches by name and lists the results", async () => {
    mockSearchFiles.mockResolvedValue(response([hit("cmd/main.go"), hit("web/main.go")]));
    mountPalette();
    await flushPromises();

    await typeAndSettle("main");

    expect(mockSearchFiles).toHaveBeenCalledWith(
      "u1",
      expect.objectContaining({ query: "main", mode: "name" }),
      expect.anything(),
    );
    expect(count(RESULT)).toBe(2);
  });

  it("emits the chosen path and closes", async () => {
    mockSearchFiles.mockResolvedValue(response([hit("cmd/main.go")]));
    const wrapper = mountPalette();
    await flushPromises();
    await typeAndSettle("main");

    await el(RESULT).trigger("click");

    expect(wrapper.emitted("select")?.[0]).toEqual([{ path: "cmd/main.go", line: 0 }]);
    expect(wrapper.emitted("update:open")?.at(-1)).toEqual([false]);
  });

  // A content hit carries the matched line so the editor can land on it —
  // opening at line 1 would throw away the only thing the search found.
  it("carries the matched line for a content hit", async () => {
    mockSearchFiles.mockResolvedValue(response([hit("a.go", 42, "func Target() {}")]));
    const wrapper = mountPalette({ initialMode: "content" });
    await flushPromises();
    await typeAndSettle("target");

    await el(RESULT).trigger("click");

    expect(wrapper.emitted("select")?.[0]).toEqual([{ path: "a.go", line: 42 }]);
  });

  it("moves the selection with the arrow keys and opens with Enter", async () => {
    mockSearchFiles.mockResolvedValue(response([hit("a.go"), hit("b.go"), hit("c.go")]));
    const wrapper = mountPalette();
    await flushPromises();
    await typeAndSettle("go");

    const input = el(INPUT);
    await input.trigger("keydown", { key: "ArrowDown" });
    await input.trigger("keydown", { key: "ArrowDown" });
    await input.trigger("keydown", { key: "Enter" });

    expect(wrapper.emitted("select")?.[0]).toEqual([{ path: "c.go", line: 0 }]);
  });

  it("wraps the selection past the end of the list", async () => {
    mockSearchFiles.mockResolvedValue(response([hit("a.go"), hit("b.go")]));
    const wrapper = mountPalette();
    await flushPromises();
    await typeAndSettle("go");

    const input = el(INPUT);
    await input.trigger("keydown", { key: "ArrowUp" });
    await input.trigger("keydown", { key: "Enter" });

    expect(wrapper.emitted("select")?.[0]).toEqual([{ path: "b.go", line: 0 }]);
  });

  it("Tab toggles between name and content search", async () => {
    mountPalette();
    await flushPromises();
    await typeAndSettle("needle");
    mockSearchFiles.mockClear();

    await el(INPUT).trigger("keydown", { key: "Tab" });
    await flushPromises();

    expect(mockSearchFiles).toHaveBeenCalledWith(
      "u1",
      expect.objectContaining({ mode: "content" }),
      expect.anything(),
    );
  });

  // One keystroke in content mode would open every file in the workspace for
  // a match that cannot be meaningful.
  it("does not run a content search on a single character", async () => {
    mountPalette({ initialMode: "content" });
    await flushPromises();

    await typeAndSettle("x");

    expect(mockSearchFiles).not.toHaveBeenCalled();
  });

  it("collapses a burst of keystrokes into one request", async () => {
    mountPalette();
    await flushPromises();
    const input = el(INPUT);

    await input.setValue("m");
    await input.setValue("ma");
    await input.setValue("mai");
    await vi.advanceTimersByTimeAsync(200);
    await flushPromises();

    expect(mockSearchFiles).toHaveBeenCalledTimes(1);
    expect(mockSearchFiles).toHaveBeenCalledWith(
      "u1",
      expect.objectContaining({ query: "mai" }),
      expect.anything(),
    );
  });

  it("reports a failed search instead of showing an empty list", async () => {
    mockSearchFiles.mockRejectedValue(new Error("boom"));
    mountPalette();
    await flushPromises();

    await typeAndSettle("main");

    expect(bodyText()).toContain("Search failed");
  });

  // Aborting is how the component cancels its own stale request; surfacing it
  // as an error would flash a failure on every fast keystroke.
  it("stays quiet when a request is aborted", async () => {
    mockSearchFiles.mockRejectedValue(new DOMException("aborted", "AbortError"));
    mountPalette();
    await flushPromises();

    await typeAndSettle("main");

    expect(bodyText()).not.toContain("Search failed");
  });

  it("reopening resets the query and applies the requested mode", async () => {
    mockSearchFiles.mockResolvedValue(response([hit("a.go")]));
    const wrapper = mountPalette();
    await flushPromises();
    await typeAndSettle("main");
    expect(count(RESULT)).toBe(1);

    await wrapper.setProps({ open: false });
    await wrapper.setProps({ open: true, initialMode: "content" });
    await flushPromises();

    expect((el(INPUT).element as HTMLInputElement).value).toBe("");
    expect(count(RESULT)).toBe(0);
    expect(el('[data-testid="quick-open-mode"]').text()).toBe("Content");
  });

  it("flags a truncated result set", async () => {
    mockSearchFiles.mockResolvedValue(response([hit("a.go")], true));
    mountPalette();
    await flushPromises();

    await typeAndSettle("main");

    expect(bodyText()).toContain("More matches not shown");
  });
});
