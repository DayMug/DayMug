import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import { ref } from "vue";

import ChatTranscript from "./ChatTranscript.vue";
import { useChatRenderItems, type RenderItem } from "@/composables/useChatRenderItems";
import type { ChatMessage } from "@/stores/chatMessageStore";

function message(content: string, role: ChatMessage["role"] = "assistant"): RenderItem {
  return { kind: "message", key: content, msg: { role, content } as ChatMessage };
}

function toolGroup(key = "g0"): RenderItem {
  return {
    kind: "tool-group",
    key,
    subagent: false,
    tools: [
      {
        key,
        msg: { role: "activity", content: "Read(a.txt)", activityType: "tool" } as ChatMessage,
      },
    ],
  };
}

function chatMessage(role: ChatMessage["role"], content: string, extra: Partial<ChatMessage> = {}) {
  return { role, content, ...extra } as ChatMessage;
}

const toolMessage = (content: string) => chatMessage("activity", content, { activityType: "tool" });

const thinkingMessage = (content: string) =>
  chatMessage("activity", content, { activityType: "thinking" });

function mountTranscript(props: Partial<InstanceType<typeof ChatTranscript>["$props"]> = {}) {
  return mount(ChatTranscript, {
    props: {
      items: [],
      conversationId: "c1",
      isEmpty: false,
      isLoadingMoreHistory: false,
      isThinking: false,
      userLabel: "You",
      assistantLabel: "Ada",
      canShowClearContext: false,
      clearingContext: false,
      isConnected: true,
      ...props,
    },
  });
}

describe("ChatTranscript", () => {
  it("removes old bubbles when switching after overlapping history pages", async () => {
    const old = chatMessage("user", "old prompt", { id: "old" });
    const reply = chatMessage("assistant", "old reply", { id: "reply" });
    const messages = ref<ChatMessage[]>([old, reply]);
    const items = useChatRenderItems(messages);
    const wrapper = mountTranscript({ items: items.value });

    messages.value = [{ ...old }, ...messages.value];
    await wrapper.setProps({ items: items.value });
    messages.value = [
      chatMessage("user", "earlier", { id: "earlier" }),
      ...messages.value,
      chatMessage("assistant", "latest reply", { id: "latest" }),
    ];
    await wrapper.setProps({ items: items.value });
    messages.value = [chatMessage("activity", "new directory", { activityType: "info" })];
    await wrapper.setProps({ conversationId: "new", items: items.value });

    expect(wrapper.findAll(".user-bubble")).toHaveLength(0);
    expect(wrapper.text()).not.toContain("old prompt");
    expect(wrapper.text()).not.toContain("old reply");
    wrapper.unmount();
  });

  it("invites the user to start when the conversation is empty", () => {
    expect(mountTranscript({ isEmpty: true }).text()).toContain("Ada");
  });

  it("shows the paging hint while earlier history loads", () => {
    expect(mountTranscript({ isLoadingMoreHistory: true }).text()).toContain("Loading earlier");
  });

  it("names the agent in the thinking indicator", () => {
    const wrapper = mountTranscript({ isThinking: true });
    expect(wrapper.find(".thinking-indicator").text()).toContain("Ada");
  });

  it("explains a turn that ended with background work still running", () => {
    const wrapper = mountTranscript({ isWaitingOnBackground: true });
    expect(wrapper.get('[data-testid="background-wait-indicator"]').text()).toContain(
      "Ada is waiting for background work",
    );
  });

  it("offers to stop the background work", async () => {
    const wrapper = mountTranscript({ isWaitingOnBackground: true });
    await wrapper.get('[data-testid="stop-background-btn"]').trigger("click");
    expect(wrapper.emitted("stop-background")).toHaveLength(1);
  });

  it("prefers the thinking indicator once the background work wakes the agent", () => {
    const wrapper = mountTranscript({ isThinking: true, isWaitingOnBackground: true });
    expect(wrapper.find(".thinking-indicator").exists()).toBe(true);
    expect(wrapper.find('[data-testid="background-wait-indicator"]').exists()).toBe(false);
  });

  it("renders a tool group folded to its summary header by default", () => {
    const wrapper = mountTranscript({ items: [toolGroup()] });
    expect(wrapper.find(".tool-group-header").exists()).toBe(true);
    // Per-tool bodies stay hidden until the group is expanded.
    expect(wrapper.find(".tool-header").exists()).toBe(false);
  });

  it("expands a tool group when its header is clicked", async () => {
    const wrapper = mountTranscript({ items: [toolGroup()] });
    await wrapper.find(".tool-group-header").trigger("click");
    expect(wrapper.find(".tool-header").exists()).toBe(true);
  });

  it("hides the clear-context trigger unless the page allows it", () => {
    expect(mountTranscript().find('[data-testid="clear-context-btn"]').exists()).toBe(false);
    expect(
      mountTranscript({ canShowClearContext: true })
        .find('[data-testid="clear-context-btn"]')
        .exists(),
    ).toBe(true);
  });

  it("emits clear-context when the trigger is pressed", async () => {
    const wrapper = mountTranscript({ canShowClearContext: true });
    await wrapper.find('[data-testid="clear-context-btn"]').trigger("click");
    expect(wrapper.emitted("clear-context")).toHaveLength(1);
  });

  it("disables the clear-context trigger while offline", () => {
    const wrapper = mountTranscript({ canShowClearContext: true, isConnected: false });
    expect(wrapper.find('[data-testid="clear-context-btn"]').attributes("disabled")).toBeDefined();
  });

  it("disables the clear-context trigger while a clear is in flight", () => {
    const wrapper = mountTranscript({ canShowClearContext: true, clearingContext: true });
    expect(wrapper.find('[data-testid="clear-context-btn"]').attributes("disabled")).toBeDefined();
  });

  it("renders plain messages from the render plan", () => {
    const wrapper = mountTranscript({ items: [message("hello there")] });
    expect(wrapper.text()).toContain("hello there");
  });

  it("leaves an expanded tool group expanded when an older page is prepended", async () => {
    const messages = ref<ChatMessage[]>([toolMessage("[Read]\n  file: /recent")]);
    const items = useChatRenderItems(messages);
    const wrapper = mountTranscript({ items: items.value });
    await wrapper.find(".tool-group-header").trigger("click");

    messages.value = [
      toolMessage("[Bash]\n  cmd: ls"),
      chatMessage("assistant", "older reply"),
      ...messages.value,
    ];
    await wrapper.setProps({ items: items.value });

    const groups = wrapper.findAll(".tool-group");
    expect(groups).toHaveLength(2);
    // The older group was never touched; the one the user opened is still open.
    expect(groups[0].find(".tool-group-body").exists()).toBe(false);
    expect(groups[1].find(".tool-group-body").exists()).toBe(true);
  });

  it("leaves a group expanded when a prepended page merges into its head", async () => {
    const messages = ref<ChatMessage[]>([toolMessage("[Read]\n  file: /recent")]);
    const items = useChatRenderItems(messages);
    const wrapper = mountTranscript({ items: items.value });
    await wrapper.find(".tool-group-header").trigger("click");

    messages.value = [toolMessage("[Bash]\n  cmd: ls"), ...messages.value];
    await wrapper.setProps({ items: items.value });

    expect(wrapper.findAll(".tool-group")).toHaveLength(1);
    expect(wrapper.find(".tool-group-body").exists()).toBe(true);
  });

  it("folds a finished thinking block by default and opens it on click", async () => {
    const finished = { ...thinkingMessage("done thinking"), id: "db-1" };
    const items = useChatRenderItems(ref<ChatMessage[]>([finished]));
    const wrapper = mountTranscript({ items: items.value });

    const entry = wrapper.get(".thinking-entry");
    expect(entry.find(".thinking-text").exists()).toBe(false);
    await entry.trigger("click");
    expect(wrapper.get(".thinking-entry").find(".thinking-text").exists()).toBe(true);
  });

  it("keeps the collapsed thinking block collapsed when the render plan rebuilds", async () => {
    const messages = ref<ChatMessage[]>([
      thinkingMessage("first thought"),
      thinkingMessage("second thought"),
    ]);
    const items = useChatRenderItems(messages);
    const wrapper = mountTranscript({ items: items.value });
    await wrapper.findAll(".thinking-entry")[1].trigger("click");

    // Same message objects, fresh array: every render-plan rebuild must resolve
    // the same stable keys, or fold state migrates to the wrong row.
    messages.value = [...messages.value];
    await wrapper.setProps({ items: items.value });

    const entries = wrapper.findAll(".thinking-entry");
    expect(entries[0].find(".thinking-text").exists()).toBe(true);
    expect(entries[1].find(".thinking-text").exists()).toBe(false);
  });

  it("drops the fold state when the conversation changes", async () => {
    const messages = ref<ChatMessage[]>([toolMessage("[Read]\n  file: /a")]);
    const items = useChatRenderItems(messages);
    const wrapper = mountTranscript({ items: items.value, conversationId: "c1" });
    await wrapper.find(".tool-group-header").trigger("click");
    expect(wrapper.find(".tool-group-body").exists()).toBe(true);

    // Same render plan, different conversation: the desktop layout keeps this
    // component mounted, so nothing but the watcher can clear the state.
    await wrapper.setProps({ conversationId: "c2" });

    expect(wrapper.find(".tool-group-body").exists()).toBe(false);
  });

  it("lets settled rows skip off-screen rendering but never the newest or a live stream", () => {
    const stream: RenderItem = {
      kind: "message",
      key: "live",
      msg: chatMessage("activity", "streaming…", { activityType: "stream", subagent: true }),
    };
    const wrapper = mountTranscript({
      items: [message("first"), toolGroup(), stream, message("last")],
    });
    const deferrable = wrapper.findAll(".chat-row-deferrable");

    expect(deferrable.map((w) => w.text())).toHaveLength(2);
    expect(deferrable[0].text()).toContain("first");
    expect(deferrable[1].classes()).toContain("tool-group");
    expect(wrapper.find(".stream-text").element.closest(".chat-row-deferrable")).toBeNull();
  });
});
