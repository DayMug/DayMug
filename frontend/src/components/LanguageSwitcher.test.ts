import { describe, it, expect, beforeEach } from "vitest";
import { mount } from "@vue/test-utils";
import { i18n, LOCALE_STORAGE_KEY } from "@/i18n";
import LanguageSwitcher from "./LanguageSwitcher.vue";

describe("LanguageSwitcher", () => {
  beforeEach(() => {
    window.localStorage.removeItem(LOCALE_STORAGE_KEY);
    i18n.global.locale.value = "en";
  });

  it("renders both options and marks the active locale as pressed", () => {
    const wrapper = mount(LanguageSwitcher);
    const buttons = wrapper.findAll("button");
    expect(buttons).toHaveLength(2);
    expect(buttons[0].text()).toBe("EN");
    expect(buttons[1].text()).toBe("中");
    expect(buttons[0].attributes("aria-pressed")).toBe("true");
    expect(buttons[1].attributes("aria-pressed")).toBe("false");
  });

  it("flips the active locale and persists the choice to localStorage", async () => {
    const wrapper = mount(LanguageSwitcher);
    const zh = wrapper.findAll("button").find((b) => b.attributes("data-locale") === "zh");
    expect(zh).toBeDefined();
    await zh!.trigger("click");
    expect(i18n.global.locale.value).toBe("zh");
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("zh");
  });

  it("clicking the already-active locale is a no-op", async () => {
    const wrapper = mount(LanguageSwitcher);
    const en = wrapper.findAll("button").find((b) => b.attributes("data-locale") === "en");
    await en!.trigger("click");
    expect(i18n.global.locale.value).toBe("en");
  });

  it("marks Chinese browser locales as Chinese without persisting a choice", () => {
    i18n.global.locale.value = "zh-CN";
    const wrapper = mount(LanguageSwitcher);
    const zh = wrapper.findAll("button").find((b) => b.attributes("data-locale") === "zh");
    const en = wrapper.findAll("button").find((b) => b.attributes("data-locale") === "en");
    expect(zh!.attributes("aria-pressed")).toBe("true");
    expect(en!.attributes("aria-pressed")).toBe("false");
    expect(window.localStorage.getItem(LOCALE_STORAGE_KEY)).toBeNull();
  });
});
