package service

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// replayBufMax and replayBufMaxBytes cap the per-conversation in-flight event
// buffer from both directions. When a reconnecting client joins, these events
// are replayed so the UI can pick up a streaming response that was interrupted
// by a refresh; once either ceiling is hit, the oldest events are dropped.
//
// Two ceilings because either dimension alone leaves the buffer unbounded:
// the frame count bounds a chatty stream of tiny deltas, while the byte budget
// bounds the opposite shape — few frames, each enormous. A single tool_result
// frame carries whatever the tool returned (the NDJSON reader deliberately has
// no line limit, see streamcommon/stream.go, because a tool_result can be an
// entire file), so 4096 frames × "no per-frame limit" was never a memory bound
// at all. Sub-agent frames make that concrete: they land here via Broadcast
// but never reach the persistence path that calls ResetReplay, so nothing
// trims them mid-turn and only EndJob clears them.
//
// The byte budget is sized to comfortably hold one turn's worth of streamed
// text plus a couple of large tool payloads. Overflow is not silent: frames
// carry a monotonic seq, so a client that receives a replay with a gap can
// tell frames were dropped and re-sync over REST.
const (
	replayBufMax      = 4096
	replayBufMaxBytes = 4 << 20
)

// liveDropLogInterval rate-limits the "slow client dropped frames" log per
// room. A stalled tab drops every delta of a streaming turn, so logging each
// one would flood the log with thousands of identical lines per second.
const liveDropLogInterval = 30 * time.Second

// Room holds all WebSocket clients subscribed to a single conversation,
// plus the in-flight job state and a replay buffer of recent broadcast
// events. Rooms outlive client disconnects while a job is running, so a
// page refresh can rejoin and resume the stream without spawning a duplicate
// Claude process.
type Room struct {
	mu      sync.Mutex
	clients map[string]roomClient // clientID → connection subscription
	busy    bool
	// jobDone closes when the current owner calls EndJob. Prompt dispatchers
	// use it to wait behind provider-initiated wakeup turns without dropping
	// the durable pending row or polling the room.
	jobDone chan struct{}
	cancel  context.CancelFunc   // cancel func for the in-flight job
	steer   JobSteerer           // optional input hook for the in-flight job
	answer  JobQuestionResponder // optional structured-answer hook
	replay  [][]byte             // buffered broadcast events for late-joiners
	// replayBytes is the summed length of replay's frames, maintained
	// incrementally so enforcing the byte cap stays O(1) per broadcast rather
	// than re-walking the buffer on every streamed delta.
	replayBytes int
	// lastContextUsage caches the most recent serialized context_usage
	// message so a fresh tab joining between turns (when replay is empty)
	// can still see the latest token bar without waiting for the next
	// Claude run. Survives StartJob/EndJob and only gets replaced when a
	// newer context_usage arrives.
	lastContextUsage []byte
	// userCancelled flips to true the moment CancelJob fires for the in-flight
	// run; StartJob resets it for the next run. The stream result uses it to
	// distinguish an explicit Stop click from server shutdown or dispatcher
	// drain while preserving the same provider session in every case.
	userCancelled bool
	// seq counts frames broadcast into this room, starting at 1. Stamped onto
	// every frame — and therefore onto the replay copy too — so a client can
	// tell "nothing happened" apart from "frames were dropped on the way to
	// me". Deliberately not reset by StartJob/EndJob: a mid-conversation
	// restart would look exactly like a gap. A room recreated after idle GC
	// does start over at 1, which clients read as a restart, not a gap.
	seq uint64
	// droppedLive counts live frames a client's full channel refused. Atomic
	// so it can be read without the room lock; the rate-limited log that
	// reports it runs under r.mu, as does lastDropLog.
	droppedLive atomic.Uint64
	lastDropLog time.Time
	// droppedSinceLog is how many drops the next log line will report.
	droppedSinceLog uint64
}

// JobSteerer appends one persisted user message to an in-flight agent turn.
// Implementations must not hold Broadcaster locks while doing provider I/O.
type JobSteerer func(ctx context.Context, messageID, input string) error

// JobQuestionResponder returns structured answers to a provider request that
// is already blocking the current turn.
type JobQuestionResponder func(ctx context.Context, requestID string, answers map[string][]string) error

type roomClient struct {
	ch             chan<- []byte
	subscriptionID string
}

// Broadcaster manages per-conversation rooms so that all connected
// WebSocket clients for the same conversation receive the same events.
type Broadcaster struct {
	mu    sync.Mutex
	rooms map[string]*Room // conversationID → Room
}

// NewBroadcaster creates a ready-to-use Broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		rooms: make(map[string]*Room),
	}
}

func (b *Broadcaster) getOrCreateRoom(conversationID string) *Room {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.rooms[conversationID]
	if !ok {
		r = &Room{clients: make(map[string]roomClient)}
		b.rooms[conversationID] = r
	}
	return r
}

// gcRoomIfIdle deletes the room only when there are no clients AND no
// in-flight job. Used by Leave and EndJob; never deletes a busy room so
// that busy state and the replay buffer survive client disconnects.
func (b *Broadcaster) gcRoomIfIdle(conversationID string, expected *Room) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.rooms[conversationID]
	if !ok || cur != expected {
		return
	}
	cur.mu.Lock()
	idle := !cur.busy && len(cur.clients) == 0
	cur.mu.Unlock()
	if idle {
		delete(b.rooms, conversationID)
	}
}

// Join adds a client to the conversation room and replays any buffered
// in-flight events to it. Replay is best-effort: if the client's channel
// fills up, remaining events are dropped (the frontend can resync from the
// store once the job completes). Live broadcasts after Join cannot
// interleave with replay because both Join and Broadcast hold r.mu.
func (b *Broadcaster) Join(conversationID, clientID string, ch chan<- []byte) {
	b.JoinSubscription(conversationID, clientID, "", ch)
}

// JoinSubscription adds a browser client whose physical WebSocket may move
// between conversation rooms. Every frame delivered through this membership
// is stamped with both the conversation and logical subscription ids. The
// legacy Join method leaves frames byte-for-byte unchanged for internal
// observers and tests that are not tied to a browser subscription.
func (b *Broadcaster) JoinSubscription(
	conversationID, clientID, subscriptionID string,
	ch chan<- []byte,
) {
	r := b.getOrCreateRoom(conversationID)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, alreadyJoined := r.clients[clientID]
	r.clients[clientID] = roomClient{ch: ch, subscriptionID: subscriptionID}
	// A visibility-driven soft resync sends init again over the same socket.
	// That client has already received every live frame, so replaying the room
	// buffer here would duplicate its assistant and thinking text.
	if !alreadyJoined {
		replayFrames(r.replay, func(data []byte) bool {
			select {
			case ch <- scopeFrame(data, conversationID, subscriptionID, true):
				return true
			default:
				return false
			}
		})
	}
	// Between turns the replay buffer is empty (EndJob clears it) but we
	// still want a reconnecting tab to see the most recent token-usage bar.
	// Push the cached context_usage tail; idempotent — the frontend just
	// overwrites its current usage state with this latest snapshot.
	if r.lastContextUsage != nil {
		select {
		case ch <- scopeFrame(r.lastContextUsage, conversationID, subscriptionID, false):
		default:
		}
	}
}

// replayFrames offers every buffered frame to a newly joined client, keeping
// going past the ones that don't fit. It used to stop at the first refusal,
// which turned a momentarily full channel into the loss of the whole tail —
// a hole in the middle of the stream that rendered as ordinary text, since
// the frames carrying the evidence of the drop were exactly the ones thrown
// away. Skipping one frame instead keeps the rest, and their seq tells the
// client to re-sync.
func replayFrames(frames [][]byte, send func([]byte) bool) {
	for _, data := range frames {
		_ = send(data)
	}
}

func scopeFrame(data []byte, conversationID, subscriptionID string, replay bool) []byte {
	if subscriptionID == "" && !replay {
		return data
	}
	var msg ServerMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return data
	}
	if conversationID != "" {
		msg.ConversationID = conversationID
	}
	if subscriptionID != "" {
		msg.SubscriptionID = subscriptionID
	}
	if replay {
		msg.Replay = true
	}
	marked, err := json.Marshal(msg)
	if err != nil {
		return data
	}
	return marked
}

// Leave removes a client from the conversation room. The room is only
// torn down if no in-flight job is running; a busy room is kept so a
// reconnecting client can rejoin and pick up the stream via replay.
func (b *Broadcaster) Leave(conversationID, clientID string) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return
	}

	r.mu.Lock()
	delete(r.clients, clientID)
	r.mu.Unlock()

	b.gcRoomIfIdle(conversationID, r)
}

// Broadcast sends data to all clients in the conversation room and appends
// the event to the replay buffer so a reconnecting client can catch up.
// Non-blocking: if a client's channel is full the message is dropped for
// that client.
func (b *Broadcaster) Broadcast(conversationID string, data []byte) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	// Stamp before buffering so the replay copy carries the same seq the live
	// frame did — a reconnecting client's replayed and live frames then form
	// one continuous run it can gap-check as a whole.
	r.seq++
	stamped := stampSeq(data, r.seq)
	r.appendReplay(stamped)
	for id, client := range r.clients {
		select {
		case client.ch <- scopeFrame(stamped, conversationID, client.subscriptionID, false):
		default:
			r.noteLiveDropLocked(conversationID, id, time.Now())
		}
	}
}

// noteLiveDropLocked records one live frame refused by a slow client. The
// drop itself is deliberate (a stalled tab must not block the stream for
// everyone; it re-syncs from the seq gap), but it used to be invisible. It
// logs at most once per liveDropLogInterval per room and reports whether it
// did, so tests can pin the rate limit without capturing log output.
func (r *Room) noteLiveDropLocked(conversationID, clientID string, now time.Time) bool {
	r.droppedLive.Add(1)
	r.droppedSinceLog++
	if !r.lastDropLog.IsZero() && now.Sub(r.lastDropLog) < liveDropLogInterval {
		return false
	}
	log.Printf("broadcaster %s: dropped %d live frame(s) for slow client(s) (latest %s, total %d)",
		conversationID, r.droppedSinceLog, clientID, r.droppedLive.Load())
	r.lastDropLog = now
	r.droppedSinceLog = 0
	return true
}

// DroppedLiveFrames reports how many live frames this conversation's room has
// dropped for clients whose channel was full. Zero for an unknown room; a
// room recreated after idle GC starts over.
func (b *Broadcaster) DroppedLiveFrames(conversationID string) uint64 {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return 0
	}
	return r.droppedLive.Load()
}

// stampSeq splices a "seq" member into an already-marshalled ServerMessage.
// Done as a byte splice rather than an unmarshal/marshal round-trip because
// this runs once per streamed delta; ServerMessage.Seq carries omitempty so
// the input never already holds the key. Anything that isn't a JSON object
// passes through untouched and simply stays unsequenced.
func stampSeq(data []byte, seq uint64) []byte {
	if len(data) == 0 || data[0] != '{' {
		return data
	}
	prefix := `{"seq":` + strconv.FormatUint(seq, 10)
	out := make([]byte, 0, len(prefix)+len(data))
	out = append(out, prefix...)
	if len(data) > 2 { // not the empty object "{}"
		out = append(out, ',')
	}
	return append(out, data[1:]...)
}

// BroadcastExcept sends data to every client in the conversation room except
// the one identified by exceptClientID. Used to echo a sender-originated event
// (e.g. a user message) to other tabs viewing the same conversation while the
// sender's UI keeps its own optimistic copy.
//
// Note: these events are NOT added to the replay buffer. They're already
// persisted to the store (e.g. via SaveMessage) and a reconnecting client
// reloads the conversation from the store, so replaying them would
// duplicate content in the UI.
func (b *Broadcaster) BroadcastExcept(conversationID, exceptClientID string, data []byte) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for id, client := range r.clients {
		if id == exceptClientID {
			continue
		}
		select {
		case client.ch <- scopeFrame(data, conversationID, client.subscriptionID, false):
		default:
			r.noteLiveDropLocked(conversationID, id, time.Now())
		}
	}
}

func (r *Room) appendReplay(data []byte) {
	r.replay = append(r.replay, data)
	r.replayBytes += len(data)

	drop := 0
	// Always keep the newest frame: one frame can legitimately exceed the
	// whole byte budget, and dropping it would leave a reconnecting client
	// with nothing while the live stream carries on.
	for drop < len(r.replay)-1 &&
		(len(r.replay)-drop > replayBufMax || r.replayBytes > replayBufMaxBytes) {
		r.replayBytes -= len(r.replay[drop])
		drop++
	}
	if drop == 0 {
		return
	}
	// Reslice instead of re-copying: once the buffer is full every broadcast
	// drops a frame, and re-copying 4096 slots per streamed delta made each
	// append O(n). Nil the dropped slots first so the shared backing array
	// doesn't keep their frames reachable — the growth these caps exist to
	// stop. The reslice also shrinks cap, so append reallocates (copying only
	// the live frames) once the dead prefix has used up the spare capacity,
	// which bounds the backing array and amortizes the copy to O(1).
	clear(r.replay[:drop])
	r.replay = r.replay[drop:]
}

// setReplayLocked replaces the buffer and its byte accounting together. Every
// assignment to r.replay goes through here so the counter can't drift out of
// step with the contents — a drifted counter would either disable the byte cap
// or evict frames that are still needed.
func (r *Room) setReplayLocked(frames [][]byte) {
	r.replay = frames
	r.replayBytes = 0
	for _, f := range frames {
		r.replayBytes += len(f)
	}
}

// IsBusy returns whether the conversation has an in-flight job.
func (b *Broadcaster) IsBusy(conversationID string) bool {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

// StartJob atomically registers an in-flight Claude job for the conversation
// and clears any stale replay buffer from a previous cycle. Returns false
// (and leaves state untouched) if a job is already running for this
// conversation. The caller must invoke EndJob exactly once when the job
// finishes. The cancel function is stored so CancelJob can abort the job
// from a different WebSocket (e.g. after the original sender refreshed).
func (b *Broadcaster) StartJob(conversationID string, cancel context.CancelFunc) bool {
	r := b.getOrCreateRoom(conversationID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy {
		return false
	}
	r.startJobLocked(cancel)
	return true
}

func (r *Room) startJobLocked(cancel context.CancelFunc) {
	r.busy = true
	r.jobDone = make(chan struct{})
	r.cancel = cancel
	r.steer = nil
	r.answer = nil
	r.setReplayLocked(nil)
	r.userCancelled = false
}

// waitStartJob atomically waits for the current room owner to finish and then
// claims the room. A provider-initiated wakeup runs outside the prompt
// dispatcher's per-conversation worker, so a user prompt can legitimately
// arrive while that wakeup owns the room. Waiting keeps the already-persisted
// prompt pending instead of turning the collision into a permanent error.
func (b *Broadcaster) waitStartJob(
	ctx context.Context,
	conversationID string,
	cancel context.CancelFunc,
) bool {
	for {
		r := b.getOrCreateRoom(conversationID)
		r.mu.Lock()
		if !r.busy {
			r.startJobLocked(cancel)
			r.mu.Unlock()
			return true
		}
		if r.jobDone == nil {
			// Defensive repair for a room assembled directly by an older test
			// or embedding. EndJob reads this field and will close it normally.
			r.jobDone = make(chan struct{})
		}
		done := r.jobDone
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-done:
		}
	}
}

// SetJobSteerer attaches a provider-specific steering hook to the current
// in-flight job. It refuses idle rooms so a late registration can never leak
// into the next run.
func (b *Broadcaster) SetJobSteerer(conversationID string, steer JobSteerer) bool {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.busy {
		return false
	}
	r.steer = steer
	return true
}

// SteerJob invokes the current job's steering hook without holding room
// locks. attempted is false when this job/backend does not support steering;
// callers can then keep the message in the normal pending queue.
func (b *Broadcaster) SteerJob(ctx context.Context, conversationID, messageID, input string) (attempted bool, err error) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return false, nil
	}
	r.mu.Lock()
	steer := r.steer
	r.mu.Unlock()
	if steer == nil {
		return false, nil
	}
	return true, steer(ctx, messageID, input)
}

func (b *Broadcaster) SetJobQuestionResponder(conversationID string, answer JobQuestionResponder) bool {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.busy {
		return false
	}
	r.answer = answer
	return true
}

func (b *Broadcaster) AnswerJobQuestion(ctx context.Context, conversationID, requestID string, answers map[string][]string) (attempted bool, err error) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return false, nil
	}
	r.mu.Lock()
	answer := r.answer
	r.mu.Unlock()
	if answer == nil {
		return false, nil
	}
	return true, answer(ctx, requestID, answers)
}

// ResetReplay drops the in-flight replay buffer mid-job. Called from the
// terminal handler whenever a checkpoint of the stream has been persisted
// to the store (after each persistTool / persistResult), so a client that
// reconnects later doesn't re-render those events on top of the canonical
// REST/history_backfill copies.
//
// Without this, the bug surfaced as: REST loads the persisted tool/result
// rows in order, the broadcaster then replays the same events, so the
// next live delta lands behind the replayed copies and the streaming
// activity ends up rendered before the (statically rendered) replay
// chips — effectively pinning the live stream a few rows above the
// visible tail.
//
// Safe to call on a non-existent room (no-op).
func (b *Broadcaster) ResetReplay(conversationID string) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return
	}
	r.mu.Lock()
	r.setReplayLocked(nil)
	r.mu.Unlock()
}

// ReplaceReplay swaps the in-flight replay buffer for a canonical snapshot.
// The previous buffer remains available until the replacement is ready, so a
// client joining while a persisted checkpoint is being assembled never sees
// an empty gap. Snapshot data is not sent to clients that are already joined.
func (b *Broadcaster) ReplaceReplay(conversationID string, snapshots ...[]byte) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return
	}
	r.mu.Lock()
	r.setReplayLocked(append([][]byte(nil), snapshots...))
	r.mu.Unlock()
}

// EndJob clears the in-flight state, drops the replay buffer (the response
// is now persisted to the store), and removes the room if no clients remain.
func (b *Broadcaster) EndJob(conversationID string) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return
	}
	r.mu.Lock()
	wasBusy := r.busy
	done := r.jobDone
	r.busy = false
	r.jobDone = nil
	r.cancel = nil
	r.steer = nil
	r.answer = nil
	r.setReplayLocked(nil)
	r.mu.Unlock()
	if wasBusy && done != nil {
		close(done)
	}
	b.gcRoomIfIdle(conversationID, r)
}

// HasContextUsage reports whether the conversation's Room currently holds
// a cached context_usage frame. Used by init handlers to decide whether
// to seed the cache from a persisted DB copy or trust the in-memory
// version that's already been kept up-to-date by live broadcasts.
func (b *Broadcaster) HasContextUsage(conversationID string) bool {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lastContextUsage) > 0
}

// ClearContextUsage drops the cached context_usage frame for a
// conversation so subsequent Joins (between turns) won't replay a stale
// bar. Used after the user resets the claude session — the in-memory
// cache must follow the DB's wiped value or peer tabs will keep seeing
// the old reading. Safe to call on a non-existent room.
func (b *Broadcaster) ClearContextUsage(conversationID string) {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return
	}
	r.mu.Lock()
	r.lastContextUsage = nil
	r.mu.Unlock()
}

// SetLastContextUsage caches the most recent context_usage payload for the
// conversation so that a tab Join'ing between turns (when replay is empty)
// still sees the latest token bar. Caller passes the already-serialized
// bytes so this layer doesn't need to know the on-wire shape. Safe to call
// on a conversation with no Room — it lazily creates one (subsequent
// gcRoomIfIdle will tear it down if no clients ever join and no job runs).
func (b *Broadcaster) SetLastContextUsage(conversationID string, data []byte) {
	if conversationID == "" || len(data) == 0 {
		return
	}
	r := b.getOrCreateRoom(conversationID)
	cp := make([]byte, len(data))
	copy(cp, data)
	r.mu.Lock()
	r.lastContextUsage = cp
	r.mu.Unlock()
}

// CancelJob invokes the registered cancel function for the conversation,
// if any. Safe to call from any WebSocket — used so a "cancel" message
// from a reconnecting tab can still abort a job started by an earlier tab.
// Sets the room's userCancelled flag so the runner's cleanup can tell a
// user-initiated cancel apart from a server-shutdown cancel and act on it
// (e.g. roll back claude's session JSONL).
func (b *Broadcaster) CancelJob(conversationID string) bool {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	cancel := r.cancel
	if cancel != nil {
		r.userCancelled = true
	}
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// WasCancelled reports whether the most recent (or current) run for this
// conversation was aborted by an explicit CancelJob call. Read by the
// terminal handler after the run drains — between persistResult and the
// deferred EndJob — to gate cleanup that should ONLY trigger on a user
// cancel, not on a server drain or claude crash.
func (b *Broadcaster) WasCancelled(conversationID string) bool {
	b.mu.Lock()
	r, ok := b.rooms[conversationID]
	b.mu.Unlock()
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.userCancelled
}
