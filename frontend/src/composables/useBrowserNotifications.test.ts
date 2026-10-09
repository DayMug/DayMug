import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { i18n } from "@/i18n";
import { bindConversationLookup, currentConversationId } from "@/stores/activeConversationStore";
import type { Conversation } from "./apiTypes";
import {
  _internals,
  bindBrowserNotificationNavigation,
  notifyBrowserTask,
  requestBrowserNotificationPermission,
} from "./useBrowserNotifications";

type FakeNotificationOptions = NotificationOptions;

class FakeNotification {
  static permission: NotificationPermission = "granted";
  static requestPermission = vi.fn(async (): Promise<NotificationPermission> => "granted");
  static instances: FakeNotification[] = [];

  onclick: ((event: Event) => void) | null = null;
  close = vi.fn();
  title: string;
  options: FakeNotificationOptions;

  constructor(title: string, options: FakeNotificationOptions = {}) {
    this.title = title;
    this.options = options;
    FakeNotification.instances.push(this);
  }
}

function conversation(id: string, title: string): Conversation {
  return {
    id,
    user_id: "agent-1",
    title,
    provider: "codex",
    model: "",
    work_dir: "/tmp",
    session_id: "",
    notifications_enabled: false,
    pinned: false,
    pin_order: 0,
    account_name: "",
    created_at: "",
    updated_at: "",
  };
}

function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => state,
  });
}

describe("browser task notifications", () => {
  beforeEach(() => {
    vi.stubGlobal("Notification", FakeNotification);
    vi.stubGlobal("BroadcastChannel", undefined);
    Object.defineProperty(navigator, "locks", {
      configurable: true,
      value: {
        request: vi.fn(async (_name: string, callback: () => boolean) => callback()),
      },
    });
    FakeNotification.permission = "granted";
    FakeNotification.requestPermission.mockClear();
    FakeNotification.instances = [];
    window.localStorage.removeItem(_internals.DEDUPE_STORAGE_KEY);
    _internals.resetForTest();
    currentConversationId.value = "c1";
    bindConversationLookup((id) => (id === "c1" ? conversation("c1", "Release build") : null));
    i18n.global.locale.value = "en";
    setVisibility("hidden");
  });

  afterEach(() => {
    bindConversationLookup(() => null);
    bindBrowserNotificationNavigation(null);
    vi.unstubAllGlobals();
  });

  it("does not notify for the visible tab's active conversation", async () => {
    setVisibility("visible");

    await notifyBrowserTask({ kind: "completed", conversationId: "c1", eventId: "seq-1" });

    expect(FakeNotification.instances).toHaveLength(0);
  });

  it("includes the conversation title and outcome while the tab is hidden", async () => {
    await notifyBrowserTask({ kind: "completed", conversationId: "c1", eventId: "seq-2" });

    expect(FakeNotification.instances).toHaveLength(1);
    expect(FakeNotification.instances[0].title).toBe("Task completed");
    expect(FakeNotification.instances[0].options.body).toBe("Conversation: Release build");
  });

  it("notifies for a different conversation even when this tab is visible", async () => {
    setVisibility("visible");
    bindConversationLookup((id) =>
      id === "c2" ? conversation("c2", "Background research") : null,
    );

    await notifyBrowserTask({ kind: "waiting", conversationId: "c2", eventId: "ask-1" });

    expect(FakeNotification.instances[0].title).toBe("Waiting for your input");
    expect(FakeNotification.instances[0].options.body).toBe("Conversation: Background research");
  });

  it("stays quiet when another tab reports that it is viewing the conversation", async () => {
    class ViewingPeerChannel {
      onmessage: ((event: MessageEvent) => void) | null = null;
      postMessage(message: { type: string; probeId: string }) {
        if (message.type !== "viewing-probe") return;
        queueMicrotask(() => {
          this.onmessage?.({
            data: { type: "viewing-response", probeId: message.probeId },
          } as MessageEvent);
        });
      }
      close() {}
    }
    vi.stubGlobal("BroadcastChannel", ViewingPeerChannel);
    _internals.resetForTest();
    bindBrowserNotificationNavigation(() => undefined);

    await notifyBrowserTask({ kind: "completed", conversationId: "c1", eventId: "seq-3" });

    expect(FakeNotification.instances).toHaveLength(0);
  });

  it("claims each event once so peer tabs cannot create duplicate notifications", async () => {
    const event = { kind: "failed" as const, conversationId: "c1", eventId: "error-1" };

    await notifyBrowserTask(event);
    _internals.resetForTest();
    await notifyBrowserTask(event);

    expect(FakeNotification.instances).toHaveLength(1);
    expect(navigator.locks.request).toHaveBeenCalledTimes(2);
  });

  it("focuses the window and navigates to the conversation when clicked", async () => {
    const navigate = vi.fn();
    const focus = vi.spyOn(window, "focus").mockImplementation(() => undefined);
    bindBrowserNotificationNavigation(navigate);
    await notifyBrowserTask({ kind: "waiting", conversationId: "c1", eventId: "ask-2" });

    FakeNotification.instances[0].onclick?.(new Event("click"));

    expect(FakeNotification.instances[0].close).toHaveBeenCalledOnce();
    expect(focus).toHaveBeenCalledOnce();
    expect(navigate).toHaveBeenCalledWith("c1");
  });

  it("requests permission only while the browser decision is pending", async () => {
    FakeNotification.permission = "default";
    expect(await requestBrowserNotificationPermission()).toBe("granted");
    expect(FakeNotification.requestPermission).toHaveBeenCalledOnce();

    FakeNotification.permission = "denied";
    expect(await requestBrowserNotificationPermission()).toBe("denied");
    expect(FakeNotification.requestPermission).toHaveBeenCalledOnce();
  });
});
