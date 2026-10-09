import { describe, it, expect, beforeEach, vi } from "vitest";

// The store reads localStorage at module-evaluation time, so each case has to
// seed storage and then re-import to exercise a real cold start.
function stubStorage(seed: Record<string, string> = {}) {
  const mem: Record<string, string> = { ...seed };
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => mem[k] ?? null,
    setItem: (k: string, v: string) => {
      mem[k] = v;
    },
    removeItem: (k: string) => {
      delete mem[k];
    },
  });
  return mem;
}

beforeEach(() => {
  vi.resetModules();
  vi.unstubAllGlobals();
});

describe("conversation drawer state", () => {
  it("stays closed on a fresh load even though the panel preference defaults to open", async () => {
    stubStorage();
    const store = await import("./conversationListStore");

    // The regression: narrow viewports drove the modal drawer off this same
    // persisted flag, so an iPad in portrait opened with a backdrop covering
    // the app that the user never asked for.
    expect(store.isConversationPanelOpen.value).toBe(true);
    expect(store.isConversationDrawerOpen.value).toBe(false);
  });

  it("stays closed when the user has explicitly persisted an open panel", async () => {
    stubStorage({ "daymug-conversation-panel": "true" });
    const store = await import("./conversationListStore");

    expect(store.isConversationPanelOpen.value).toBe(true);
    expect(store.isConversationDrawerOpen.value).toBe(false);
  });

  it("toggles without touching the persisted panel preference", async () => {
    const mem = stubStorage({ "daymug-conversation-panel": "true" });
    const store = await import("./conversationListStore");

    store.toggleConversationDrawer();
    expect(store.isConversationDrawerOpen.value).toBe(true);

    // Dismissing the drawer must not rewrite the wide-screen column choice.
    store.closeConversationDrawer();
    expect(store.isConversationDrawerOpen.value).toBe(false);
    expect(mem["daymug-conversation-panel"]).toBe("true");
    expect(store.isConversationPanelOpen.value).toBe(true);
  });

  it("is cleared by resetConversationListStore", async () => {
    stubStorage();
    const store = await import("./conversationListStore");

    store.toggleConversationDrawer();
    expect(store.isConversationDrawerOpen.value).toBe(true);

    store.resetConversationListStore();
    expect(store.isConversationDrawerOpen.value).toBe(false);
  });
});
