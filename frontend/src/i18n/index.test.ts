import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { detectInitialLocale, LOCALE_STORAGE_KEY, toSupportedLocale } from "./index";

describe("detectInitialLocale", () => {
  beforeEach(() => {
    window.localStorage.removeItem(LOCALE_STORAGE_KEY);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    window.localStorage.removeItem(LOCALE_STORAGE_KEY);
  });

  it("prefers an explicit choice persisted in localStorage", () => {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, "zh");
    expect(detectInitialLocale()).toBe("zh");
  });

  it("ignores an unknown stored value and falls back to navigator detection", () => {
    window.localStorage.setItem(LOCALE_STORAGE_KEY, "fr");
    vi.stubGlobal("navigator", { language: "en-US", languages: ["en-US"] });
    expect(detectInitialLocale()).toBe("en-US");
  });

  it("keeps navigator.language starting with zh as the initial locale", () => {
    vi.stubGlobal("navigator", { language: "zh-CN", languages: ["zh-CN", "zh"] });
    expect(detectInitialLocale()).toBe("zh-CN");
  });

  it("keeps navigator.language starting with en as the initial locale", () => {
    vi.stubGlobal("navigator", { language: "en-GB", languages: ["en-GB"] });
    expect(detectInitialLocale()).toBe("en-GB");
  });

  it("keeps the browser locale when no app translation exists for it", () => {
    vi.stubGlobal("navigator", { language: "fr-FR", languages: ["fr-FR", "de-DE"] });
    expect(detectInitialLocale()).toBe("fr-FR");
  });

  it("uses the first non-empty navigator.languages entry", () => {
    vi.stubGlobal("navigator", { language: "fr-FR", languages: ["", "zh-TW"] });
    expect(detectInitialLocale()).toBe("zh-TW");
  });
});

describe("lazy locale messages", () => {
  it("ships only English up front and registers Chinese on demand", async () => {
    vi.resetModules();
    const mod = await import("./index");

    expect(mod.isLocaleLoaded("en")).toBe(true);
    expect(mod.isLocaleLoaded("zh")).toBe(false);

    await mod.loadLocaleMessages("zh");

    expect(mod.isLocaleLoaded("zh")).toBe(true);
    mod.i18n.global.locale.value = "zh";
    expect(mod.i18n.global.t("common.cancel")).toBe("取消");
  });

  it("maps browser tags onto the translation that serves them", () => {
    expect(toSupportedLocale("zh-TW")).toBe("zh");
    expect(toSupportedLocale("en-GB")).toBe("en");
    expect(toSupportedLocale("fr-FR")).toBe("en");
  });
});
