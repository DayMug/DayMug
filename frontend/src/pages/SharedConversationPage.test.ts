import { describe, expect, it, vi } from "vitest";
import { mount } from "@vue/test-utils";
import { createRouter, createMemoryHistory } from "vue-router";
import SharedConversationPage from "./SharedConversationPage.vue";

const mockFetchSharedConversation = vi.fn();

vi.mock("@/composables/apiConversations", () => ({
  fetchSharedConversation: (...args: unknown[]) => mockFetchSharedConversation(...args),
}));

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      {
        path: "/share/conversation/:token",
        name: "shared-conversation",
        component: SharedConversationPage,
      },
    ],
  });
}

describe("SharedConversationPage", () => {
  it("loads and renders a shared conversation transcript", async () => {
    mockFetchSharedConversation.mockResolvedValueOnce({
      conversation: {
        id: "c1",
        user_id: "u1",
        title: "Shared chat",
        provider: "claude",
        model: "sonnet",
        work_dir: "/tmp",
        session_id: "s1",
        notifications_enabled: true,
        pinned: false,
        created_at: "2026-06-01T10:00:00Z",
        updated_at: "2026-06-01T10:01:00Z",
      },
      messages: [
        {
          id: "m1",
          conversation_id: "c1",
          role: "user",
          content: "hello",
          created_at: "2026-06-01T10:00:00Z",
        },
        {
          id: "m2",
          conversation_id: "c1",
          role: "assistant",
          content: [{ type: "text", text: "hi there" }],
          created_at: "2026-06-01T10:01:00Z",
        },
        {
          id: "m3",
          conversation_id: "c1",
          role: "tool",
          content: { name: "Read", input: { file_path: "/tmp/secret.txt" } },
          created_at: "2026-06-01T10:01:30Z",
        },
        {
          id: "m4",
          conversation_id: "c1",
          role: "tool",
          content: { name: "Bash", input: { command: "ls /tmp" } },
          created_at: "2026-06-01T10:01:31Z",
        },
      ],
    });
    const router = makeRouter();
    await router.push("/share/conversation/tok123");
    await router.isReady();

    const wrapper = mount(SharedConversationPage, { global: { plugins: [router] } });
    await vi.dynamicImportSettled();

    expect(mockFetchSharedConversation).toHaveBeenCalledWith("tok123");
    expect(wrapper.text()).toContain("Shared chat");
    expect(wrapper.text()).toContain("hello");
    expect(wrapper.text()).toContain("hi there");
    expect(wrapper.text()).not.toContain('"type":"text"');
    expect(wrapper.findAll(".tool-group-header")).toHaveLength(1);
    expect(wrapper.find(".tool-group-header").text()).toContain("Read");
    expect(wrapper.find(".tool-group-header").text()).toContain("Bash");
    expect(wrapper.find(".tool-group-header").text()).toContain("Used 2 tools");
    expect(wrapper.find(".tool-group-body").exists()).toBe(false);
    expect(wrapper.text()).not.toContain("/tmp/secret.txt");

    await wrapper.find(".tool-group-header").trigger("click");

    expect(wrapper.findAll(".tool-header").map((node) => node.text())).toEqual([
      "[Read]",
      "[Bash]",
    ]);
    expect(wrapper.text()).toContain("/tmp/secret.txt");
    expect(wrapper.text()).toContain("ls /tmp");
  });

  it("owns vertical scrolling inside the app shell", async () => {
    mockFetchSharedConversation.mockResolvedValueOnce({
      conversation: {
        id: "c1",
        user_id: "u1",
        title: "Long shared chat",
        provider: "claude",
        model: "sonnet",
        work_dir: "/tmp",
        session_id: "s1",
        notifications_enabled: true,
        pinned: false,
        created_at: "2026-06-01T10:00:00Z",
        updated_at: "2026-06-01T10:01:00Z",
      },
      messages: [],
    });
    const router = makeRouter();
    await router.push("/share/conversation/tok123");
    await router.isReady();

    const wrapper = mount(SharedConversationPage, { global: { plugins: [router] } });
    await vi.dynamicImportSettled();

    const page = wrapper.find("main");
    expect(page.classes()).toContain("h-full");
    expect(page.classes()).toContain("overflow-y-auto");
  });
});
