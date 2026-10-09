import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import PendingPromptsArea from "./PendingPromptsArea.vue";
import type { PendingPrompt } from "@/composables/useChat";

function p(over: Partial<PendingPrompt> = {}): PendingPrompt {
  // Use `in` instead of `??` so `over.id: undefined` overrides the
  // default — the "no id yet" branch needs an explicit undefined.
  return {
    id: "id" in over ? over.id : `srv-${Math.random().toString(36).slice(2, 8)}`,
    content: over.content ?? "hello",
    clientKey: over.clientKey ?? `cli-${Math.random().toString(36).slice(2, 8)}`,
    poolPosition: over.poolPosition,
    poolAhead: over.poolAhead,
    poolRunning: over.poolRunning,
    fromIM: over.fromIM,
    attachments: over.attachments,
    sender: over.sender,
  };
}

describe("PendingPromptsArea", () => {
  it("renders nothing when the staging area is empty", () => {
    const wrapper = mount(PendingPromptsArea, { props: { prompts: [] } });
    expect(wrapper.find("[data-testid='pending-prompts-area']").exists()).toBe(false);
  });

  it("renders one card per pending prompt with its content", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: { prompts: [p({ content: "first" }), p({ content: "second" })] },
    });
    const cards = wrapper.findAll("[data-testid='pending-prompt-card']");
    expect(cards.length).toBe(2);
    expect(cards[0].text()).toContain("first");
    expect(cards[1].text()).toContain("second");
  });

  it("shows an image preview without exposing its agent-facing path", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: {
        prompts: [
          p({
            content: "check this\n\n/home/agent/work/uploads/shot.png",
            attachments: [
              {
                name: "shot.png",
                mime: "image/png",
                path: "uploads/shot.png",
                url: "/api/files/shot.png",
              },
            ],
          }),
        ],
      },
    });

    const image = wrapper.get('[data-testid="pending-prompt-image"]');
    expect(image.attributes("src")).toBe("/api/files/shot.png");
    expect(image.attributes("alt")).toBe("shot.png");
    expect(wrapper.get('[data-testid="pending-prompt-content"]').text()).toBe("check this");
    expect(wrapper.text()).not.toContain("/home/agent/work/uploads/shot.png");
  });

  it("shows only the filename for a queued non-image attachment", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: {
        prompts: [
          p({
            content: "/home/agent/work/uploads/brief.pdf",
            attachments: [
              {
                name: "brief.pdf",
                mime: "application/pdf",
                path: "uploads/brief.pdf",
                url: "/api/files/brief.pdf",
              },
            ],
          }),
        ],
      },
    });

    expect(wrapper.find('[data-testid="pending-prompt-content"]').exists()).toBe(false);
    expect(wrapper.get('[data-testid="pending-prompt-file"]').text()).toBe("brief.pdf");
    expect(wrapper.text()).not.toContain("/home/agent/work/uploads/brief.pdf");
  });

  it("caps the queue panel height and scrolls the prompt list", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: {
        prompts: [p({ content: "first" }), p({ content: "second" }), p({ content: "third" })],
      },
    });

    const area = wrapper.find("[data-testid='pending-prompts-area']");
    expect(area.classes()).toContain("max-h-[min(40vh,18rem)]");
    const list = area.find(".overflow-y-auto");
    expect(list.exists()).toBe(true);
    expect(list.classes()).toContain("min-h-0");
  });

  it("shows a neutral 'Queued' label for an unconfirmed head entry", () => {
    // Without a backend-confirmed poolPosition we deliberately do NOT
    // claim "Next up" — that lies when the prompt was just resent after
    // a recall and other waiters are still ahead of it in the pool.
    const wrapper = mount(PendingPromptsArea, {
      props: { prompts: [p({ content: "alpha" })] },
    });
    const pos = wrapper.find("[data-testid='pending-prompt-position']");
    expect(pos.text()).toBe("Queued");
  });

  it("shows one queued task once the backend confirms poolPosition === 1", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: {
        prompts: [p({ content: "alpha", poolPosition: 1, poolAhead: 4, poolRunning: 4 })],
      },
    });
    const pos = wrapper.find("[data-testid='pending-prompt-position']");
    expect(pos.text()).toBe("Account busy · 4 messages ahead · 4 running");
  });

  it("shows 'N message(s) ahead in this conversation' for non-head pending entries", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: { prompts: [p(), p(), p({ content: "third" })] },
    });
    const positions = wrapper.findAll("[data-testid='pending-prompt-position']");
    expect(positions[2].text()).toContain("2 messages ahead");
  });

  it("singular 'message ahead' when only one prompt is queued in front", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: { prompts: [p(), p({ content: "second" })] },
    });
    const positions = wrapper.findAll("[data-testid='pending-prompt-position']");
    expect(positions[1].text()).toBe("1 message ahead in this conversation");
  });

  it("surfaces account-pool wait when head has a poolPosition > 1", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: { prompts: [p({ poolPosition: 3, poolAhead: 6, poolRunning: 4 })] },
    });
    const pos = wrapper.find("[data-testid='pending-prompt-position']");
    expect(pos.text()).toContain("Account busy");
    expect(pos.text()).toContain("6 messages ahead · 4 running");
  });

  it("shows total-ahead and running counts beyond the first waiter", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: { prompts: [p({ poolPosition: 2, poolAhead: 5, poolRunning: 4 })] },
    });
    const pos = wrapper.find("[data-testid='pending-prompt-position']");
    expect(pos.text()).toContain("5 messages ahead · 4 running");
  });

  it("counts an active in-flight task as one prompt ahead at the head of staging", () => {
    // While a Claude run is actively executing in this conversation
    // the bare "Queued" label hides the running task. Folding it into
    // the count surfaces it as "1 message ahead in this conversation"
    // so the user understands what they're queued behind.
    const wrapper = mount(PendingPromptsArea, {
      props: { prompts: [p({ content: "behind run" })], hasActiveTask: true },
    });
    const pos = wrapper.find("[data-testid='pending-prompt-position']");
    expect(pos.text()).toBe("1 message ahead in this conversation");
  });

  it("shifts the ahead count for non-head entries when a task is in flight", () => {
    const wrapper = mount(PendingPromptsArea, {
      props: { prompts: [p(), p({ content: "second" })], hasActiveTask: true },
    });
    const positions = wrapper.findAll("[data-testid='pending-prompt-position']");
    expect(positions[0].text()).toBe("1 message ahead in this conversation");
    expect(positions[1].text()).toBe("2 messages ahead in this conversation");
  });

  it("keeps the pool-wait labels regardless of hasActiveTask", () => {
    // poolPosition carries the more specific cross-account pool wait
    // info; the active-task counter must not override it for the head
    // entry.
    const wrapper = mount(PendingPromptsArea, {
      props: {
        prompts: [p({ poolPosition: 1, poolAhead: 4, poolRunning: 4 })],
        hasActiveTask: true,
      },
    });
    expect(wrapper.find("[data-testid='pending-prompt-position']").text()).toContain(
      "4 messages ahead · 4 running",
    );
  });

  it("emits cancel with the persisted id when the X is clicked", async () => {
    const target = p({ id: "msg-7", clientKey: "cli-7" });
    const wrapper = mount(PendingPromptsArea, { props: { prompts: [target] } });
    await wrapper.find("[data-testid='pending-prompt-cancel']").trigger("click");
    expect(wrapper.emitted("cancel")).toEqual([["msg-7"]]);
  });

  it("falls back to clientKey when id is not yet known", async () => {
    const target = p({ id: undefined, clientKey: "cli-9" });
    const wrapper = mount(PendingPromptsArea, { props: { prompts: [target] } });
    await wrapper.find("[data-testid='pending-prompt-cancel']").trigger("click");
    expect(wrapper.emitted("cancel")).toEqual([["cli-9"]]);
  });

  // An IM prompt has nothing to recall into this composer, so the affordance
  // must say "cancel" — promising an edit that cannot happen is worse than
  // offering no tooltip at all.
  it("offers cancel, not recall, for a prompt that came from an IM bot", () => {
    const wrapper = mount(PendingPromptsArea, { props: { prompts: [p({ fromIM: true })] } });
    const button = wrapper.find("[data-testid='pending-prompt-cancel']");
    expect(button.attributes("aria-label")).toBe("Cancel this queued message");
    expect(wrapper.find("[data-testid='pending-prompts-area']").text()).toContain(
      "click \u00d7 to cancel",
    );
  });

  it("keeps the recall wording for web prompts", () => {
    const wrapper = mount(PendingPromptsArea, { props: { prompts: [p()] } });
    expect(wrapper.find("[data-testid='pending-prompt-cancel']").attributes("aria-label")).toBe(
      "Recall and edit this message",
    );
  });
});
