import { afterEach, beforeEach, expect, vi } from "vitest";
import { config } from "@vue/test-utils";
import { i18n, DEFAULT_LOCALE, LOCALE_STORAGE_KEY } from "@/i18n";
import zh from "@/i18n/locales/zh";
import { resetWorkspaceUploadStore } from "@/stores/workspaceUploadStore";
import { createTestFetchRouter } from "./fetchRouter";

// Production fetches non-English messages on demand; tests flip
// `i18n.global.locale.value` synchronously, so register them all up front.
i18n.global.setLocaleMessage("zh", zh);

// Register vue-i18n globally for every component mount so individual test
// files don't have to wire it up. Tests that need a specific locale can flip
// `i18n.global.locale.value` after the per-test reset below.
config.global.plugins = [...(config.global.plugins ?? []), i18n];

let unexpectedRequests: string[] = [];

// Force the locale back to English before each test so detection-time state
// (navigator.language, localStorage) doesn't leak between files. Tests that
// want Chinese behaviour set the locale themselves.
beforeEach(() => {
  const router = createTestFetchRouter();
  unexpectedRequests = router.unexpectedRequests;
  vi.stubGlobal("fetch", vi.fn(router.fetch));
  try {
    window.localStorage.removeItem(LOCALE_STORAGE_KEY);
  } catch {
    /* ignore */
  }
  i18n.global.locale.value = DEFAULT_LOCALE;
  // Every WorkspacePanel mount joins the app-wide upload pipeline.
  resetWorkspaceUploadStore();
});

afterEach(() => {
  // Components frequently fetch from onMounted. Requiring an explicit mock
  // prevents hidden localhost requests from surviving into happy-dom teardown.
  expect(unexpectedRequests, "test issued a fetch without an explicit mock").toEqual([]);
});
