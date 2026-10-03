import { describe, it, expect, vi } from "vitest";
import { mount } from "@vue/test-utils";

import ChatComposer from "./ChatComposer.vue";

const focus = vi.fn();

// ChatInput owns uploads, slash commands and a textarea; stub it down to the
// contract this component actually forwards.
const ChatInputStub = {
  name: "ChatInput",
  props: [
    "isThinking",
    "turnStatusKnown",
    "isConnected",
    "conversationId",
    "recallText",
    "recallAttachments",
    "provider",
  ],
  emits: ["send", "cancel", "recall-consumed", "uploaded"],
  methods: { focus },
  template: `<div data-testid="chat-input-stub" />`,
};

function mountComposer(props: Partial<InstanceType<typeof ChatComposer>["$props"]> = {}) {
  return mount(ChatComposer, {
    props: {
      prompts: [],
      hasActiveTask: false,
      isThinking: false,
      turnStatusKnown: true,
      isConnected: true,
      conversationId: "c1",
      recallText: "",
      recallAttachments: [],
      provider: "claude",
      ...props,
    },
    global: { stubs: { ChatInput: ChatInputStub } },
  });
}

function input(wrapper: ReturnType<typeof mountComposer>) {
  return wrapper.findComponent(ChatInputStub);
}

describe("ChatComposer", () => {
  it("keeps the stack measurable under a stable test id", () => {
    const stack = mountComposer().get('[data-testid="composer-stack"]');
    expect(stack.classes()).toEqual(
      expect.arrayContaining(["flex", "max-h-full", "min-h-0", "flex-col"]),
    );
  });

  it("lets a question card shrink inside the visible composer height", () => {
    const wrapper = mountComposer({
      question: {
        request_id: "ask-1",
        questions: [{ id: "q1", header: "Choice", question: "Choose", options: [] }],
      },
      answeringQuestion: false,
    });

    expect(wrapper.get('[data-testid="ask-user-question"]').classes()).toEqual(
      expect.arrayContaining(["min-h-0", "flex-1"]),
    );
  });

  it("forwards a send with its attachments", async () => {
    const wrapper = mountComposer();
    const attachments = [{ name: "a.png", path: "a.png", size: 1, mime_type: "image/png" }];
    input(wrapper).vm.$emit("send", "hi", attachments);

    expect(wrapper.emitted("send")?.[0]).toEqual(["hi", attachments]);
  });

  it("forwards a plain send with no attachments", () => {
    const wrapper = mountComposer();
    input(wrapper).vm.$emit("send", "hi", undefined);
    expect(wrapper.emitted("send")?.[0]).toEqual(["hi", undefined]);
  });

  it("forwards the explicit insert mode", () => {
    const wrapper = mountComposer();
    input(wrapper).vm.$emit("send", "guide the current task", undefined, true);
    expect(wrapper.emitted("send")?.[0]).toEqual(["guide the current task", undefined, true]);
  });

  it("forwards cancel, recall-consumed and uploaded upward", () => {
    const wrapper = mountComposer();
    input(wrapper).vm.$emit("cancel");
    input(wrapper).vm.$emit("recall-consumed");
    input(wrapper).vm.$emit("uploaded");

    expect(wrapper.emitted("cancel")).toHaveLength(1);
    expect(wrapper.emitted("recall-consumed")).toHaveLength(1);
    expect(wrapper.emitted("uploaded")).toHaveLength(1);
  });

  it("passes the queue state through to the input", () => {
    const wrapper = mountComposer({ isThinking: true, turnStatusKnown: false });
    expect(input(wrapper).props("isThinking")).toBe(true);
    expect(input(wrapper).props("turnStatusKnown")).toBe(false);
  });

  it("reports a cancelled staged prompt by id", () => {
    const wrapper = mountComposer({
      prompts: [{ content: "queued", clientKey: "k1", id: "p1" }],
    });
    wrapper.findComponent({ name: "PendingPromptsArea" }).vm.$emit("cancel", "p1");
    expect(wrapper.emitted("cancel-prompt")?.[0]).toEqual(["p1"]);
  });

  it("focuses the input when the page asks the composer to", () => {
    const wrapper = mountComposer();
    focus.mockClear();
    wrapper.vm.focus();
    expect(focus).toHaveBeenCalledOnce();
  });
});
