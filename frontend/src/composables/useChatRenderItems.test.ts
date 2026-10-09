import { describe, it, expect } from "vitest";
import { ref } from "vue";

import { useChatRenderItems } from "./useChatRenderItems";
import type { ChatMessage } from "@/stores/chatMessageStore";

function msg(role: ChatMessage["role"], content: string, extra: Partial<ChatMessage> = {}) {
  return { role, content, ...extra } as ChatMessage;
}

const tool = (content: string, extra: Partial<ChatMessage> = {}) =>
  msg("activity", content, { activityType: "tool", ...extra });

function render(messages: ChatMessage[]) {
  return useChatRenderItems(ref(messages)).value;
}

describe("useChatRenderItems", () => {
  it("passes non-tool entries through as individual messages", () => {
    const items = render([msg("user", "hi"), msg("assistant", "hello")]);
    expect(items.map((i) => i.kind)).toEqual(["message", "message"]);
  });

  it("folds a run of consecutive tool calls into one group", () => {
    const items = render([msg("user", "hi"), tool("a"), tool("b"), msg("assistant", "done")]);

    expect(items.map((i) => i.kind)).toEqual(["message", "tool-group", "message"]);
    const group = items[1];
    expect(group.kind === "tool-group" && group.tools).toHaveLength(2);
  });

  it("anchors a group on the key of its first tool", () => {
    const items = render([msg("user", "hi"), tool("a"), tool("b")]);
    const group = items[1];
    expect(group.kind === "tool-group" && group.key).toBe(
      group.kind === "tool-group" ? group.tools[0].key : "",
    );
  });

  it("keeps sub-agent tool runs in a group of their own", () => {
    const items = render([tool("parent"), tool("worker", { subagent: true })]);

    expect(items).toHaveLength(2);
    expect(items.every((i) => i.kind === "tool-group")).toBe(true);
    expect(items[0].kind === "tool-group" && items[0].subagent).toBe(false);
    expect(items[1].kind === "tool-group" && items[1].subagent).toBe(true);
  });

  it("splits one run into two groups when a non-tool row interrupts it", () => {
    const items = render([tool("a"), msg("assistant", "note"), tool("b")]);
    expect(items.map((i) => i.kind)).toEqual(["tool-group", "message", "tool-group"]);
  });

  it("keeps every activity row — tool, thinking and stream alike", () => {
    const items = render([
      msg("user", "hi"),
      tool("a"),
      msg("activity", "thought", { activityType: "thinking" }),
      msg("activity", "partial…", { activityType: "stream" }),
    ]);
    expect(items.map((i) => i.kind)).toEqual(["message", "tool-group", "message", "message"]);
  });

  it("gives every message its own key", () => {
    const items = render([msg("user", "hi"), msg("assistant", "hello")]);
    expect(new Set(items.map((i) => i.key)).size).toBe(2);
  });

  it("gives distinct objects with the same persisted id different keys", () => {
    const items = render([
      msg("user", "overlapping prompt", { id: "same-id" }),
      msg("user", "overlapping prompt", { id: "same-id" }),
    ]);
    expect(new Set(items.map((i) => i.key)).size).toBe(2);
  });

  it("keeps a message's key when an older page is prepended in front of it", () => {
    const recent = msg("assistant", "recent");
    const messages = ref<ChatMessage[]>([recent]);
    const items = useChatRenderItems(messages);
    const before = items.value[0].key;

    messages.value = [msg("user", "older"), ...messages.value];

    expect(items.value[1].key).toBe(before);
  });

  it("keeps a message's key when the plan is rebuilt from the same objects", () => {
    const messages = ref<ChatMessage[]>([msg("user", "hi")]);
    const items = useChatRenderItems(messages);
    const before = items.value[0].key;

    messages.value = [...messages.value];

    expect(items.value[0].key).toBe(before);
  });

  it("keeps a thinking row's key when it is later stamped with its persisted id", () => {
    const live = msg("activity", "thought", { activityType: "thinking" });
    const messages = ref<ChatMessage[]>([live]);
    const items = useChatRenderItems(messages);
    const before = items.value[0].key;

    // claimTrailingThinking stamps the row in place once the result lands.
    messages.value[0].id = "42";

    expect(items.value[0].key).toBe(before);
  });

  it("returns nothing for an empty transcript", () => {
    expect(render([])).toEqual([]);
  });
});
