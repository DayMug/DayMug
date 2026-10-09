import { afterEach, describe, expect, it } from "vitest";
import { mount } from "@vue/test-utils";
import MobileWorkspaceNav from "./MobileWorkspaceNav.vue";
import {
  applyAttention,
  resetConversationAttentionStore,
} from "@/stores/conversationAttentionStore";

describe("MobileWorkspaceNav", () => {
  afterEach(resetConversationAttentionStore);

  it("highlights the active workspace view", () => {
    const wrapper = mount(MobileWorkspaceNav, { props: { active: "artifacts" } });

    expect(wrapper.get('[data-testid="mobile-artifacts-tab"]').classes()).toContain("bg-[#f3f2ff]");
  });

  it("badges sessions with the conversations that need the user back", () => {
    applyAttention(
      [
        { conversation_id: "done", agent_id: "a", title: "", state: "done", at: "" },
        { conversation_id: "failed", agent_id: "a", title: "", state: "error", at: "" },
        { conversation_id: "ask", agent_id: "a", title: "", state: "waiting", at: "" },
      ],
      {},
    );
    const wrapper = mount(MobileWorkspaceNav, { props: { active: "chat" } });

    expect(wrapper.get('[data-testid="mobile-conversations-badge"]').text()).toBe("3");
  });

  it("hides the badge when nothing needs attention", () => {
    const wrapper = mount(MobileWorkspaceNav, { props: { active: "chat" } });

    expect(wrapper.find('[data-testid="mobile-conversations-badge"]').exists()).toBe(false);
  });

  it("emits a distinct event for each destination", async () => {
    const wrapper = mount(MobileWorkspaceNav, { props: { active: "chat" } });
    await wrapper.get('[data-testid="mobile-conversations-tab"]').trigger("click");
    await wrapper.get('[data-testid="mobile-chat-tab"]').trigger("click");
    await wrapper.get('[data-testid="mobile-artifacts-tab"]').trigger("click");

    expect(wrapper.emitted("sessions")).toHaveLength(1);
    expect(wrapper.emitted("chat")).toHaveLength(1);
    expect(wrapper.emitted("artifacts")).toHaveLength(1);
  });
});
