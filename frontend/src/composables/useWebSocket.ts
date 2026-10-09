import { ref } from "vue";

import { createBackoff } from "@/lib/backoff";
import { isWSFrame, type MessageAttachment, type WSMessage } from "@/lib/wsProtocol";

// Frame types live in lib/wsProtocol.ts. These three are re-exported because
// stores and chat components still import them from here.
export type { MessageAttachment, MessageSender, UserQuestionRequest } from "@/lib/wsProtocol";

type MessageHandler = (msg: WSMessage) => void;
type ReconnectCallback = () => void;

// The chat socket deliberately retries on a flat 3s cadence rather than the
// doubling schedule the file watcher uses (e0f5c9e dropped exponential
// backoff here): it is the app's only live channel, and after a server
// restart a user should not sit behind a 30s gap. Same helper, flat bounds.
const RECONNECT_DELAY_MS = 3000;
// ±20% spread, so a server restart isn't answered by every open tab
// reconnecting in the same instant.
const RECONNECT_JITTER = 0.2;

function jittered(ms: number): number {
  return Math.round(ms * (1 - RECONNECT_JITTER + Math.random() * 2 * RECONNECT_JITTER));
}

export function useWebSocket() {
  const isConnected = ref(false);
  const reconnectAttempts = ref(0);
  let ws: WebSocket | null = null;
  let messageHandler: MessageHandler | null = null;
  // Fires on every successful WebSocket open. The chat composable
  // registers a handler that re-sends `init` from current state
  // (currentConversationId.value, chatUserId.value, lastSyncedMessageId).
  // Reading from live state — rather than a closure captured at
  // connect-call time — is what makes recovery robust: if the first
  // socket's onopen never fires (handshake failure / mid-handshake
  // close), the next reconnect's onopen still re-sends init, because
  // the handler isn't tied to a specific socket lifetime.
  let reconnectCallback: ReconnectCallback | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  const reconnectBackoff = createBackoff(RECONNECT_DELAY_MS, RECONNECT_DELAY_MS);
  let intentionalClose = false;
  let generation = 0;

  function buildURL(): string {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${window.location.host}/api/terminal`;
  }

  function clearReconnectTimer() {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
  }

  function scheduleReconnect() {
    if (intentionalClose || reconnectTimer !== null) return;

    reconnectAttempts.value++;

    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      connect();
    }, jittered(reconnectBackoff.next()));
  }

  // When the tab returns to the foreground, two failure modes need
  // healing:
  //   - the socket may have been torn down silently while we were
  //     backgrounded; the reconnect timer would leave the
  //     green dot red until the next tick fires;
  //   - the socket may have stayed open but events delivered while
  //     backgrounded were dropped (broadcaster GC'd a room between turns,
  //     mobile browser throttled the WS, etc.), leaving the UI with a
  //     stale snapshot.
  // For the first case we reconnect immediately. For the second we
  // fire the reconnect callback on the existing socket — the chat
  // composable's handler re-sends `init` (which the server answers
  // with init_ok + history_backfill + init_status snapshot) and runs
  // a parallel REST cursor sync, healing both isThinking and any
  // missed messages without tearing down the connection.
  let visibilityListenerRegistered = false;
  function setupVisibilityReconnect() {
    if (visibilityListenerRegistered || typeof document === "undefined") return;
    visibilityListenerRegistered = true;
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState !== "visible") return;
      if (intentionalClose) return;
      if (ws && ws.readyState === WebSocket.OPEN) {
        // Healthy socket — request a state resync without recreating
        // the WebSocket itself.
        reconnectCallback?.();
        return;
      }
      clearReconnectTimer();
      reconnectAttempts.value = 0;
      reconnectBackoff.reset();
      // Drop any half-dead socket reference so connect() proceeds.
      ws = null;
      connect();
    });
  }

  // The server's keep-alive is a protocol-level ping the browser answers
  // invisibly, so the client has no app-level heartbeat to watch. The network
  // events stand in: a socket that lived through an offline spell is usually
  // half-dead (NAT mapping gone, Wi-Fi ↔ cellular swap) yet still reports
  // OPEN until TCP gives up minutes later, and a socket that already closed
  // shouldn't sit out the rest of its retry delay once the network is back.
  let networkListenersRegistered = false;
  let wentOffline = false;
  function setupNetworkReconnect() {
    if (networkListenersRegistered || typeof window === "undefined") return;
    networkListenersRegistered = true;
    window.addEventListener("offline", () => {
      wentOffline = true;
    });
    window.addEventListener("online", () => {
      const sawOffline = wentOffline;
      wentOffline = false;
      if (intentionalClose) return;
      if (!sawOffline && ws?.readyState === WebSocket.OPEN) return;
      forceReconnect();
    });
  }

  function forceReconnect() {
    clearReconnectTimer();
    reconnectAttempts.value = 0;
    reconnectBackoff.reset();
    if (ws) {
      // Retire the old socket first so its late onclose can't schedule a
      // second reconnect on top of this one.
      generation++;
      const stale = ws;
      ws = null;
      isConnected.value = false;
      try {
        stale.close();
      } catch {
        // Already closing / closed.
      }
    }
    connect();
  }

  function connect() {
    setupVisibilityReconnect();
    setupNetworkReconnect();
    if (ws) return;
    intentionalClose = false;
    const gen = ++generation;

    ws = new WebSocket(buildURL());

    ws.onopen = () => {
      if (gen !== generation) return;
      isConnected.value = true;
      reconnectAttempts.value = 0;
      reconnectBackoff.reset();
      // Single rejoin path: every successful open fires the registered
      // handler. The handler reads current state to build the init
      // payload, so it works for first-connect, scheduleReconnect,
      // visibility-triggered reopen, and the recovery case where an
      // earlier socket closed before its own onopen ever fired
      // (otherwise that path would leave server-side state.conversationID
      // empty and every input would be rejected until a full refresh).
      reconnectCallback?.();
    };

    ws.onclose = () => {
      if (gen !== generation) return;
      isConnected.value = false;
      ws = null;
      if (!intentionalClose) {
        scheduleReconnect();
      }
    };

    ws.onerror = () => {
      if (gen !== generation) return;
      isConnected.value = false;
    };

    ws.onmessage = (event: MessageEvent) => {
      let decoded: unknown;
      try {
        decoded = JSON.parse(event.data as string);
      } catch {
        // ignore malformed messages
        return;
      }
      // Valid JSON that isn't a routable frame (null, an array, no `type`)
      // used to reach the handler and crash there.
      if (!isWSFrame(decoded)) {
        console.error("[ws] frame without a string type; dropped", decoded);
        return;
      }
      const msg = decoded;
      // Kept out of the parse try: a bug in a frame handler used to be
      // swallowed as a "malformed message", leaving the UI stuck with no
      // trace. Still caught so one bad frame can't break the next ones.
      try {
        messageHandler?.(msg);
      } catch (err) {
        console.error(`[ws] handler for "${msg.type}" frame failed`, err);
      }
    };
  }

  function disconnect() {
    intentionalClose = true;
    generation++;
    clearReconnectTimer();
    reconnectAttempts.value = 0;
    reconnectBackoff.reset();
    ws?.close();
    ws = null;
    isConnected.value = false;
  }

  function send(msg: {
    type: string;
    content?: string;
    conversation_id?: string;
    user_id?: string;
    subscription_id?: string;
    last_message_id?: string;
    attachments?: MessageAttachment[];
    // Set on `cancel` to drop a single pending prompt by id; empty
    // means the legacy whole-conversation cancel.
    message_id?: string;
    request_id?: string;
    answers?: Record<string, string[]>;
    steer?: boolean;
  }): boolean {
    // False means the frame was not handed to the socket, so callers that
    // staged optimistic state for it can undo it instead of waiting forever.
    if (ws?.readyState !== WebSocket.OPEN) return false;
    try {
      ws.send(JSON.stringify(msg));
      return true;
    } catch {
      return false;
    }
  }

  function onMessage(handler: MessageHandler) {
    messageHandler = handler;
  }

  function onReconnect(callback: ReconnectCallback) {
    reconnectCallback = callback;
  }

  return { isConnected, reconnectAttempts, connect, disconnect, send, onMessage, onReconnect };
}
