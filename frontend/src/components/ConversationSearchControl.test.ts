import { expect, it } from "vitest";
import { mount } from "@vue/test-utils";

import ConversationSearchControl from "./ConversationSearchControl.vue";

it("opens, focuses, updates, and closes the conversation search", async () => {
  const wrapper = mount(ConversationSearchControl, {
    attachTo: document.body,
    props: { modelValue: "" },
  });

  try {
    expect(wrapper.find('[data-testid="conversation-search-input"]').exists()).toBe(false);

    await wrapper.get('[data-testid="conversation-search-toggle"]').trigger("click");
    const input = wrapper.get('[data-testid="conversation-search-input"]');
    expect(document.activeElement).toBe(input.element);
    expect(wrapper.find('[data-testid="conversation-search-toggle"]').exists()).toBe(false);

    await input.setValue("release notes");
    expect(wrapper.emitted("update:modelValue")?.at(-1)).toEqual(["release notes"]);

    await input.trigger("keydown", { key: "Escape" });
    expect(wrapper.emitted("update:modelValue")?.at(-1)).toEqual([""]);
    expect(wrapper.find('[data-testid="conversation-search-input"]').exists()).toBe(false);
  } finally {
    wrapper.unmount();
  }
});
