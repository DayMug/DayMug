import { describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import type { Conversation } from "@/composables/apiTypes";
import ModelSelector from "@/components/ModelSelector.vue";
import ContextUsageBar from "./ContextUsageBar.vue";
import RateLimitBadge from "./RateLimitBadge.vue";
import ChatFooterControls from "./ChatFooterControls.vue";

const conversation: Conversation = {
  id: "c1",
  user_id: "u1",
  title: "Chat",
  provider: "codex",
  model: "gpt-5.6-sol",
  work_dir: "/tmp",
  session_id: "s1",
  notifications_enabled: false,
  pinned: false,
  pin_order: 0,
  account_name: "",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function mountControls(overrides: Partial<InstanceType<typeof ChatFooterControls>["$props"]> = {}) {
  return mount(ChatFooterControls, {
    props: {
      conversation,
      conversationStarted: true,
      showContextUsage: true,
      contextUsage: { used: 100, total: 200 },
      provider: "codex",
      showRateLimit: true,
      rateLimits: {
        five_hour: { type: "five_hour", status: "allowed", resets_at: 2_000_000_000 },
      },
      ...overrides,
    },
  });
}

describe("ChatFooterControls", () => {
  it("groups the model, context, and rate-limit controls in the composer footer", () => {
    const wrapper = mountControls();

    expect(wrapper.get('[data-testid="chat-footer-controls"]')).toBeTruthy();
    expect(wrapper.findComponent(ModelSelector).props("conversation")).toEqual(conversation);
    expect(wrapper.findComponent(ContextUsageBar).props()).toMatchObject({
      used: 100,
      total: 200,
      priceBreakpoint: 272000,
    });
    expect(wrapper.findComponent(RateLimitBadge).exists()).toBe(true);
  });

  // It shares the composer's unwrapped control line with the send button.
  // Staying `contents` lets the selector itself participate in flex sizing; as
  // a box of its own it once collapsed to zero width beside the send cluster.
  it("hands its children to the composer row instead of boxing them", () => {
    const classes = mountControls().get('[data-testid="chat-footer-controls"]').classes();

    expect(classes).toContain("contents");
    expect(classes).not.toContain("overflow-hidden");
  });

  it("stays absent until there is a conversation or status to show", () => {
    const wrapper = mountControls({
      conversation: null,
      showContextUsage: false,
      contextUsage: null,
      showRateLimit: false,
      rateLimits: {},
    });

    expect(wrapper.find('[data-testid="chat-footer-controls"]').exists()).toBe(false);
  });
});
