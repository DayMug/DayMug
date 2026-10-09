package wechat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CommandNew is the in-band instruction that starts a fresh conversation.
//
// Weixin has no threads: every message from one peer arrives in a single flat
// stream, so there is no platform signal for "this is a new topic". Without
// one, a conversation would grow forever and every reply would carry the whole
// history. /new is that missing signal — the user draws the boundary
// explicitly, and everything after it belongs to a new thread id.
const CommandNew = "new"

// Command is an in-band control instruction parsed out of a message.
type Command struct {
	// Name is the instruction without its leading slash, lowercased.
	Name string
	// Rest is whatever followed the instruction, with leading blank space and
	// newlines removed. Empty when the user sent the bare command.
	Rest string
}

// ParseCommand recognises a leading "/word" instruction.
//
// It only reports the shape, never whether the instruction is meaningful:
// callers act on the names they know and treat the rest as ordinary text. That
// keeps a message that merely happens to open with a slash — a path, a date, a
// snippet — from being swallowed.
func ParseCommand(text string) (Command, bool) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, "/") {
		return Command{}, false
	}
	body := trimmed[1:]
	idx := strings.IndexFunc(body, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	name := body
	rest := ""
	if idx >= 0 {
		name = body[:idx]
		rest = body[idx+1:]
	}
	if !isCommandName(name) {
		return Command{}, false
	}
	return Command{Name: strings.ToLower(name), Rest: strings.TrimLeft(rest, " \t\r\n")}, true
}

// isCommandName keeps the grammar tight enough that a filesystem path or a
// date cannot masquerade as an instruction.
func isCommandName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// ThreadStore persists the peer→thread assignment across restarts. Losing it
// silently starts everyone on a new thread, which is safe but discards
// continuity, so anything long-lived should use FileThreadStore.
type ThreadStore interface {
	Load() (map[string]string, error)
	Save(map[string]string) error
}

// MemoryThreadStore keeps assignments in memory only.
type MemoryThreadStore struct {
	mu    sync.Mutex
	state map[string]string
}

func (m *MemoryThreadStore) Load() (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneMap(m.state), nil
}

func (m *MemoryThreadStore) Save(state map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = cloneMap(state)
	return nil
}

// FileThreadStore persists assignments as a JSON object, written atomically.
type FileThreadStore struct {
	Path string
	mu   sync.Mutex
}

func (f *FileThreadStore) Load() (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("wechat: read threads: %w", err)
	}
	state := map[string]string{}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("wechat: decode threads: %w", err)
	}
	return state, nil
}

func (f *FileThreadStore) Save(state map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return fmt.Errorf("wechat: create threads dir: %w", err)
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("wechat: encode threads: %w", err)
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("wechat: write threads: %w", err)
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		return fmt.Errorf("wechat: commit threads: %w", err)
	}
	return nil
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

// Threads assigns each peer a thread id and rotates it on /new.
//
// The id is what a DayMug conversation will eventually be keyed by, so it has
// to stay stable across restarts and never be reused after a reset — otherwise
// a new topic would resume the previous conversation's session.
type Threads struct {
	mu     sync.Mutex
	byPeer map[string]string
	store  ThreadStore
	newID  func() string
	logf   func(string, ...any)
}

// ThreadsOptions configures a Threads tracker.
type ThreadsOptions struct {
	Store ThreadStore
	// NewID overrides id generation in tests.
	NewID func() string
	// Logf receives non-fatal persistence failures.
	Logf func(format string, args ...any)
}

// NewThreads loads any persisted assignments. A store that fails to load is
// reported but not fatal: starting everyone fresh beats refusing to run.
func NewThreads(opts ThreadsOptions) *Threads {
	t := &Threads{
		byPeer: map[string]string{},
		store:  opts.Store,
		newID:  opts.NewID,
		logf:   opts.Logf,
	}
	if t.newID == nil {
		t.newID = newThreadID
	}
	if t.store != nil {
		state, err := t.store.Load()
		if err != nil {
			t.log("wechat: thread state unreadable, starting fresh: %v", err)
		} else if state != nil {
			t.byPeer = state
		}
	}
	return t
}

func (t *Threads) log(format string, args ...any) {
	if t.logf != nil {
		t.logf(format, args...)
	}
}

// newThreadID is time-ordered so logs and stored rows sort naturally, with
// random suffix so two resets in the same millisecond cannot collide.
func newThreadID() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	return strconv.FormatInt(time.Now().UnixMilli(), 10) + "-" + hex.EncodeToString(buf[:])
}

// Current returns the peer's thread id, minting one on first contact.
func (t *Threads) Current(peer string) string {
	t.mu.Lock()
	id, ok := t.byPeer[peer]
	if !ok {
		id = t.newID()
		t.byPeer[peer] = id
	}
	snapshot := cloneMap(t.byPeer)
	t.mu.Unlock()
	if !ok {
		t.persist(snapshot)
	}
	return id
}

// Reset abandons the peer's current thread and returns the new id.
func (t *Threads) Reset(peer string) string {
	t.mu.Lock()
	id := t.newID()
	t.byPeer[peer] = id
	snapshot := cloneMap(t.byPeer)
	t.mu.Unlock()
	t.persist(snapshot)
	return id
}

func (t *Threads) persist(state map[string]string) {
	if t.store == nil {
		return
	}
	if err := t.store.Save(state); err != nil {
		// Continuity is a convenience; a failed write must not drop a message.
		t.log("wechat: thread state not persisted: %v", err)
	}
}

// Routed is the outcome of feeding one inbound message through the tracker.
type Routed struct {
	// ThreadID is the conversation the message belongs to, already rotated if
	// the message started a new one.
	ThreadID string
	// Prompt is the text to hand to the agent. Empty when the user sent a
	// bare /new with nothing after it — there is nothing to answer yet.
	Prompt string
	// Reset reports that this message opened a new conversation.
	Reset bool
}

// Route resolves which thread a message belongs to and strips any recognised
// control instruction from the text.
//
// A bare /new rotates the thread and yields no prompt; "/new <text>" rotates
// and carries <text> into the new conversation, so a user can start over and
// ask in one message. Unrecognised slash words are left untouched and reach
// the agent verbatim — the agent CLIs have their own slash commands.
func (t *Threads) Route(peer, text string) Routed {
	if cmd, ok := ParseCommand(text); ok && cmd.Name == CommandNew {
		return Routed{ThreadID: t.Reset(peer), Prompt: cmd.Rest, Reset: true}
	}
	return Routed{ThreadID: t.Current(peer), Prompt: text}
}
