import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ref, nextTick } from "vue";
import { mount } from "@vue/test-utils";

import { useChatDraft } from "./useChatDraft";

const { loadDraft, saveDraft } = vi.hoisted(() => ({
  loadDraft: vi.fn(() => ""),
  saveDraft: vi.fn(),
}));
vi.mock("@/lib/chatDrafts", () => ({ loadDraft, saveDraft }));

function host(convId = ref("c1"), text = ref("")) {
  const wrapper = mount({
    setup() {
      useChatDraft(convId, text);
      return () => null;
    },
  });
  return { convId, text, wrapper };
}

beforeEach(() => {
  vi.clearAllMocks();
  loadDraft.mockReturnValue("");
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useChatDraft", () => {
  it("restores the active conversation's draft on mount", () => {
    loadDraft.mockReturnValue("half typed");
    const { text } = host();
    expect(text.value).toBe("half typed");
  });

  it("debounces saves rather than writing on every keystroke", async () => {
    const { text } = host();
    text.value = "a";
    await nextTick();
    text.value = "ab";
    await nextTick();
    expect(saveDraft).not.toHaveBeenCalled();

    vi.advanceTimersByTime(300);
    expect(saveDraft).toHaveBeenCalledExactlyOnceWith("c1", "ab");
  });

  it("flushes the outgoing conversation's text before switching", async () => {
    const convId = ref("c1");
    const { text } = host(convId);
    text.value = "unsent";
    await nextTick();

    convId.value = "c2";
    await nextTick();
    expect(saveDraft).toHaveBeenCalledWith("c1", "unsent");
  });

  it("loads the incoming conversation's draft on switch", async () => {
    const convId = ref("c1");
    const { text } = host(convId);
    loadDraft.mockReturnValue("other draft");

    convId.value = "c2";
    await nextTick();
    expect(text.value).toBe("other draft");
  });

  it("never mis-saves a pending draft to the conversation switched into", async () => {
    const convId = ref("c1");
    const { text } = host(convId);
    text.value = "belongs to c1";
    await nextTick();

    convId.value = "c2";
    await nextTick();
    vi.advanceTimersByTime(300);

    expect(saveDraft).not.toHaveBeenCalledWith("c2", "belongs to c1");
  });

  it("clears the editor when there is no conversation", async () => {
    const convId = ref("c1");
    const { text } = host(convId);
    text.value = "typed";
    await nextTick();

    convId.value = "";
    await nextTick();
    expect(text.value).toBe("");
  });

  it("persists the last keystrokes when the composer unmounts", async () => {
    const { text, wrapper } = host();
    text.value = "not yet flushed";
    await nextTick();

    wrapper.unmount();
    expect(saveDraft).toHaveBeenCalledWith("c1", "not yet flushed");
  });
});
