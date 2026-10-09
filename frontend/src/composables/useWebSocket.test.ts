import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { useWebSocket } from "./useWebSocket";

class MockWebSocket {
  static OPEN = 1;
  readyState = MockWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  sent: string[] = [];

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    this.onclose?.();
  }
}

let mockInstances: MockWebSocket[];

beforeEach(() => {
  vi.useFakeTimers();
  // Centre of the ±20% reconnect jitter, so delays read as the nominal 3s.
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  mockInstances = [];
  function FakeWebSocket() {
    const ws = new MockWebSocket();
    mockInstances.push(ws);
    return ws;
  }
  FakeWebSocket.OPEN = 1;
  vi.stubGlobal("WebSocket", FakeWebSocket);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

// Captures this instance's window network listeners so a test can fire them
// without also waking every useWebSocket instance earlier tests created.
function captureNetworkListeners() {
  const handlers: Record<string, Array<() => void>> = { online: [], offline: [] };
  const origAdd = window.addEventListener.bind(window);
  vi.spyOn(window, "addEventListener").mockImplementation(
    (evt: string, handler: EventListenerOrEventListenerObject) => {
      if ((evt === "online" || evt === "offline") && typeof handler === "function") {
        handlers[evt].push(handler as () => void);
        return;
      }
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      return origAdd(evt as any, handler as any);
    },
  );
  return {
    fire: (evt: "online" | "offline") => handlers[evt].forEach((h) => h()),
  };
}

function latestMock(): MockWebSocket {
  return mockInstances[mockInstances.length - 1];
}

describe("useWebSocket", () => {
  it("connects and sets isConnected", () => {
    const { isConnected, connect } = useWebSocket();
    expect(isConnected.value).toBe(false);

    connect();
    latestMock().onopen?.();

    expect(isConnected.value).toBe(true);
  });

  it("disconnects and clears isConnected", () => {
    const { isConnected, connect, disconnect } = useWebSocket();

    connect();
    latestMock().onopen?.();
    expect(isConnected.value).toBe(true);

    disconnect();
    expect(isConnected.value).toBe(false);
  });

  it("sends JSON messages", () => {
    const { connect, send } = useWebSocket();

    connect();
    const ws = latestMock();
    ws.onopen?.();

    expect(send({ type: "input", content: "hello" })).toBe(true);

    expect(ws.sent).toHaveLength(1);
    expect(JSON.parse(ws.sent[0])).toEqual({
      type: "input",
      content: "hello",
    });
  });

  it("receives and dispatches messages", () => {
    const { connect, onMessage } = useWebSocket();
    const handler = vi.fn();

    connect();
    onMessage(handler);

    latestMock().onmessage?.({
      data: JSON.stringify({ type: "output", content: "hi" }),
    });

    expect(handler).toHaveBeenCalledWith({ type: "output", content: "hi" });
  });

  it("ignores malformed messages", () => {
    const { connect, onMessage } = useWebSocket();
    const handler = vi.fn();

    connect();
    onMessage(handler);

    latestMock().onmessage?.({ data: "not json" });

    expect(handler).not.toHaveBeenCalled();
  });

  it.each([["null"], ["[]"], ['"text"'], ['{"content":"no type"}']])(
    "drops and reports a decoded value that isn't a frame: %s",
    (data) => {
      const { connect, onMessage } = useWebSocket();
      const handler = vi.fn();
      const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});

      connect();
      onMessage(handler);
      latestMock().onmessage?.({ data });

      expect(handler).not.toHaveBeenCalled();
      expect(errorSpy).toHaveBeenCalledOnce();
      errorSpy.mockRestore();
    },
  );

  it("reports handler failures instead of treating them as malformed frames", () => {
    const { connect, onMessage } = useWebSocket();
    const failure = new Error("dispatcher bug");
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    onMessage(() => {
      throw failure;
    });

    connect();
    latestMock().onmessage?.({ data: JSON.stringify({ type: "output", content: "hi" }) });

    expect(errorSpy).toHaveBeenCalledWith('[ws] handler for "output" frame failed', failure);
    errorSpy.mockRestore();
  });

  it("auto-reconnects after unexpected close", () => {
    const { connect, isConnected, reconnectAttempts } = useWebSocket();

    connect();
    const ws1 = latestMock();
    ws1.onopen?.();
    expect(isConnected.value).toBe(true);

    // Simulate unexpected close
    ws1.onclose?.();
    expect(isConnected.value).toBe(false);
    expect(reconnectAttempts.value).toBe(1);

    // Advance timer past the reconnect delay (3000ms)
    vi.advanceTimersByTime(3000);

    // A new WebSocket should have been created
    expect(mockInstances.length).toBe(2);
  });

  it("does not reconnect after intentional disconnect", () => {
    const { connect, disconnect } = useWebSocket();

    connect();
    latestMock().onopen?.();

    disconnect();

    vi.advanceTimersByTime(5000);
    // Only the original instance should exist
    expect(mockInstances.length).toBe(1);
  });

  // Single rejoin path: the registered callback fires on every
  // successful open, including the first. The chat composable's
  // handler reads current state to build the init payload, so an
  // idempotent re-fire is correct regardless of whether this is a
  // brand-new connect or a reopen.
  it("fires onReconnect callback on every successful open", () => {
    const { connect, onReconnect } = useWebSocket();
    const callback = vi.fn();
    onReconnect(callback);

    connect();
    const ws1 = latestMock();
    ws1.onopen?.();
    expect(callback).toHaveBeenCalledTimes(1);

    // Simulate unexpected close + reconnect.
    ws1.onclose?.();
    vi.advanceTimersByTime(3000);
    latestMock().onopen?.();

    expect(callback).toHaveBeenCalledTimes(2);
  });

  // Regression: if the very first socket closes before its own onopen
  // ever fires (failed handshake, network blip mid-upgrade, server
  // returning 401 on the upgrade response), the reconnect that follows
  // must still fire the rejoin callback. Earlier code tied init to an
  // onReady closure bound to that one dead socket, so the next reopen
  // landed at the server with state.conversationID == "" and every
  // subsequent input was rejected as "init required before input" —
  // visible to the user only after refreshing the browser.
  it("fires onReconnect callback when the first socket closes before opening", () => {
    const { connect, onReconnect } = useWebSocket();
    const callback = vi.fn();
    onReconnect(callback);

    connect();
    const ws1 = latestMock();
    // Skip onopen entirely — handshake never completes.
    ws1.onclose?.();
    expect(callback).not.toHaveBeenCalled();

    vi.advanceTimersByTime(3000);
    const ws2 = latestMock();
    expect(ws2).not.toBe(ws1);
    ws2.onopen?.();

    expect(callback).toHaveBeenCalledTimes(1);
  });

  it("old onclose does not clobber new connection after disconnect→connect", () => {
    const { connect, disconnect, isConnected } = useWebSocket();

    connect();
    const ws1 = latestMock();
    // Save onclose before disconnect nulls it via synchronous close
    const ws1Onclose = ws1.onclose;
    // Prevent synchronous onclose during disconnect
    ws1.close = vi.fn();
    ws1.onopen?.();
    expect(isConnected.value).toBe(true);

    // disconnect then immediately connect (simulates switchConversation)
    disconnect();
    expect(isConnected.value).toBe(false);

    connect();
    const ws2 = latestMock();
    expect(ws2).not.toBe(ws1);
    ws2.onopen?.();
    expect(isConnected.value).toBe(true);

    // Old ws1's onclose fires asynchronously — should be ignored
    ws1Onclose?.();
    expect(isConnected.value).toBe(true);
    // ws2 should still be the active connection (not clobbered)
    expect(mockInstances.length).toBe(2);
  });

  // When the user comes back to the tab after the socket dropped while it
  // was hidden, the reconnect timer would otherwise leave the green dot
  // red until the next scheduled tick. The visibility handler should kick a
  // fresh connect immediately so the user doesn't perceive any wait.
  it("reconnects immediately on visibility=visible after an unexpected close", () => {
    const visibilityHandlers: Array<() => void> = [];
    const origAdd = document.addEventListener.bind(document);
    const addSpy = vi
      .spyOn(document, "addEventListener")
      .mockImplementation((evt: string, handler: EventListenerOrEventListenerObject) => {
        if (evt === "visibilitychange" && typeof handler === "function") {
          visibilityHandlers.push(handler as () => void);
          return;
        }
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        return origAdd(evt as any, handler as any);
      });

    const { connect } = useWebSocket();
    connect();
    const ws1 = latestMock();
    ws1.onopen?.();

    // Tab goes hidden, then the socket dies in the background.
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    ws1.onclose?.();
    expect(mockInstances.length).toBe(1);

    // Tab returns to foreground BEFORE the 3000ms reconnect fires.
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    visibilityHandlers.forEach((h) => h());

    // A new WebSocket was created immediately, without waiting for the timer.
    expect(mockInstances.length).toBe(2);

    addSpy.mockRestore();
  });

  // Without re-firing the rejoin handler on visibility-triggered reopen
  // the next user prompt would arrive at the server before re-init and
  // be rejected with "init required before input" — observable as a red
  // error banner in the chat after the tab returns from background.
  it("fires onReconnect callback after a visibility-triggered reopen", () => {
    const visibilityHandlers: Array<() => void> = [];
    const origAdd = document.addEventListener.bind(document);
    const addSpy = vi
      .spyOn(document, "addEventListener")
      .mockImplementation((evt: string, handler: EventListenerOrEventListenerObject) => {
        if (evt === "visibilitychange" && typeof handler === "function") {
          visibilityHandlers.push(handler as () => void);
          return;
        }
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        return origAdd(evt as any, handler as any);
      });

    const { connect, onReconnect } = useWebSocket();
    const callback = vi.fn();
    onReconnect(callback);

    connect();
    const ws1 = latestMock();
    ws1.onopen?.();

    // Tab hides, socket dies in background.
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    ws1.onclose?.();

    // Tab returns BEFORE the backoff timer fires; visibility handler
    // resets reconnectAttempts to 0 before calling connect().
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    visibilityHandlers.forEach((h) => h());

    latestMock().onopen?.();

    // First open + visibility-driven reopen → fired twice.
    expect(callback).toHaveBeenCalledTimes(2);

    addSpy.mockRestore();
  });

  it("does not reconnect on visibility=visible when the connection is already healthy", () => {
    const visibilityHandlers: Array<() => void> = [];
    const origAdd = document.addEventListener.bind(document);
    const addSpy = vi
      .spyOn(document, "addEventListener")
      .mockImplementation((evt: string, handler: EventListenerOrEventListenerObject) => {
        if (evt === "visibilitychange" && typeof handler === "function") {
          visibilityHandlers.push(handler as () => void);
          return;
        }
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        return origAdd(evt as any, handler as any);
      });

    const { connect } = useWebSocket();
    connect();
    latestMock().onopen?.();

    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    visibilityHandlers.forEach((h) => h());

    expect(mockInstances.length).toBe(1);

    addSpy.mockRestore();
  });

  // A healthy socket can still lose events: mobile browsers throttle
  // background WS, the broadcaster may have GC'd a room between turns,
  // a flaky network drops a frame. Firing the reconnect callback on
  // visibility-visible lets the chat composable re-init (which fetches
  // an authoritative init_status snapshot + history_backfill) without
  // tearing down the socket. Without this the bug observed by the user
  // — "still thinking" on the original tab vs. completed reply on a
  // fresh tab — would persist until the next real reconnect.
  it("fires onReconnect callback on visibility=visible to force a soft resync", () => {
    const visibilityHandlers: Array<() => void> = [];
    const origAdd = document.addEventListener.bind(document);
    const addSpy = vi
      .spyOn(document, "addEventListener")
      .mockImplementation((evt: string, handler: EventListenerOrEventListenerObject) => {
        if (evt === "visibilitychange" && typeof handler === "function") {
          visibilityHandlers.push(handler as () => void);
          return;
        }
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        return origAdd(evt as any, handler as any);
      });

    const { connect, onReconnect } = useWebSocket();
    const callback = vi.fn();
    onReconnect(callback);

    connect();
    latestMock().onopen?.();
    // The initial onopen has already fired the callback once.
    expect(callback).toHaveBeenCalledTimes(1);

    // Tab becomes visible while the socket is still healthy.
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    visibilityHandlers.forEach((h) => h());

    // No new WebSocket was created, but the callback fired again so
    // the chat composable re-sends init and runs a REST cursor sync.
    expect(mockInstances.length).toBe(1);
    expect(callback).toHaveBeenCalledTimes(2);

    addSpy.mockRestore();
  });

  it("keeps reconnecting every three seconds until a connection opens", () => {
    const { connect, reconnectAttempts } = useWebSocket();

    connect();
    const ws1 = latestMock();
    ws1.onopen?.();

    // Close unexpectedly
    ws1.onclose?.();

    // First attempt at 3000ms
    vi.advanceTimersByTime(2999);
    expect(mockInstances.length).toBe(1);
    vi.advanceTimersByTime(1);
    expect(mockInstances.length).toBe(2);
    expect(reconnectAttempts.value).toBe(1);

    // The retry also fails before opening.
    latestMock().onclose?.();

    // Further retries keep the same three-second interval.
    vi.advanceTimersByTime(2999);
    expect(mockInstances.length).toBe(2);
    vi.advanceTimersByTime(1);
    expect(mockInstances.length).toBe(3);
    expect(reconnectAttempts.value).toBe(2);

    // There is no attempt limit: a later failure still schedules another retry.
    for (let attempt = 3; attempt <= 25; attempt++) {
      latestMock().onclose?.();
      vi.advanceTimersByTime(3000);
      expect(mockInstances.length).toBe(attempt + 1);
    }

    latestMock().onopen?.();
    expect(reconnectAttempts.value).toBe(0);
  });

  it("reports a send on a socket that isn't open as not sent", () => {
    const { connect, send } = useWebSocket();
    expect(send({ type: "input", content: "early" })).toBe(false);

    connect();
    const ws = latestMock();
    ws.readyState = 3; // CLOSED, before onclose has run
    expect(send({ type: "input", content: "late" })).toBe(false);
    expect(ws.sent).toHaveLength(0);
  });

  it("spreads the reconnect delay by ±20%", () => {
    const delays: number[] = [];
    for (const r of [0, 0.999]) {
      vi.mocked(Math.random).mockReturnValue(r);
      const spy = vi.spyOn(globalThis, "setTimeout");
      const { connect, disconnect } = useWebSocket();
      connect();
      latestMock().onopen?.();
      latestMock().onclose?.();
      delays.push(spy.mock.calls.at(-1)?.[1] as number);
      spy.mockRestore();
      disconnect();
    }
    expect(delays[0]).toBe(2400);
    expect(delays[1]).toBeGreaterThanOrEqual(3590);
    expect(delays[1]).toBeLessThanOrEqual(3600);
  });

  it("reconnects immediately when the network comes back after a drop", () => {
    const net = captureNetworkListeners();
    const { connect, isConnected } = useWebSocket();
    connect();
    latestMock().onopen?.();
    latestMock().onclose?.();
    expect(mockInstances).toHaveLength(1);

    net.fire("online");

    expect(mockInstances).toHaveLength(2);
    latestMock().onopen?.();
    expect(isConnected.value).toBe(true);
    // The pending 3s retry was cancelled, not left to open a third socket.
    vi.advanceTimersByTime(10_000);
    expect(mockInstances).toHaveLength(2);
  });

  it("replaces a socket that still looks open after an offline spell", () => {
    const net = captureNetworkListeners();
    const { connect } = useWebSocket();
    connect();
    const stale = latestMock();
    stale.onopen?.();
    const close = vi.spyOn(stale, "close");

    net.fire("offline");
    net.fire("online");

    expect(close).toHaveBeenCalled();
    expect(mockInstances).toHaveLength(2);
    // The retired socket's close event must not schedule another reconnect.
    vi.advanceTimersByTime(10_000);
    expect(mockInstances).toHaveLength(2);
  });

  it("leaves a healthy socket alone on a spurious online event", () => {
    const net = captureNetworkListeners();
    const { connect } = useWebSocket();
    connect();
    latestMock().onopen?.();

    net.fire("online");

    expect(mockInstances).toHaveLength(1);
  });

  it("does not reconnect on network events after an intentional disconnect", () => {
    const net = captureNetworkListeners();
    const { connect, disconnect } = useWebSocket();
    connect();
    latestMock().onopen?.();
    disconnect();

    net.fire("offline");
    net.fire("online");

    expect(mockInstances).toHaveLength(1);
  });
});
