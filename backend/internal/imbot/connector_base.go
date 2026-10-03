package imbot

import "sync"

// baseConnector holds the state every Connector implementation needs: the
// inbound sink, the one-shot down report, and the sender-name cache.
type baseConnector struct {
	msgMu     sync.Mutex
	onMessage func(Message)

	onDown   func(error)
	downOnce sync.Once

	nameMu    sync.Mutex
	nameCache map[string]string // platform user id → display name

	// mentions records the humans seen in each thread, so an agent reply that
	// writes "@name" can be turned back into a native platform mention.
	mentions mentionDirectory
}

func (b *baseConnector) SetOnMessage(fn func(Message)) {
	b.msgMu.Lock()
	defer b.msgMu.Unlock()
	b.onMessage = fn
}

// hasSink reports whether inbound messages still have somewhere to go, so a
// connector can skip normalizing an event it would only drop.
func (b *baseConnector) hasSink() bool {
	b.msgMu.Lock()
	defer b.msgMu.Unlock()
	return b.onMessage != nil
}

// deliver hands one normalized message to the sink. Taking the sink under the
// lock is what makes teardown safe: Stop clears it, so an event that a platform
// goroutine was already decoding is dropped instead of being dispatched under
// the abandoned generation's cancelled context — where it would be swallowed
// somewhere down the bridge ("context canceled") instead of answered.
func (b *baseConnector) deliver(msg Message) {
	b.msgMu.Lock()
	sink := b.onMessage
	b.msgMu.Unlock()
	if sink != nil {
		sink(msg)
	}
}

func (b *baseConnector) SetOnDown(fn func(err error)) { b.onDown = fn }

// reportDown surfaces a dead connection to the supervisor exactly once per
// Start; the supervisor rebuilds the connector, so the Once never re-arms.
func (b *baseConnector) reportDown(err error) {
	b.downOnce.Do(func() {
		if b.onDown != nil {
			b.onDown(err)
		}
	})
}

// cachedName memoizes a display-name lookup — group prompts carry the sender's
// name so the agent can tell voices apart. fetch runs outside the lock.
//
// Only a non-empty result is cached. Caching the empty string too would turn a
// transient failure (contact API 429, a timeout) into a permanent one: the user
// would show up as their raw `ou_…` / `U…` id until the connector is rebuilt.
// The cost of not caching is that a genuinely unavailable name (missing contact
// scope) re-fetches once per message from that sender, which is a bounded,
// self-healing waste rather than a stuck value.
func (b *baseConnector) cachedName(id string, fetch func() string) string {
	if id == "" {
		return ""
	}
	b.nameMu.Lock()
	if name, ok := b.nameCache[id]; ok {
		b.nameMu.Unlock()
		return name
	}
	b.nameMu.Unlock()

	name := fetch()
	if name == "" {
		return ""
	}

	b.nameMu.Lock()
	if b.nameCache == nil {
		b.nameCache = map[string]string{}
	}
	b.nameCache[id] = name
	b.nameMu.Unlock()
	return name
}
