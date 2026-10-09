package imbot

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Platform reply chunking shared by every connector: chat APIs cap message
// size well below an agent's longest answer, so replies are split into at
// most replyMaxChunks chunks of replyChunkLimit runes.
const (
	replyChunkLimit = 3800
	replyMaxChunks  = 10
)

// progressResponder is the platform-independent Responder skeleton: the first
// Start (or an Update before any Start) posts the progress message, later
// Updates edit it in place, and Complete rewrites it with the first reply
// chunk then posts the remaining chunks as regular thread replies. Connectors
// supply only the three platform primitives.
//
// A platform with no edit-in-place API (Weixin) leaves edit nil. The whole
// progress model then degrades to "say nothing until the answer is ready":
// Start and Update become no-ops and Complete posts every chunk fresh.
// Announcing "thinking…" on a platform that can never revise that message
// would leave a stale placeholder sitting above the real answer forever.
// Callers branch on the missing primitive, never on a platform id, so a
// connector cannot claim an ability its responder does not actually wire up.
//
// Notice is the one exception to that silence: text the reader cannot infer
// from the absence of a reply goes out as a fresh message instead of being
// dropped. See Notice.
type progressResponder struct {
	messageID string

	// start posts the progress message and returns its platform message id.
	start func(ctx context.Context, text string) (string, error)
	// edit rewrites the progress message in place. Nil on platforms that
	// cannot edit a sent message; see the type comment.
	edit func(ctx context.Context, messageID, text string) error
	// post appends one more reply chunk to the thread.
	post func(ctx context.Context, chunk string) error
	// remove deletes the progress message before a final answer is posted.
	// Only connectors whose platforms support message deletion wire it up.
	remove func(ctx context.Context, messageID string) error
	// fitPreview shortens progress text the platform would reject as too
	// long. Nil when the skeleton's own budget already matches what the
	// platform accepts. Only the progress writes go through it: Complete and
	// Post carry the answer itself, which SplitReply already bounds and which
	// must never be silently shortened.
	fitPreview func(text string) string
	// refreshActivity and clearActivity are optional platform-native activity
	// indicators. They are best effort so a status API failure never prevents
	// the durable progress message or final answer from reaching the user.
	refreshActivity func(ctx context.Context)
	clearActivity   func(ctx context.Context)
	// activityInterval, when positive, keeps refreshActivity firing on its own
	// clock for as long as the turn runs. See startHeartbeat.
	activityInterval time.Duration

	mu            sync.Mutex
	heartbeatStop context.CancelFunc
	heartbeatDone chan struct{}
	activityEnded bool
}

// canEdit reports whether this platform can revise a message it already sent.
func (r *progressResponder) canEdit() bool { return r.edit != nil }

// fit applies the platform's size guard to progress text. Callers pass the
// text they would have sent, so a connector without a guard is unaffected.
func (r *progressResponder) fit(text string) string {
	if r.fitPreview == nil {
		return text
	}
	return r.fitPreview(text)
}

func (r *progressResponder) Start(ctx context.Context, text string) error {
	if !r.canEdit() {
		// Nothing durable to post yet, but the activity indicator is still
		// the one signal we can give while the agent works.
		r.touchActivity(ctx)
		return nil
	}
	id, err := r.start(ctx, r.fit(text))
	if err != nil {
		return err
	}
	r.messageID = id
	r.touchActivity(ctx)
	return nil
}

func (r *progressResponder) Update(ctx context.Context, text string) error {
	if !r.canEdit() {
		r.touchActivity(ctx)
		return nil
	}
	if r.messageID == "" {
		return r.Start(ctx, text)
	}
	if err := r.edit(ctx, r.messageID, r.fit(text)); err != nil {
		return err
	}
	r.touchActivity(ctx)
	return nil
}

// Notice delivers a standalone message the reader must see even when the turn
// never produces a reply: a failure, a cancellation, a queue position. On a
// platform that can edit it is Update verbatim — the notice supersedes the
// progress message, which is the existing Slack / Feishu / Telegram behaviour.
// Where Update is a no-op it posts instead: staying silent is right for a
// progress preview the answer will supersede anyway, but a run that failed
// supersedes nothing, and a reader left watching an indicator has no way to
// tell "still working" from "gave up two minutes ago".
func (r *progressResponder) Notice(ctx context.Context, text string) error {
	if r.canEdit() {
		return r.Update(ctx, text)
	}
	if err := r.post(ctx, r.fit(text)); err != nil {
		return err
	}
	r.touchActivity(ctx)
	return nil
}

func (r *progressResponder) touchActivity(ctx context.Context) {
	if r.refreshActivity == nil {
		return
	}
	r.refreshActivity(ctx)
	r.startHeartbeat()
}

// startHeartbeat keeps the platform indicator alive on its own clock. Refreshes
// driven by the agent stream are not enough where the indicator is the only
// progress signal there is: one long tool call emits no events for a minute at
// a time, and the indicator would lapse exactly when the reader most needs to
// know the bot is still working. Idempotent, and a no-op unless the connector
// opted in with activityInterval.
func (r *progressResponder) startHeartbeat() {
	if r.activityInterval <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activityEnded || r.heartbeatStop != nil {
		return
	}
	// Detached from the caller's context on purpose: that one is scoped to a
	// single progress write, while the indicator has to outlive every write.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.heartbeatStop, r.heartbeatDone = cancel, done
	refresh, interval := r.refreshActivity, r.activityInterval
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh(ctx)
			}
		}
	}()
}

// endActivity stops the heartbeat and clears the indicator. Idempotent because
// a turn ends either through Complete or through the bridge closing a responder
// whose run failed, and both have to leave the indicator off — a thread still
// showing "typing" long after a failure is a worse lie than the failure.
func (r *progressResponder) endActivity(ctx context.Context) {
	r.mu.Lock()
	if r.activityEnded {
		r.mu.Unlock()
		return
	}
	r.activityEnded = true
	stop, done := r.heartbeatStop, r.heartbeatDone
	r.mu.Unlock()

	if stop != nil {
		stop()
		<-done
	}
	if r.clearActivity != nil {
		r.clearActivity(ctx)
	}
}

// Close releases what the turn holds. The bridge calls it on every exit path,
// including the ones that never reach Complete.
func (r *progressResponder) Close(ctx context.Context) { r.endActivity(ctx) }

func (r *progressResponder) Post(ctx context.Context, text string) error {
	return r.post(ctx, text)
}

func (r *progressResponder) Complete(ctx context.Context, text string) error {
	defer r.endActivity(ctx)
	chunks := SplitReply(text, replyChunkLimit, replyMaxChunks)
	if len(chunks) == 0 {
		chunks = []string{EmptyReplyText}
	}
	if r.messageID == "" {
		id, err := r.start(ctx, chunks[0])
		if err != nil {
			return err
		}
		r.messageID = id
	} else if err := r.edit(ctx, r.messageID, chunks[0]); err != nil {
		return err
	}
	for _, chunk := range chunks[1:] {
		if err := r.post(ctx, chunk); err != nil {
			return err
		}
	}
	return nil
}

// completeAsNew deletes an existing progress message before posting the answer
// as fresh thread messages. Slack and Feishu do not notify a user when an edit
// adds a mention, so their final mention must live in a new message. Deletion
// deliberately happens first: callers opting into this path require the thread
// to contain only the final answer once the turn succeeds.
func (r *progressResponder) completeAsNew(ctx context.Context, text string) error {
	defer r.endActivity(ctx)
	chunks := SplitReply(text, replyChunkLimit, replyMaxChunks)
	if len(chunks) == 0 {
		chunks = []string{EmptyReplyText}
	}
	if r.messageID == "" {
		id, err := r.start(ctx, chunks[0])
		if err != nil {
			return err
		}
		r.messageID = id
		chunks = chunks[1:]
	} else {
		if r.remove == nil {
			return fmt.Errorf("delete progress message: platform does not support deletion")
		}
		if err := r.remove(ctx, r.messageID); err != nil {
			return err
		}
		r.messageID = ""
	}
	for _, chunk := range chunks {
		if err := r.post(ctx, chunk); err != nil {
			return err
		}
	}
	return nil
}
