import { describe, it, expect } from "vitest";
import { mount } from "@vue/test-utils";
import { createI18n } from "vue-i18n";
import ConfirmDialog from "./ConfirmDialog.vue";
import { useConfirm } from "@/composables/useConfirm";
import en from "@/i18n/locales/en";

function mountDialog() {
  const i18n = createI18n({ legacy: false, locale: "en", messages: { en } });
  return mount(ConfirmDialog, { global: { plugins: [i18n] } });
}

describe("ConfirmDialog", () => {
  it("resolves true when the action button is clicked", async () => {
    const wrapper = mountDialog();
    const { confirm } = useConfirm();
    const pending = confirm({ message: "Delete this?", variant: "destructive" });
    await wrapper.vm.$nextTick();

    const action = document.querySelector("[data-slot='alert-dialog-content'] button:last-of-type");
    expect(action).not.toBeNull();
    (action as HTMLElement).click();

    await expect(pending).resolves.toBe(true);
    wrapper.unmount();
  });
});
