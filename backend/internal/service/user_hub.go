package service

import "sync"

// UserHub fans out user-scoped WebSocket events (e.g. conversation
// list changes) to every connection that user has open. Distinct from
// Broadcaster, which is keyed by conversation: a single human typically
// has multiple tabs each subscribed to a different conversation, but
// they all share the same user_id and want to see "session created /
// renamed / deleted" in real time across tabs.
//
// The hub deliberately keeps no replay buffer: a reconnecting tab pulls
// the canonical conversation list via REST on resume, so live events
// are best-effort. Sends are non-blocking — if a client's outbound
// channel is full the message is dropped for that client, and the seq
// stamped on the next frame that does get through tells the client to
// reconcile over REST instead of waiting for an unrelated refresh.
type UserHub struct {
	mu    sync.Mutex
	rooms map[string]map[string]*hubClient // userID → clientID → client
}

// hubClient is one connected socket plus its own delivery counter.
//
// Counting per client rather than per room (the way Broadcaster does) is what
// makes BroadcastExcept honest: the excluded client is skipped on purpose, so
// leaving its counter untouched means the omission never reads as a dropped
// frame. A room-wide counter would hand it a phantom gap and a pointless
// REST refetch on every event it originated.
type hubClient struct {
	ch  chan<- []byte
	seq uint64
}

// send stamps the next sequence number for this client and offers the frame.
// The counter advances whether or not the send lands — that is the point.
// A frame counted but never delivered shows up as a jump in the seq the
// client eventually does see, which is the only signal it gets that its view
// of the conversation list went stale.
func (c *hubClient) send(data []byte) {
	c.seq++
	select {
	case c.ch <- stampSeq(data, c.seq):
	default:
	}
}

// NewUserHub returns a ready-to-use hub.
func NewUserHub() *UserHub {
	return &UserHub{rooms: make(map[string]map[string]*hubClient)}
}

// Join registers a client's send channel under the given user id. Safe
// to call multiple times for the same client (idempotent overwrite) — the
// re-join starts a fresh delivery count, which the client reads as a restart
// rather than a gap because the sequence doesn't advance.
func (h *UserHub) Join(userID, clientID string, ch chan<- []byte) {
	if h == nil || userID == "" || clientID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[userID]
	if !ok {
		room = make(map[string]*hubClient)
		h.rooms[userID] = room
	}
	room[clientID] = &hubClient{ch: ch}
}

// Leave removes a client from the user's room. The room is deleted
// entirely when its last client departs so an idle hub doesn't grow
// unbounded across user sessions.
func (h *UserHub) Leave(userID, clientID string) {
	if h == nil || userID == "" || clientID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[userID]
	if !ok {
		return
	}
	delete(room, clientID)
	if len(room) == 0 {
		delete(h.rooms, userID)
	}
}

// Broadcast sends data to every client connected as the given user.
// Non-blocking: a backed-up client drops the event, notices the resulting
// seq gap on the next frame it receives, and resyncs through REST.
func (h *UserHub) Broadcast(userID string, data []byte) {
	if h == nil || userID == "" || len(data) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, client := range h.rooms[userID] {
		client.send(data)
	}
}

// BroadcastAll sends data to every client connected to the hub, regardless
// of user room. Global operational snapshots use this path so every signed-in
// user sees the same live running-conversation list.
func (h *UserHub) BroadcastAll(data []byte) {
	if h == nil || len(data) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, room := range h.rooms {
		for _, client := range room {
			client.send(data)
		}
	}
}

// BroadcastExcept is the same as Broadcast but skips one specific
// client. Used when the originator already updated its own UI
// optimistically and a duplicate live event would race the optimistic
// row. The skipped client's counter stays put, so a deliberate omission is
// never mistaken for a dropped frame.
func (h *UserHub) BroadcastExcept(userID, exceptClientID string, data []byte) {
	if h == nil || userID == "" || len(data) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, client := range h.rooms[userID] {
		if id == exceptClientID {
			continue
		}
		client.send(data)
	}
}
