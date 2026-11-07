import { i18n } from "@/i18n";
import { desktopClient } from "@/lib/desktopClient";
import { currentConversationId, lookupConversation } from "@/stores/activeConversationStore";

export type BrowserTaskNotificationKind = "completed" | "failed" | "waiting";

export interface BrowserTaskNotification {
  kind: BrowserTaskNotificationKind;
  conversationId: string;
  eventId: string;
}

type ConversationNavigator = (conversationId: string) => void;

const DEDUPE_LOCK = "daymug-browser-notification";
// DayMug-Client shows task notifications natively: WKWebView has no
// Notification API and WebKitGTK refuses it, so the page asks the client
// through a same-origin navigation it intercepts (like the browser-login
// handoff). A click on the native notification comes back as this DOM event.
const DESKTOP_NOTIFY_PATH = "/__daymug_client__/notify";
const DESKTOP_CLICK_EVENT = "daymug-client-notification-click";
const DEDUPE_STORAGE_KEY = "daymug-browser-notification-claims";
const DEDUPE_TTL_MS = 5 * 60 * 1000;
const VIEWING_PROBE_MS = 75;

let navigateToConversation: ConversationNavigator | null = null;
let desktopClickListener: ((event: Event) => void) | null = null;
const memoryClaims = new Map<string, number>();
let notificationChannel: BroadcastChannel | null | undefined;
const pendingViewingProbes = new Map<
  string,
  { resolve: (viewing: boolean) => void; timer: ReturnType<typeof setTimeout> }
>();

type BrowserNotificationChannelMessage =
  | { type: "viewing-probe"; probeId: string; conversationId: string }
  | { type: "viewing-response"; probeId: string };

function notificationAPI(): typeof Notification | null {
  if (typeof window === "undefined" || !("Notification" in window)) return null;
  return window.Notification;
}

function desktopNotifications(): boolean {
  return desktopClient()?.notificationNavigation === true;
}

function isConversationBeingViewed(conversationId: string): boolean {
  return (
    typeof document !== "undefined" &&
    document.visibilityState === "visible" &&
    currentConversationId.value === conversationId
  );
}

function getNotificationChannel(): BroadcastChannel | null {
  if (notificationChannel !== undefined) return notificationChannel;
  if (typeof BroadcastChannel === "undefined") {
    notificationChannel = null;
    return notificationChannel;
  }
  notificationChannel = new BroadcastChannel("daymug-browser-notifications");
  notificationChannel.onmessage = (event: MessageEvent<BrowserNotificationChannelMessage>) => {
    const message = event.data;
    if (!message || typeof message.probeId !== "string") return;
    if (message.type === "viewing-probe") {
      if (isConversationBeingViewed(message.conversationId)) {
        notificationChannel?.postMessage({
          type: "viewing-response",
          probeId: message.probeId,
        } satisfies BrowserNotificationChannelMessage);
      }
      return;
    }
    const pending = pendingViewingProbes.get(message.probeId);
    if (!pending) return;
    clearTimeout(pending.timer);
    pendingViewingProbes.delete(message.probeId);
    pending.resolve(true);
  };
  return notificationChannel;
}

async function isConversationViewedInAnotherTab(conversationId: string): Promise<boolean> {
  const channel = getNotificationChannel();
  if (!channel) return false;
  const probeId =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random()}`;
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      pendingViewingProbes.delete(probeId);
      resolve(false);
    }, VIEWING_PROBE_MS);
    pendingViewingProbes.set(probeId, { resolve, timer });
    channel.postMessage({
      type: "viewing-probe",
      probeId,
      conversationId,
    } satisfies BrowserNotificationChannelMessage);
  });
}

function closeNotificationChannel() {
  notificationChannel?.close();
  notificationChannel = undefined;
  for (const pending of pendingViewingProbes.values()) {
    clearTimeout(pending.timer);
    pending.resolve(false);
  }
  pendingViewingProbes.clear();
}

function parseStoredClaims(): Record<string, number> {
  try {
    const parsed = JSON.parse(window.localStorage.getItem(DEDUPE_STORAGE_KEY) ?? "{}") as Record<
      string,
      unknown
    >;
    return Object.fromEntries(
      Object.entries(parsed).filter((entry): entry is [string, number] =>
        Number.isFinite(entry[1]),
      ),
    );
  } catch {
    return {};
  }
}

function claimNotification(key: string): boolean {
  const now = Date.now();
  for (const [claim, timestamp] of memoryClaims) {
    if (now - timestamp >= DEDUPE_TTL_MS) memoryClaims.delete(claim);
  }
  if (memoryClaims.has(key)) return false;

  try {
    const claims = parseStoredClaims();
    for (const [claim, timestamp] of Object.entries(claims)) {
      if (now - timestamp >= DEDUPE_TTL_MS) delete claims[claim];
    }
    if (claims[key] !== undefined) {
      memoryClaims.set(key, claims[key]);
      return false;
    }
    claims[key] = now;
    window.localStorage.setItem(DEDUPE_STORAGE_KEY, JSON.stringify(claims));
  } catch {
    // A same-origin Web Lock normally serializes the storage claim. If
    // storage is unavailable, the Notification tag and this tab-local map
    // still prevent the common duplicate paths.
  }

  memoryClaims.set(key, now);
  return true;
}

async function claimAcrossTabs(key: string): Promise<boolean> {
  const locks = typeof navigator !== "undefined" ? navigator.locks : undefined;
  if (!locks) return claimNotification(key);
  try {
    return await locks.request(DEDUPE_LOCK, () => claimNotification(key));
  } catch {
    return claimNotification(key);
  }
}

function translated(key: string, params?: Record<string, string>): string {
  return String(i18n.global.t(key, params ?? {}));
}

function stableHash(value: string): string {
  let hash = 2166136261;
  for (let index = 0; index < value.length; index++) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}

export function bindBrowserNotificationNavigation(handler: ConversationNavigator | null) {
  navigateToConversation = handler;
  if (desktopClickListener) {
    window.removeEventListener(DESKTOP_CLICK_EVENT, desktopClickListener);
    desktopClickListener = null;
  }
  if (handler) {
    getNotificationChannel();
    desktopClickListener = (event) => {
      const id = (event as CustomEvent<{ conversationId?: unknown }>).detail?.conversationId;
      if (typeof id === "string" && id) navigateToConversation?.(id);
    };
    window.addEventListener(DESKTOP_CLICK_EVENT, desktopClickListener);
  } else {
    closeNotificationChannel();
  }
}

export async function requestBrowserNotificationPermission(): Promise<NotificationPermission | null> {
  // The desktop client needs no permission and must not be asked for one:
  // WebView2 would show its own prompt for an API the page will not use.
  if (desktopNotifications()) return "granted";
  const api = notificationAPI();
  if (!api) return null;
  if (api.permission !== "default") return api.permission;
  try {
    return await api.requestPermission();
  } catch {
    return null;
  }
}

export async function notifyBrowserTask(event: BrowserTaskNotification): Promise<void> {
  if (!event.conversationId || !event.eventId) return;
  const desktop = desktopNotifications();
  // A browser tab showing the conversation is enough; a desktop window
  // showing it may still be behind other windows, which only the client
  // can tell, so it gets the fact and decides.
  const viewing = isConversationBeingViewed(event.conversationId);
  if (viewing && !desktop) return;

  const api = notificationAPI();
  if (!desktop && (!api || api.permission !== "granted")) return;
  if (await isConversationViewedInAnotherTab(event.conversationId)) return;

  const dedupeKey = stableHash(`${event.conversationId}:${event.kind}:${event.eventId}`);
  if (!(await claimAcrossTabs(dedupeKey))) return;

  const conversationTitle =
    lookupConversation(event.conversationId)?.title.trim() ||
    translated("browserNotifications.untitledConversation");
  const title = translated(`browserNotifications.${event.kind}`);
  const body = translated("browserNotifications.conversation", { title: conversationTitle });
  if (desktop) {
    // The client cancels this navigation, so the page stays as it is.
    window.location.assign(
      `${DESKTOP_NOTIFY_PATH}?${new URLSearchParams({
        title,
        body,
        conversation: event.conversationId,
        viewing: viewing ? "1" : "0",
      })}`,
    );
    return;
  }
  if (!api) return;
  try {
    const notification = new api(title, {
      body,
      icon: "/favicon.svg",
      tag: `daymug:${dedupeKey}`,
    });
    notification.onclick = () => {
      notification.close();
      window.focus();
      navigateToConversation?.(event.conversationId);
    };
  } catch {
    // Permission can be revoked between the check and construction.
  }
}

export const _internals = {
  DEDUPE_STORAGE_KEY,
  DEDUPE_TTL_MS,
  VIEWING_PROBE_MS,
  isConversationBeingViewed,
  stableHash,
  resetForTest() {
    bindBrowserNotificationNavigation(null);
    memoryClaims.clear();
  },
};
