// Reconnect delay schedule shared by the app's WebSockets: each call to
// next() returns the current delay and doubles it for the following attempt,
// capped at maxMs; reset() goes back to baseMs once a connection is healthy.
// There is no attempt limit and no jitter — callers retry for as long as they
// are mounted.
export interface Backoff {
  next(): number;
  reset(): void;
}

export const RECONNECT_BASE_MS = 1000;
export const RECONNECT_MAX_MS = 30_000;

export function createBackoff(baseMs = RECONNECT_BASE_MS, maxMs = RECONNECT_MAX_MS): Backoff {
  let delay = baseMs;
  return {
    next() {
      const current = delay;
      delay = Math.min(delay * 2, maxMs);
      return current;
    },
    reset() {
      delay = baseMs;
    },
  };
}
