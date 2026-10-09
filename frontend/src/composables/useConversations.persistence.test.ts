// Regression test for the conversation-panel persistence READ path.
//
// Why a dedicated file? The other useConversations test stubs
// localStorage with vi.fn().mockReturnValue(null) at the top of the
// file, which means the module is FIRST evaluated against a stub that
// always returns null — the case this test needs to differentiate from
// (a saved "true" value) gets clobbered.
//
// What broke: an earlier version of useConversations.ts called
// `readPanelState()` to initialise `isConversationPanelOpen` BEFORE
// declaring the storage-key constants the function uses. Block-scoped
// `const`s live in the temporal dead zone until their declaration
// runs, so the read threw a ReferenceError that the function's
// try/catch silently swallowed — every fresh page load reset the panel
// to closed regardless of what the user had toggled, and a duplicated
// tab booted with the panel hidden even though localStorage held
// "true". The fix moves the consts above the read.
//
// vi.hoisted is critical here: ES-module `import` statements are
// hoisted above the rest of module-level code, so a plain
// `localStorage.setItem(...)` written before the import would actually
// run AFTER useConversations evaluated. We need the seed to land at
// hoist-time so the module's top-level read sees the saved value.
import { describe, it, expect, vi } from "vitest";

vi.hoisted(() => {
  // happy-dom (the test DOM impl) provides a real Storage so this
  // setItem persists across the rest of this file's evaluation.
  localStorage.setItem("daymug-conversation-panel", "true");
});

vi.stubGlobal(
  "matchMedia",
  vi.fn().mockReturnValue({
    matches: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }),
);

vi.mock("./useApi", () => ({
  fetchConversations: vi.fn().mockResolvedValue([]),
  fetchConversationsPage: vi.fn().mockResolvedValue([]),
  createConversation: vi.fn(),
  deleteConversation: vi.fn(),
  renameConversation: vi.fn(),
  updateConversationNotifications: vi.fn(),
  fetchMessages: vi.fn().mockResolvedValue([]),
  fetchConversation: vi.fn().mockResolvedValue({}),
  updateConversationWorkDir: vi.fn(),
}));

vi.mock("./useWebSocket", () => ({
  useWebSocket: () => ({
    isConnected: { value: true },
    reconnectAttempts: { value: 0 },
    connect: vi.fn(),
    disconnect: vi.fn(),
    send: vi.fn(),
    onMessage: vi.fn(),
    onReconnect: vi.fn(),
  }),
}));

import { useConversations } from "./useConversations";

describe("conversation panel persistence — read path", () => {
  it("initialises isConversationPanelOpen from a saved 'true' value", () => {
    const { isConversationPanelOpen } = useConversations();
    expect(isConversationPanelOpen.value).toBe(true);
  });

  it("the seeded value survived module init (sanity check)", () => {
    expect(localStorage.getItem("daymug-conversation-panel")).toBe("true");
  });
});
