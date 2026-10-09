package imbridge

import (
	"context"
	"sync"

	"github.com/DayMug/DayMug/backend/internal/imbot"
)

const (
	observedContextMaxThreads           = 256
	observedContextMaxMessagesPerThread = 100
)

type observedThreadContextCache struct {
	mu      sync.Mutex
	clock   uint64
	threads map[string]*observedThreadContext
}

type observedThreadContext struct {
	lastSeen uint64
	messages []imbot.Message
}

// observeThreadContext records one rule-authorized group message and installs
// a snapshot loader only when the connector cannot read platform history.
// Snapshotting here prevents messages arriving after the trigger from leaking
// backwards into a turn that is waiting on the per-thread lock.
func (b *IMBridge) observeThreadContext(msg *imbot.Message) {
	if msg == nil || msg.IsDM || msg.MessageID == "" {
		return
	}
	history := b.observedContext.observe(threadKey(*msg), *msg)
	if msg.LoadThreadMessages == nil && len(history) > 0 {
		msg.LoadThreadMessages = observedContextLoader(history)
	}
}

func (c *observedThreadContextCache) observe(key string, msg imbot.Message) []imbot.Message {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.threads == nil {
		c.threads = make(map[string]*observedThreadContext)
	}
	c.clock++
	thread := c.threads[key]
	if thread == nil {
		if len(c.threads) >= observedContextMaxThreads {
			c.evictOldestThread()
		}
		thread = &observedThreadContext{}
		c.threads[key] = thread
	}
	thread.lastSeen = c.clock

	for i := range thread.messages {
		if thread.messages[i].MessageID == msg.MessageID {
			return cloneObservedMessages(thread.messages[:i])
		}
	}

	history := cloneObservedMessages(thread.messages)
	msg.LoadThreadMessages = nil
	msg.Ack = ""
	msg.Attachments = append([]imbot.Attachment(nil), msg.Attachments...)
	thread.messages = append(thread.messages, msg)
	if len(thread.messages) > observedContextMaxMessagesPerThread {
		thread.messages = append(
			[]imbot.Message(nil),
			thread.messages[len(thread.messages)-observedContextMaxMessagesPerThread:]...,
		)
	}
	return history
}

func (c *observedThreadContextCache) evictOldestThread() {
	var oldestKey string
	var oldestSeen uint64
	for key, thread := range c.threads {
		if oldestKey == "" || thread.lastSeen < oldestSeen {
			oldestKey = key
			oldestSeen = thread.lastSeen
		}
	}
	delete(c.threads, oldestKey)
}

func observedContextLoader(snapshot []imbot.Message) func(context.Context, string) ([]imbot.Message, error) {
	return func(ctx context.Context, afterMessageID string) ([]imbot.Message, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start := 0
		if afterMessageID != "" {
			for i := len(snapshot) - 1; i >= 0; i-- {
				if snapshot[i].MessageID == afterMessageID {
					start = i + 1
					break
				}
			}
		}
		return cloneObservedMessages(snapshot[start:]), nil
	}
}

func cloneObservedMessages(messages []imbot.Message) []imbot.Message {
	cloned := make([]imbot.Message, len(messages))
	for i, msg := range messages {
		msg.LoadThreadMessages = nil
		msg.Attachments = append([]imbot.Attachment(nil), msg.Attachments...)
		cloned[i] = msg
	}
	return cloned
}
