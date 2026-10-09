// Delivery watermarks for the two independent event streams that share one
// WebSocket. Both server-side fanouts are non-blocking by design — a slow
// client must never stall the stream for everyone else — so frames addressed
// to us are silently discarded when our socket's send buffer is full. Each
// stream stamps a monotonic `seq` on its frames; comparing it against the
// watermark is the only way to notice the loss. See `noteFrameSeq` in
// composables/chat/messageDispatcher.ts for the detection rules and the
// per-stream recovery.
//
// Plain numbers rather than refs: nothing renders these, they only gate the
// re-sync decision inside the dispatcher.
export const deliveryWatermarks = {
  // service.Broadcaster, keyed by conversation. Recovery is a cursor-based
  // REST pull of persisted messages.
  conversation: 0,
  // service.UserHub, keyed by the signed-in user. Recovery is a refetch of
  // the conversation list.
  hub: 0,
};

// The conversation-stream repair currently in flight, so a burst of dropped
// frames costs one REST pull rather than one per frame. Held as the promise
// itself so a pull that settles after a reset can recognise it is stale and
// leave the next one's guard alone. The hub stream needs no equivalent: it
// publishes to a ref, and Vue collapses a synchronous burst into one watcher
// run.
export const deliveryResync: { conversation: Promise<void> | null } = { conversation: null };

// Switching conversations moves us to a different broadcaster room with its
// own counter, so the conversation watermark has to start over — carrying the
// old one across would read as an enormous gap. The hub stream is per-socket
// and spans conversations, so it deliberately survives.
export function resetConversationDeliveryWatermark() {
  deliveryWatermarks.conversation = 0;
}

export function resetWsDeliveryStore() {
  deliveryWatermarks.conversation = 0;
  deliveryWatermarks.hub = 0;
  deliveryResync.conversation = null;
}
