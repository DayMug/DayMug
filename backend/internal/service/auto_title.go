package service

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// AutoTitlePromptCap caps how many user prompts we feed the generator.
const AutoTitlePromptCap = 4

// autoTitleTimeout caps total wall time spent on a single auto-title attempt
// (DB lookups + generator call).
const autoTitleTimeout = 60 * time.Second

// AutoTitleStartDelay is the production value for MessagePersister.AutoTitleDelay:
// it holds the title generator back briefly before it spawns its own CLI. The
// generator runs the same backend (notably the codex CLI) under the
// conversation owner's account, concurrently with the main turn. Both processes
// refresh and rewrite the account's OAuth token file; firing them at the same
// instant races that write and can corrupt the credential (one process clobbers
// the token the other just rotated). Letting the main turn settle first lets
// its token refresh land before the title CLI touches the same file.
const AutoTitleStartDelay = 1 * time.Second

// AutoTitleMaxConcurrent caps how many auto-title CLI children may be alive at
// once, process-wide and across every provider.
//
// Two, and deliberately small: title generation is fired from PersistResult on
// *every* assistant result (and again from the IM bridge), so it is the one
// path that keeps producing new agent processes after the account pool is
// already saturated — each one a full `claude -p` / `codex` child measured at
// roughly a gigabyte on this deployment. A title is decoration; two in flight
// is plenty to keep the sidebar filling in promptly, while the cost of a burst
// stays bounded at something the box can hold alongside the real turns.
const AutoTitleMaxConcurrent = 2

// AutoTitleLimiter is a *non-blocking* counting semaphore for title-generator
// children. Non-blocking is the whole point: queueing would trade an unbounded
// pile of processes for an unbounded pile of goroutines each holding a DB
// snapshot and a 60s budget, and the queued titles would land long after
// they're useful. Refusing instead is free — the next turn calls
// MaybeAutoTitle again while the title is still empty, so a dropped attempt
// self-heals.
//
// Deliberately NOT the account Pool: titles are 30s single-shot calls, and
// charging them against MaxConcurrent would let decoration evict a user's
// actual chat turn.
type AutoTitleLimiter struct {
	slots   chan struct{}
	dropped atomic.Int64
}

// NewAutoTitleLimiter returns a limiter admitting at most capacity concurrent
// title runs. Capacities below one are clamped so a misconfigured zero doesn't
// silently disable auto-titling altogether.
func NewAutoTitleLimiter(capacity int) *AutoTitleLimiter {
	if capacity < 1 {
		capacity = 1
	}
	return &AutoTitleLimiter{slots: make(chan struct{}, capacity)}
}

// TryAcquire takes a slot if one is free and never blocks. ok=false means the
// caller must abandon this title attempt; the returned release is idempotent.
func (l *AutoTitleLimiter) TryAcquire() (release func(), ok bool) {
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-l.slots }) }, true
	default:
		l.dropped.Add(1)
		return nil, false
	}
}

// Dropped counts title attempts abandoned for want of a slot. Exposed so
// tests can assert the gate fired and an operator can tell "titles are slow"
// from "titles are being shed under load".
func (l *AutoTitleLimiter) Dropped() int64 { return l.dropped.Load() }

// InFlight reports how many title children currently hold a slot.
func (l *AutoTitleLimiter) InFlight() int { return len(l.slots) }

// Capacity is the concurrent-title ceiling this limiter enforces.
func (l *AutoTitleLimiter) Capacity() int { return cap(l.slots) }

// defaultAutoTitleLimiter has to be process-wide rather than a field value
// because callers build a throwaway MessagePersister per turn
// (Runtime.MessagePersister, AgentStreamer.persister); a limiter
// living only on the struct would be fresh on every call and gate nothing.
var defaultAutoTitleLimiter = NewAutoTitleLimiter(AutoTitleMaxConcurrent)

// ResolveTitleAccount picks the credentials the title generator should run
// under for a given conversation, by re-walking the same chain processPrompt
// uses for the main run:
//
//  1. CLI type comes from conv.Provider (or the server default for legacy
//     rows with an empty provider column).
//  2. The owner's user_provider_bindings row picks the named account within
//     that CLI type.
//  3. Pool.AccountForType resolves the name to the YAML config_dir + env.
//
// Returns ok=false (and logs) when the binding is mandatory but missing —
// the alternative is to run on the process-default config dir, which is
// exactly the bug this helper exists to prevent. Returns an empty TitleAccount
// with ok=true when the persister has no Pool (test wiring): tests stub the
// generator out anyway, and refusing to title would break their expectations.
func (p *MessagePersister) ResolveTitleAccount(ctx context.Context, conv store.Conversation) (agent.TitleAccount, bool) {
	if p.Pool == nil {
		return agent.TitleAccount{}, true
	}

	providerType := conv.Provider
	if providerType == "" {
		log.Printf("auto-title %s: skipped — conversation row has no provider", conv.ID)
		return agent.TitleAccount{}, false
	}

	user, err := p.Store.GetUser(ctx, conv.UserID)
	if err != nil {
		log.Printf("auto-title %s: skipped — get user %s: %v", conv.ID, conv.UserID, err)
		return agent.TitleAccount{}, false
	}

	// Title the conversation through the same account it chats on (its pinned
	// account when set, else the user's default for the type) so summaries hit
	// the right credentials + rate limit. Every failure takes the chat path's
	// posture — refuse rather than re-home onto a default — but is only logged
	// here: the user already saw the error on their next turn, and the
	// operator can spot it in journalctl.
	acc, err := ResolveRunAccount(p.Pool, user, providerType, conv.AccountName)
	if err != nil {
		log.Printf("auto-title %s: skipped — %v", conv.ID, err)
		return agent.TitleAccount{}, false
	}
	return agent.TitleAccount{ConfigDir: acc.ConfigDir, Env: acc.Env}, true
}

// titleLimiter returns the limiter this persister gates title children on,
// defaulting to the process-wide one so the per-turn throwaway persisters all
// share a single ceiling.
func (p *MessagePersister) titleLimiter() *AutoTitleLimiter {
	if p.TitleLimiter != nil {
		return p.TitleLimiter
	}
	return defaultAutoTitleLimiter
}

// titleGeneratorFor returns the title generator for the given provider id,
// falling back to the default TitleGen when no per-provider override exists.
func (p *MessagePersister) titleGeneratorFor(provider string) agent.TitleGenerator {
	if provider == "" && p.Cfg != nil {
		provider = p.Cfg.DefaultProviderType()
	}
	if provider != "" && p.TitleGens != nil {
		if g, ok := p.TitleGens[provider]; ok && g != nil {
			return g
		}
	}
	return p.TitleGen
}

// MaybeAutoTitle is the gate for auto-titling. It runs detached from the WS
// request lifetime (uses context.Background()) so a client disconnect doesn't
// abandon the title in flight. Skips when:
//   - no generator is wired (tests),
//   - the title is already non-empty (manual rename or prior auto-set),
//   - the conversation has no user messages yet.
//
// We attempt extraction every time it's called (after each user prompt and
// each assistant reply); the generator itself decides whether the signal is
// substantive enough to produce a title and may return "" to abstain. On
// abstention or generator failure the title stays empty so the next call
// can retry with the additional context.
func (p *MessagePersister) MaybeAutoTitle(convID string) {
	p.maybeAutoTitle(convID, "", "", "")
}

// MaybeAutoTitleReplacing runs the normal message-based title generator while
// allowing replaceableTitle to act as a provisional title. A different
// non-empty title is treated as a manual or already-generated title and is
// never overwritten.
func (p *MessagePersister) MaybeAutoTitleReplacing(convID, replaceableTitle string) {
	p.maybeAutoTitle(convID, replaceableTitle, "", "")
}

// MaybeAutoTitleReplacingWithAffixes is the IM variant of
// MaybeAutoTitleReplacing. The generated portion remains message-based, while
// titlePrefix preserves the source platform marker used to distinguish bot
// conversations in the shared conversation list, and titleSuffix preserves a
// rotated case-mode conversation's number on its thread.
func (p *MessagePersister) MaybeAutoTitleReplacingWithAffixes(convID, replaceableTitle, titlePrefix, titleSuffix string) {
	p.maybeAutoTitle(convID, replaceableTitle, titlePrefix, titleSuffix)
}

func (p *MessagePersister) maybeAutoTitle(convID, replaceableTitle, titlePrefix, titleSuffix string) {
	if p.Store == nil || convID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), autoTitleTimeout)
	defer cancel()

	// Stagger the title CLI behind the main turn so the two don't race the
	// account's OAuth token rewrite. See AutoTitleDelay / AutoTitleStartDelay.
	// Zero (the test default) skips the wait. Abort early if the (60s) budget
	// is cancelled out from under us mid-wait.
	if p.AutoTitleDelay > 0 {
		select {
		case <-time.After(p.AutoTitleDelay):
		case <-ctx.Done():
			return
		}
	}

	conv, err := p.Store.GetConversation(ctx, convID)
	if err != nil || !titleCanBeReplaced(conv.Title, replaceableTitle) {
		return
	}
	titleGen := p.titleGeneratorFor(conv.Provider)
	if titleGen == nil {
		return
	}

	// Resolve the conversation owner's bound account so the generator
	// runs under the same credentials the main turn uses. Without this
	// every conversation's titles funneled onto the daymug-process default
	// config dir (an unset CLAUDE_CONFIG_DIR / CODEX_HOME), which both
	// ignored per-user bindings and silently piled all title traffic onto
	// a single account — tripping its rate limit and breaking titles
	// globally. The lookup mirrors processPrompt's resolution so any
	// future binding rule changes stay in lockstep.
	account, ok := p.ResolveTitleAccount(ctx, conv)
	if !ok {
		return
	}

	msgs, err := p.Store.ListMessages(ctx, convID, 0, 0)
	if err != nil {
		return
	}

	// Collect the first N user prompts for the generator. Tool/thinking/
	// error roles are intentionally omitted — they rarely add summary
	// signal and can drag the title off-topic.
	prompts := make([]string, 0, AutoTitlePromptCap)
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		if len(prompts) >= AutoTitlePromptCap {
			break
		}
		prompts = append(prompts, m.Content)
	}
	if len(prompts) == 0 {
		return
	}
	prompts = expandTitleAttachments(ctx, p.Store, conv, prompts)

	// Gate the child process, not the goroutine: everything above this line is
	// cheap SQLite reads, GenerateTitle is what forks a CLI. Non-blocking, so a
	// burst of turns sheds titles instead of queueing agent processes behind an
	// already-full account pool.
	releaseSlot, ok := p.titleLimiter().TryAcquire()
	if !ok {
		log.Printf("auto-title %s: skipped — all %d title slots busy", convID, p.titleLimiter().Capacity())
		return
	}
	title, err := titleGen.GenerateTitle(ctx, prompts, account)
	// Released here rather than deferred: the slot exists to bound live child
	// processes, and the remaining work is local DB writes and broadcasts.
	releaseSlot()
	if err != nil {
		log.Printf("auto-title %s: %v", convID, err)
		return
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	title = titlePrefix + title + titleSuffix
	// The title generator runs asynchronously. Re-read before writing so a
	// manual rename made while generation was in flight always wins.
	latest, err := p.Store.GetConversation(ctx, convID)
	if err != nil || !titleCanBeReplaced(latest.Title, replaceableTitle) {
		return
	}

	if err := p.Store.UpdateConversationTitle(ctx, convID, title); err != nil {
		log.Printf("auto-title %s: persist failed: %v", convID, err)
		return
	}

	// Push the new title to any tabs currently subscribed to this room so the
	// sidebar / browser tab title update without waiting for a manual reload.
	// Generation is async (small-model CLI call up to 30s), so the earlier
	// "result"-driven refreshConversations on the frontend lands before the
	// title is persisted; this broadcast is what actually delivers it live.
	// Uses BroadcastExcept with an empty exceptClientID to deliver to all
	// connected clients without polluting the replay buffer — a reconnecting
	// tab refetches conversations via REST anyway, so the title is still
	// authoritative there.
	if p.Broadcaster != nil {
		data, err := json.Marshal(struct {
			Type           string `json:"type"`
			ConversationID string `json:"conversation_id"`
			Title          string `json:"title"`
		}{
			Type:           "title_updated",
			ConversationID: convID,
			Title:          title,
		})
		if err == nil {
			p.Broadcaster.BroadcastExcept(convID, "", data)
		}
	}

	// Also fan a `conversation_updated` event out on the user hub so
	// peer tabs that aren't subscribed to this conversation's room
	// (i.e. tabs viewing a different session in the same browser, or
	// a different browser altogether) refresh their session list with
	// the new title without waiting for the next REST poll. The title
	// path could in principle be merged into a single event type, but
	// keeping `title_updated` lets the active-conversation surface
	// react with a tighter handler and avoids re-broadcasting the
	// whole row to clients that only need the new title string.
	if p.UserHub != nil && conv.UserID != "" {
		conv.Title = title
		conv.UpdatedAt = time.Now().UTC()
		evt := struct {
			Type           string              `json:"type"`
			ConversationID string              `json:"conversation_id"`
			Conversation   *store.Conversation `json:"conversation"`
		}{
			Type:           "conversation_updated",
			ConversationID: convID,
			Conversation:   &conv,
		}
		if data, err := json.Marshal(evt); err == nil {
			p.UserHub.Broadcast(ResolveHubOwnerID(ctx, p.Store, conv.UserID), data)
		}
	}
}

func titleCanBeReplaced(current, replaceable string) bool {
	return current == "" || (replaceable != "" && current == replaceable)
}
