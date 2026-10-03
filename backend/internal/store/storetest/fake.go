// Package storetest provides an in-memory store.Store implementation shared by
// tests across packages. It is a pure fake: it depends only on internal/store
// types and holds no knowledge of any HTTP handler or service wiring.
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// Fake implements the full store.Store surface.
var _ store.Store = (*Fake)(nil)

// New returns a Fake with its maps initialised.
func New() *Fake {
	return &Fake{
		Messages: make(map[string][]store.Message),
		Sessions: make(map[string]store.Session),
	}
}

type Fake struct {
	// queueMu serialises access to messages from the dispatcher's worker
	// goroutines (claim/mark-done) and the WS handler's cancel goroutine
	// (delete-pending). Production SQLiteStore relies on per-statement
	// transactions for the same property; the mock has no such isolation
	// so we add an explicit lock here. Without it, the cancel-while-
	// processing test races on QueueStatus reads/writes and can see a
	// "processing" row as still "pending".
	QueueMu       sync.Mutex
	Users         []store.User
	Bots          []store.Bot
	Conversations []store.Conversation
	// Attention mirrors conversations.attention, keyed by conversation id.
	Attention map[string]string
	// SuspendedQuestions mirrors conversations.suspended_question.
	SuspendedQuestions map[string]string
	// RotatedFrom mirrors conversations.rotated_from.
	RotatedFrom map[string]string
	Messages    map[string][]store.Message
	// deletedMessages keeps a deleted conversation's transcript, mirroring
	// the soft delete that leaves message rows on disk.
	deletedMessages map[string][]store.Message
	Sessions        map[string]store.Session
	CreateErr       error
	DeleteErr       error
	// UpdateErr, when set, makes UpdateUser fail without touching Users —
	// used to pin the ordering of writes that must not run before the row
	// update commits (e.g. the CLAUDE.md disk sync).
	UpdateErr   error
	BackendName string // override Backend() for tests; "" → "mock"
	OptimizeFn  func() (store.DBOptimizeResult, error)
	DBSize      int64
	// claimErrFn is consulted on every ClaimPendingPromptByID call; if it
	// returns non-nil that error is surfaced to the caller and the row is
	// left at 'pending'. Lets tests simulate transient sqlite contention
	// (e.g. SQLITE_BUSY) without needing a real DB.
	ClaimErrFn func(messageID string) error
	// promptRecoveries mirrors messages.recovery_count: how many startup
	// recovery passes found the prompt mid-run. Guarded by QueueMu.
	promptRecoveries map[string]int
	// tokenUsage backs the AddTokenUsage / AggregateTokenUsage path with
	// a simple in-memory aggregate keyed by (user_id, model, day=today).
	TokenUsage []store.TokenUsageRecord
	// claudeUsageCheckpoints mirrors SQLite's durable cumulative snapshots.
	// Tests that exercise SQLite's metadata bootstrap use the real store.
	claudeUsageCheckpoints map[string]fakeClaudeUsageCheckpoint
	claudeUsageEvents      map[string]struct{}
	// appSettings backs GetAppSetting / SetAppSetting; nil-safe — the
	// helpers lazy-init on first write.
	AppSettings map[string]string
	// botThreads backs GetBotThread / UpsertBotThread, keyed by
	// platform|channel|thread; nil-safe — lazy-init on first write.
	BotThreads map[string]store.BotThread
	// ThreadCases backs the case-file store, keyed by channel|thread. No
	// platform or agent in the key: one IM thread has exactly one case, which
	// is what lets two agents hand work over through it.
	ThreadCases map[string]store.ThreadCase
	// ThreadCaseHistory holds every revision written, newest last, under the
	// same key.
	ThreadCaseHistory map[string][]store.ThreadCase
	// SaveThreadCaseErr makes every case save fail, which is how the recovery
	// path for a dropped save is exercised.
	SaveThreadCaseErr error
	// CronJobs backs CronStore in insertion order.
	CronJobs []store.CronJob
	// MarketplaceApps backs MarketplaceStore in insertion order.
	MarketplaceApps []store.MarketplaceApp
	// UsageEvents backs the attributed usage ledger that RecordUsageEvent
	// writes and QueryUsageInsights reads. Tests may seed it directly.
	UsageEvents []store.UsageEvent
}

type fakeClaudeUsageCheckpoint struct {
	totalCost float64
	perModel  map[string]store.ClaudeModelUsage
	seen      bool
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

func (m *Fake) Init() error { return nil }

func (m *Fake) CreateUser(_ context.Context, user store.User) error {
	if user.Username != "" {
		user.RoleDefinition = ""
		user.McpConfig = ""
		user.ClaudeMdContent = ""
		user.ManageClaudeMd = false
		user.Archived = false
	}
	m.Users = append(m.Users, user)
	return nil
}

// CreateFirstAdmin mirrors SQLiteStore: refuse once a human row
// exists, otherwise append both rows. Fake methods are not otherwise locked;
// the check and the appends happen without yielding, which is what the
// single-goroutine tests that use it need.
func (m *Fake) CreateFirstAdmin(ctx context.Context, admin, agent store.User) error {
	if admin.Username == "" || agent.OwnerID != admin.ID {
		return errors.New("CreateFirstAdmin: admin needs a username and agent must be owned by it")
	}
	for _, u := range m.Users {
		if u.Username != "" {
			return store.ErrSetupClosed
		}
	}
	if err := m.CreateUser(ctx, admin); err != nil {
		return err
	}
	return m.CreateUser(ctx, agent)
}

func (m *Fake) CreateBot(_ context.Context, bot store.Bot) error {
	m.Bots = append(m.Bots, bot)
	return nil
}

func (m *Fake) GetBot(_ context.Context, id string) (store.Bot, error) {
	for _, bot := range m.Bots {
		if bot.ID == id {
			return bot, nil
		}
	}
	return store.Bot{}, store.ErrNotFound
}

func (m *Fake) ListBots(_ context.Context, agentID string) ([]store.Bot, error) {
	result := []store.Bot{}
	for _, bot := range m.Bots {
		if agentID == "" || bot.AgentID == agentID {
			result = append(result, bot)
		}
	}
	return result, nil
}

// UpdateBot assigns field-by-field rather than replacing the row wholesale:
// SQLiteStore.UpdateBot's statement omits created_at, so a caller passing a
// zero CreatedAt keeps the stored timestamp in production and must here too.
func (m *Fake) UpdateBot(_ context.Context, updated store.Bot) error {
	for i := range m.Bots {
		if m.Bots[i].ID == updated.ID && m.Bots[i].AgentID == updated.AgentID {
			m.Bots[i].Name = updated.Name
			m.Bots[i].Platform = updated.Platform
			m.Bots[i].Enabled = updated.Enabled
			m.Bots[i].Model = updated.Model
			m.Bots[i].MaxConversationDuration = updated.MaxConversationDuration
			m.Bots[i].BotToken = updated.BotToken
			m.Bots[i].BotAppToken = updated.BotAppToken
			m.Bots[i].BotAppID = updated.BotAppID
			m.Bots[i].BotAppSecret = updated.BotAppSecret
			m.Bots[i].Channels = updated.Channels
			m.Bots[i].UnconfiguredReply = updated.UnconfiguredReply
			m.Bots[i].UnauthorizedReply = updated.UnauthorizedReply
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) DeleteBot(_ context.Context, id, agentID string) error {
	for i := range m.Bots {
		if m.Bots[i].ID == id && m.Bots[i].AgentID == agentID {
			m.Bots = append(m.Bots[:i], m.Bots[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) GetUser(_ context.Context, id string) (store.User, error) {
	for _, u := range m.Users {
		if u.ID == id {
			if u.Username == "" && u.OwnerID != "" {
				for _, owner := range m.Users {
					if owner.ID == u.OwnerID {
						u.ProviderBindings = cloneStringMap(owner.ProviderBindings)
						u.ProviderAccounts = cloneStringSliceMap(owner.ProviderAccounts)
						break
					}
				}
			}
			return u, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (m *Fake) ListUsers(_ context.Context) ([]store.User, error) {
	return m.Users, nil
}

func (m *Fake) DeleteUser(_ context.Context, id string) error {
	for i, u := range m.Users {
		if u.ID == id {
			m.Users = append(m.Users[:i], m.Users[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) ReorderAgents(_ context.Context, ownerID string, ids []string) error {
	order := map[string]int{}
	for i, id := range ids {
		order[id] = i
	}
	for i := range m.Users {
		if m.Users[i].Username != "" || m.Users[i].Owner() != ownerID {
			continue
		}
		if pos, ok := order[m.Users[i].ID]; ok {
			m.Users[i].SortOrder = pos
		}
	}
	return nil
}

func (m *Fake) ArchiveUser(_ context.Context, id string) error {
	for i := range m.Users {
		if m.Users[i].ID == id {
			if m.Users[i].Archived {
				return store.ErrNotFound
			}
			m.Users[i].Archived = true
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) UnarchiveUser(_ context.Context, id string) error {
	for i := range m.Users {
		if m.Users[i].ID == id {
			if !m.Users[i].Archived {
				return store.ErrNotFound
			}
			m.Users[i].Archived = false
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) ListArchivedAgents(_ context.Context, ownerID string) ([]store.User, error) {
	var out []store.User
	for _, u := range m.Users {
		if u.Username == "" && u.Owner() == ownerID && u.Archived {
			out = append(out, u)
		}
	}
	return out, nil
}

// ConversationTitle reads a title under the lock the store writes it with.
// Auto-titling runs on a background goroutine, so an unlocked read from a test
// polling loop races with UpdateConversationTitle.
func (m *Fake) ConversationTitle(index int) string {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if index < 0 || index >= len(m.Conversations) {
		return ""
	}
	return m.Conversations[index].Title
}

func (m *Fake) UpdateConversationTitle(_ context.Context, id, title string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].Title = title
			return nil
		}
	}
	return nil
}

func (m *Fake) CreateConversation(ctx context.Context, id, title, userID, workDir, provider, model string) error {
	return m.CreateConversationRecord(ctx, store.NewConversation{
		ID: id, Title: title, UserID: userID, WorkDir: workDir, Provider: provider, Model: model,
	})
}

func (m *Fake) CreateConversationRecord(_ context.Context, c store.NewConversation) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.CreateErr != nil {
		return m.CreateErr
	}
	sessionID := c.SessionID
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	sourceType := c.SourceType
	if sourceType == "" {
		sourceType = "manual"
	}
	m.Conversations = append(m.Conversations, store.Conversation{
		ID:                   c.ID,
		Title:                c.Title,
		UserID:               c.UserID,
		WorkDir:              c.WorkDir,
		Provider:             c.Provider,
		Model:                c.Model,
		ThinkLevel:           c.ThinkLevel,
		AccountName:          c.AccountName,
		SessionID:            sessionID,
		NotificationsEnabled: !c.MuteNotifications,
		SourceType:           sourceType,
		CronJobID:            c.CronJobID,
	})
	return nil
}

func (m *Fake) GetConversation(_ context.Context, id string) (store.Conversation, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, c := range m.Conversations {
		if c.ID == id {
			return c, nil
		}
	}
	return store.Conversation{}, store.ErrNotFound
}

func (m *Fake) UpdateConversationWorkDir(_ context.Context, id, workDir string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].WorkDir = workDir
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetConversationAttention(_ context.Context, id, state string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.Attention == nil {
		m.Attention = map[string]string{}
	}
	if state == "" {
		delete(m.Attention, id)
	} else {
		m.Attention[id] = state
	}
	return nil
}

func (m *Fake) SetConversationSuspendedQuestion(_ context.Context, id, payload string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.SuspendedQuestions == nil {
		m.SuspendedQuestions = map[string]string{}
	}
	if payload == "" {
		delete(m.SuspendedQuestions, id)
	} else {
		m.SuspendedQuestions[id] = payload
	}
	return nil
}

func (m *Fake) GetConversationSuspendedQuestion(_ context.Context, id string) (string, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	return m.SuspendedQuestions[id], nil
}

func (m *Fake) SetConversationRotatedFrom(_ context.Context, id, parentID string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.RotatedFrom == nil {
		m.RotatedFrom = map[string]string{}
	}
	m.RotatedFrom[id] = parentID
	return nil
}

func (m *Fake) GetConversationRotatedFrom(_ context.Context, id string) (string, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	return m.RotatedFrom[id], nil
}

func (m *Fake) ListConversationAttention(_ context.Context, ownerID string) ([]store.ConversationAttention, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	owned := map[string]bool{}
	for _, u := range m.Users {
		if u.OwnerID == ownerID {
			owned[u.ID] = true
		}
	}
	out := []store.ConversationAttention{}
	for _, c := range m.Conversations {
		state := m.Attention[c.ID]
		if state == "" || !owned[c.UserID] {
			continue
		}
		out = append(out, store.ConversationAttention{
			ConversationID: c.ID, AgentID: c.UserID, Title: c.Title, State: state, At: c.UpdatedAt,
		})
	}
	return out, nil
}

func (m *Fake) UpdateConversationNotifications(_ context.Context, id string, enabled bool) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].NotificationsEnabled = enabled
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) UpdateConversationPinned(_ context.Context, id string, pinned bool) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].Pinned = pinned
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) EnableConversationShare(_ context.Context, id string) (store.Conversation, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			if m.Conversations[i].ShareToken == "" {
				m.Conversations[i].ShareToken = "share-" + id
			}
			m.Conversations[i].Shared = true
			return m.Conversations[i], nil
		}
	}
	return store.Conversation{}, store.ErrNotFound
}

func (m *Fake) DisableConversationShare(_ context.Context, id string) (store.Conversation, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].ShareToken = ""
			m.Conversations[i].Shared = false
			return m.Conversations[i], nil
		}
	}
	return store.Conversation{}, store.ErrNotFound
}

func (m *Fake) GetSharedConversation(_ context.Context, token string) (store.Conversation, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, c := range m.Conversations {
		if c.ShareToken == token && token != "" {
			c.Shared = true
			return c, nil
		}
	}
	return store.Conversation{}, store.ErrNotFound
}

func (m *Fake) ReorderPinnedConversations(_ context.Context, userID string, ids []string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	order := make(map[string]int, len(ids))
	for i, id := range ids {
		order[id] = i
	}
	for i := range m.Conversations {
		if m.Conversations[i].UserID != userID {
			continue
		}
		if pos, ok := order[m.Conversations[i].ID]; ok {
			m.Conversations[i].Pinned = true
			m.Conversations[i].PinOrder = pos
		} else if m.Conversations[i].Pinned {
			m.Conversations[i].Pinned = false
		}
	}
	return nil
}

func (m *Fake) UpdateConversationModel(_ context.Context, id, provider, model string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].Provider = provider
			m.Conversations[i].Model = model
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) UpdateConversationAccount(_ context.Context, id, account string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].AccountName = account
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) UpdateConversationThinkLevel(_ context.Context, id, level string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].ThinkLevel = level
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) UpdateConversationContextUsage(_ context.Context, id, payload string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].LastContextUsage = payload
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) ResetConversationSession(_ context.Context, id string) (string, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			// Tests assert on the new ID — keep it deterministic by
			// deriving from the old one rather than calling uuid.New.
			next := c.SessionID + "-rotated"
			m.Conversations[i].SessionID = next
			m.Conversations[i].LastContextUsage = ""
			return next, nil
		}
	}
	return "", store.ErrNotFound
}

func (m *Fake) SetSessionID(_ context.Context, id, newSessionID string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations[i].SessionID = newSessionID
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) HasUserMessages(_ context.Context, conversationID string) (bool, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, msg := range m.Messages[conversationID] {
		if msg.Role == "user" {
			return true, nil
		}
	}
	return false, nil
}

func (m *Fake) CountUserMessages(_ context.Context, conversationID string) (int, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	n := 0
	for _, msg := range m.Messages[conversationID] {
		if msg.Role == "user" {
			n++
		}
	}
	return n, nil
}

// ListConversations (and ListConversationsPage) knowingly diverge from SQLite:
// they ignore userID and return seed order instead of the pinned/updated_at
// sort. Handler tests seed conversations without an owner and rely on that, so
// the conformance suite does not cover listing yet.
func (m *Fake) ListConversations(_ context.Context, _ string) ([]store.Conversation, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	out := make([]store.Conversation, 0, len(m.Conversations))
	out = append(out, m.Conversations...)
	return out, nil
}

// ListConversationsPage mirrors SQLiteStore's window arithmetic (clamp, skip,
// over-read by one to answer HasMore) so handler tests exercise the same
// boundary behaviour they would hit against the real store.
func (m *Fake) ListConversationsPage(_ context.Context, _ string, query store.ConversationQuery) (store.ConversationPage, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()

	limit := query.Limit
	if limit <= 0 || limit > store.ConversationListHardCap {
		limit = store.ConversationListHardCap
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	if offset >= len(m.Conversations) {
		return store.ConversationPage{Conversations: []store.Conversation{}}, nil
	}
	window := m.Conversations[offset:]
	hasMore := len(window) > limit
	if hasMore {
		window = window[:limit]
	}
	out := make([]store.Conversation, 0, len(window))
	out = append(out, window...)
	return store.ConversationPage{Conversations: out, HasMore: hasMore}, nil
}

func (m *Fake) DeleteConversation(_ context.Context, id string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	for i, c := range m.Conversations {
		if c.ID == id {
			m.Conversations = append(m.Conversations[:i], m.Conversations[i+1:]...)
			m.retireMessagesLocked(id)
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) DeleteConversationsUpdatedBefore(_ context.Context, userID string, before time.Time) ([]string, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.DeleteErr != nil {
		return nil, m.DeleteErr
	}
	ids := make([]string, 0)
	kept := m.Conversations[:0]
	for _, conversation := range m.Conversations {
		if conversation.UserID == userID && conversation.UpdatedAt.Before(before) {
			ids = append(ids, conversation.ID)
			m.retireMessagesLocked(conversation.ID)
			continue
		}
		kept = append(kept, conversation)
	}
	m.Conversations = kept
	return ids, nil
}

func (m *Fake) retireMessagesLocked(id string) {
	if msgs, ok := m.Messages[id]; ok {
		if m.deletedMessages == nil {
			m.deletedMessages = make(map[string][]store.Message)
		}
		m.deletedMessages[id] = msgs
	}
	delete(m.Messages, id)
}

func (m *Fake) ExclusiveAttachmentPaths(_ context.Context, conversationID string) ([]string, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	msgs, ok := m.Messages[conversationID]
	if !ok {
		msgs = m.deletedMessages[conversationID]
	}
	live := make(map[string]bool, len(m.Conversations))
	for _, c := range m.Conversations {
		live[c.ID] = true
	}
	var paths []string
	seen := map[string]bool{}
	for _, msg := range msgs {
		var meta struct {
			Attachments []struct {
				Path string `json:"path"`
			} `json:"attachments"`
		}
		if len(msg.Metadata) == 0 || json.Unmarshal(msg.Metadata, &meta) != nil {
			continue
		}
		for _, a := range meta.Attachments {
			if a.Path == "" || seen[a.Path] || m.attachmentSharedLocked(conversationID, a.Path, live) {
				continue
			}
			seen[a.Path] = true
			paths = append(paths, a.Path)
		}
	}
	return paths, nil
}

func (m *Fake) attachmentSharedLocked(conversationID, path string, live map[string]bool) bool {
	for id, msgs := range m.Messages {
		if id == conversationID || !live[id] {
			continue
		}
		for _, msg := range msgs {
			if strings.Contains(string(msg.Metadata), path) {
				return true
			}
		}
	}
	return false
}

func (m *Fake) SaveMessage(_ context.Context, msg store.Message) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	m.Messages[msg.ConversationID] = append(m.Messages[msg.ConversationID], msg)
	return nil
}

func (m *Fake) SaveImportedMessage(_ context.Context, msg store.Message, createdAt time.Time) (bool, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	// Mirror the unique (conversation_id, source_id) index: a repeated
	// source_id is a no-op insert.
	for _, existing := range m.Messages[msg.ConversationID] {
		if existing.SourceID != "" && existing.SourceID == msg.SourceID {
			return false, nil
		}
	}
	msg.CreatedAt = createdAt
	m.Messages[msg.ConversationID] = append(m.Messages[msg.ConversationID], msg)
	return true, nil
}

func (m *Fake) LatestMessageTime(_ context.Context, conversationID string) (time.Time, bool, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	var latest time.Time
	found := false
	for _, msg := range m.Messages[conversationID] {
		if !found || msg.CreatedAt.After(latest) {
			latest = msg.CreatedAt
			found = true
		}
	}
	return latest, found, nil
}

func (m *Fake) EnqueuePrompt(_ context.Context, conversationID, content string) (store.Message, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	msg := store.Message{
		ID:             uuid.New().String(),
		ConversationID: conversationID,
		Role:           "user",
		Content:        content,
		QueueStatus:    "pending",
	}
	m.Messages[conversationID] = append(m.Messages[conversationID], msg)
	return msg, nil
}

func (m *Fake) PeekNextPendingPrompt(_ context.Context, conversationID string) (store.Message, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, msg := range m.Messages[conversationID] {
		if msg.QueueStatus == "pending" {
			return msg, nil
		}
	}
	return store.Message{}, store.ErrNotFound
}

func (m *Fake) ClaimPendingPromptByID(_ context.Context, messageID string) (store.Message, error) {
	m.QueueMu.Lock()
	fn := m.ClaimErrFn
	m.QueueMu.Unlock()
	if fn != nil {
		if err := fn(messageID); err != nil {
			return store.Message{}, err
		}
	}
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for convID, msgs := range m.Messages {
		for i, msg := range msgs {
			if msg.ID != messageID {
				continue
			}
			if msg.QueueStatus != "pending" {
				return store.Message{}, store.ErrNotFound
			}
			// SQLite re-inserts the claimed row so it sorts after the reply
			// saved since it was enqueued; move it to the end to match.
			msg.QueueStatus = "processing"
			reordered := make([]store.Message, 0, len(msgs))
			reordered = append(reordered, msgs[:i]...)
			reordered = append(reordered, msgs[i+1:]...)
			m.Messages[convID] = append(reordered, msg)
			return msg, nil
		}
	}
	return store.Message{}, store.ErrNotFound
}

func (m *Fake) MarkPromptDone(_ context.Context, messageID string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for convID, msgs := range m.Messages {
		for i, msg := range msgs {
			if msg.ID == messageID {
				m.Messages[convID][i].QueueStatus = ""
				return nil
			}
		}
	}
	return nil
}

func (m *Fake) DeletePendingPrompts(_ context.Context, conversationID string) ([]store.Message, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	msgs := m.Messages[conversationID]
	var deleted []store.Message
	var kept []store.Message
	for _, msg := range msgs {
		if msg.QueueStatus == "pending" {
			deleted = append(deleted, msg)
			continue
		}
		kept = append(kept, msg)
	}
	m.Messages[conversationID] = kept
	return deleted, nil
}

func (m *Fake) DeletePendingPrompt(_ context.Context, messageID string) (store.Message, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for convID, msgs := range m.Messages {
		for i, msg := range msgs {
			if msg.ID != messageID {
				continue
			}
			if msg.QueueStatus != "pending" {
				return store.Message{}, store.ErrNotFound
			}
			m.Messages[convID] = append(msgs[:i], msgs[i+1:]...)
			return msg, nil
		}
	}
	return store.Message{}, store.ErrNotFound
}

func (m *Fake) ClearPoolQueuedPrompts(_ context.Context) (int, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	cleared := 0
	for convID, msgs := range m.Messages {
		for i, msg := range msgs {
			if msg.QueueStatus == store.QueueStatusPoolQueued {
				m.Messages[convID][i].QueueStatus = ""
				cleared++
			}
		}
	}
	return cleared, nil
}

// fakeMaxPromptRecoveries mirrors store.maxPromptRecoveries (unexported);
// TestResetProcessingToPendingAbandonsPoisonPrompt pins the two together.
const fakeMaxPromptRecoveries = 3

func (m *Fake) ResetProcessingToPending(_ context.Context) (recovered, abandoned int, err error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for convID, msgs := range m.Messages {
		for i, msg := range msgs {
			if msg.QueueStatus != "processing" {
				continue
			}
			if m.promptRecoveries == nil {
				m.promptRecoveries = make(map[string]int)
			}
			m.promptRecoveries[msg.ID]++
			if m.promptRecoveries[msg.ID] >= fakeMaxPromptRecoveries {
				m.Messages[convID][i].QueueStatus = ""
				abandoned++
				continue
			}
			m.Messages[convID][i].QueueStatus = "pending"
			recovered++
		}
	}
	return recovered, abandoned, nil
}

func (m *Fake) ListConversationsWithPending(_ context.Context) ([]string, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	seen := make(map[string]struct{})
	var ids []string
	for convID, msgs := range m.Messages {
		for _, msg := range msgs {
			if msg.QueueStatus != "" {
				if _, ok := seen[convID]; !ok {
					seen[convID] = struct{}{}
					ids = append(ids, convID)
				}
				break
			}
		}
	}
	return ids, nil
}

// SnapshotMessages returns a copy of the messages slice for the given
// conversation under the queue lock so concurrent dispatcher goroutines
// can't race the test's assertion. Tests that want to inspect persisted
// rows after firing async work MUST use this rather than touching
// m.Messages directly.
func (m *Fake) SnapshotMessages(conversationID string) []store.Message {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	src := m.Messages[conversationID]
	out := make([]store.Message, len(src))
	copy(out, src)
	return out
}

func (m *Fake) ListMessages(_ context.Context, conversationID string, limit, offset int) ([]store.Message, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	src := m.Messages[conversationID]
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(src) {
		return nil, nil
	}
	end := offset + limit
	if end > len(src) {
		end = len(src)
	}
	src = src[offset:end]
	out := make([]store.Message, len(src))
	copy(out, src)
	return out, nil
}

func (m *Fake) ListMessagesAfter(_ context.Context, conversationID, lastMessageID string, limit int) ([]store.Message, error) {
	if conversationID == "" {
		return nil, nil
	}
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	all := m.Messages[conversationID]
	// Empty or unknown cursor returns the full conversation, mirroring
	// the SQLite implementation's rowid-based query (cursorRowid stays
	// 0 → WHERE rowid > 0 matches every row).
	idx := -1
	if lastMessageID != "" {
		for i, msg := range all {
			if msg.ID == lastMessageID {
				idx = i
				break
			}
		}
	}
	rest := all[idx+1:]
	if limit > 0 && len(rest) > limit {
		rest = rest[:limit]
	}
	out := make([]store.Message, len(rest))
	copy(out, rest)
	return out, nil
}

func (m *Fake) ListMessagesBefore(_ context.Context, conversationID, beforeMessageID string, limit int) ([]store.Message, error) {
	if conversationID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	all := m.Messages[conversationID]
	end := len(all)
	if beforeMessageID != "" {
		for i, msg := range all {
			if msg.ID == beforeMessageID {
				end = i
				break
			}
		}
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	if start > end {
		return nil, nil
	}
	out := make([]store.Message, end-start)
	copy(out, all[start:end])
	return out, nil
}

// UpdateUser mirrors SQLiteStore.UpdateUser's column set exactly — see
// TestUpdateUserColumnSetMatchesSQLite. Fields the real UPDATE leaves alone
// (work_dir, env, bark_url, …) must not be assigned here:
// a Fake that persists more than production does lets a test assert
// persistence the real store never delivers.
func (m *Fake) UpdateUser(_ context.Context, user store.User) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	for i, u := range m.Users {
		if u.ID == user.ID {
			m.Users[i].Name = user.Name
			m.Users[i].Avatar = user.Avatar
			if u.Username == "" {
				m.Users[i].RoleDefinition = user.RoleDefinition
				m.Users[i].McpConfig = user.McpConfig
				m.Users[i].ClaudeMdContent = user.ClaudeMdContent
				m.Users[i].ManageClaudeMd = user.ManageClaudeMd
				m.Users[i].ThinkLevel = user.ThinkLevel
				m.Users[i].CaseMode = user.CaseMode
			}
			m.Users[i].DefaultModel = user.DefaultModel
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) ClearMessages(_ context.Context, conversationID string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	delete(m.Messages, conversationID)
	for i, c := range m.Conversations {
		if c.ID == conversationID {
			m.Conversations[i].SessionID = uuid.New().String()
			break
		}
	}
	return nil
}

func (m *Fake) GetSessionID(_ context.Context, conversationID string) (string, error) {
	for _, c := range m.Conversations {
		if c.ID == conversationID {
			return c.SessionID, nil
		}
	}
	return "", store.ErrNotFound
}

func (m *Fake) GetUserByUsername(_ context.Context, username string) (store.User, error) {
	for _, u := range m.Users {
		if u.Username == username && username != "" {
			return u, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (m *Fake) GetUserByEmail(_ context.Context, email string) (store.User, error) {
	for _, u := range m.Users {
		if u.Email == email && email != "" && u.Username != "" {
			return u, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (m *Fake) GetOwner(ctx context.Context, user store.User) (store.User, error) {
	if user.Username != "" {
		return user, nil
	}
	if user.Email == "" {
		return store.User{}, store.ErrNotFound
	}
	return m.GetUserByEmail(ctx, user.Email)
}

func (m *Fake) SetUserPassword(_ context.Context, userID, hash string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].PasswordHash = hash
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserAdmin(_ context.Context, userID string, isAdmin bool) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].IsAdmin = isAdmin
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserDisabled(_ context.Context, userID string, disabled bool) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].Disabled = disabled
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserWorkDir(_ context.Context, userID, workDir string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].WorkDir = workDir
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) GetUserProviderBindings(_ context.Context, userID string) (map[string]string, error) {
	for _, u := range m.Users {
		if u.ID == userID {
			out := map[string]string{}
			for k, v := range u.ProviderBindings {
				out[k] = v
			}
			return out, nil
		}
	}
	return map[string]string{}, nil
}

func (m *Fake) GetUserProviderAccounts(_ context.Context, userID string) (map[string][]string, error) {
	for _, u := range m.Users {
		if u.ID == userID {
			out := map[string][]string{}
			for k, v := range u.ProviderAccounts {
				out[k] = append([]string(nil), v...)
			}
			return out, nil
		}
	}
	return map[string][]string{}, nil
}

func (m *Fake) SetUserProviderAccounts(_ context.Context, userID, providerType string, names []string, defaultName string) error {
	for i, u := range m.Users {
		if u.ID != userID {
			continue
		}
		if m.Users[i].ProviderAccounts == nil {
			m.Users[i].ProviderAccounts = map[string][]string{}
		}
		if m.Users[i].ProviderBindings == nil {
			m.Users[i].ProviderBindings = map[string]string{}
		}
		if len(names) == 0 {
			delete(m.Users[i].ProviderAccounts, providerType)
			delete(m.Users[i].ProviderBindings, providerType)
			return nil
		}
		def := defaultName
		if def == "" {
			def = names[0]
		}
		ordered := []string{def}
		for _, n := range names {
			if n != def {
				ordered = append(ordered, n)
			}
		}
		m.Users[i].ProviderAccounts[providerType] = ordered
		m.Users[i].ProviderBindings[providerType] = def
		return nil
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserProviderBinding(ctx context.Context, userID, providerType, providerName string) error {
	if providerName == "" {
		return m.DeleteUserProviderBinding(ctx, userID, providerType)
	}
	for i, u := range m.Users {
		if u.ID == userID {
			if m.Users[i].ProviderBindings == nil {
				m.Users[i].ProviderBindings = map[string]string{}
			}
			m.Users[i].ProviderBindings[providerType] = providerName
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) DeleteUserProviderBinding(_ context.Context, userID, providerType string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			if m.Users[i].ProviderBindings != nil {
				delete(m.Users[i].ProviderBindings, providerType)
				if len(m.Users[i].ProviderBindings) == 0 {
					m.Users[i].ProviderBindings = nil
				}
			}
			return nil
		}
	}
	return nil
}

func (m *Fake) SetUserEmail(_ context.Context, userID, email string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].Email = email
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserEnv(_ context.Context, userID, env string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].Env = env
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserDefaultModel(_ context.Context, userID, model string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].DefaultModel = model
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserSandboxMode(_ context.Context, userID, mode string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].SandboxMode = mode
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserBarkURL(_ context.Context, userID, barkURL string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].BarkURL = barkURL
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserPushDeerKey(_ context.Context, userID, key string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].PushDeerKey = key
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) SetUserNotificationChannel(_ context.Context, userID, channel string) error {
	for i, u := range m.Users {
		if u.ID == userID {
			m.Users[i].NotificationChannel = channel
			return nil
		}
	}
	return store.ErrNotFound
}

func (m *Fake) CreateSession(_ context.Context, sess store.Session) error {
	m.Sessions[sess.Token] = sess
	return nil
}

func (m *Fake) GetSession(_ context.Context, token string) (store.Session, error) {
	if token == "" {
		return store.Session{}, store.ErrNotFound
	}
	sess, ok := m.Sessions[token]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	// Mirror SQLiteStore.GetSession: an elapsed expiry reads as "not found", so
	// tests that seed an expired session can't pass against the fake while
	// failing against the real store. A zero ExpiresAt means never expires.
	if !sess.ExpiresAt.IsZero() && time.Now().After(sess.ExpiresAt) {
		return store.Session{}, store.ErrNotFound
	}
	return sess, nil
}

func (m *Fake) DeleteSession(_ context.Context, token string) error {
	delete(m.Sessions, token)
	return nil
}

func (m *Fake) DeleteExpiredSessions(_ context.Context) error { return nil }

func (m *Fake) Backend() string {
	if m.BackendName != "" {
		return m.BackendName
	}
	return "mock"
}

func (m *Fake) OptimizeDatabase(_ context.Context) (store.DBOptimizeResult, error) {
	if m.OptimizeFn != nil {
		return m.OptimizeFn()
	}
	return store.DBOptimizeResult{}, nil
}

func (m *Fake) DatabaseSize(_ context.Context) (int64, error) {
	return m.DBSize, nil
}

func (m *Fake) Close() error { return nil }

func (m *Fake) AddTokenUsage(_ context.Context, d store.TokenUsageDelta) error {
	if d.UserID == "" {
		return errors.New("token usage: user_id required")
	}
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	model := strings.TrimSpace(d.Model)
	if model == "" {
		model = "unknown"
	}
	// An unset day means "today" (UTC), as in SQLite, which keeps callers
	// that omit it inside the handler's default 30-day window.
	day := strings.TrimSpace(d.Day)
	if day == "" {
		day = time.Now().UTC().Format("2006-01-02")
	}
	for i, r := range m.TokenUsage {
		if r.UserID == d.UserID && r.Model == model && r.Day == day {
			m.TokenUsage[i].InputTokens += d.InputTokens
			m.TokenUsage[i].OutputTokens += d.OutputTokens
			m.TokenUsage[i].CacheReadInputTokens += d.CacheReadInputTokens
			m.TokenUsage[i].CacheCreationInputTokens += d.CacheCreationInputTokens
			m.TokenUsage[i].CostUSD += d.CostUSD
			m.TokenUsage[i].Turns++
			return nil
		}
	}
	m.TokenUsage = append(m.TokenUsage, store.TokenUsageRecord{
		UserID:                   d.UserID,
		Model:                    model,
		Day:                      day,
		InputTokens:              d.InputTokens,
		OutputTokens:             d.OutputTokens,
		CacheReadInputTokens:     d.CacheReadInputTokens,
		CacheCreationInputTokens: d.CacheCreationInputTokens,
		CostUSD:                  d.CostUSD,
		Turns:                    1,
	})
	return nil
}

var _ store.ClaudeUsageRecorder = (*Fake)(nil)

func (m *Fake) RecordClaudeTokenUsage(ctx context.Context, event store.ClaudeUsageEvent) (float64, error) {
	m.QueueMu.Lock()
	if m.claudeUsageCheckpoints == nil {
		m.claudeUsageCheckpoints = make(map[string]fakeClaudeUsageCheckpoint)
	}
	key := event.ConversationID + "\x00" + event.SessionID
	eventJSON, _ := json.Marshal(event)
	eventKey := key + "\x00" + string(eventJSON)
	if _, duplicate := m.claudeUsageEvents[eventKey]; duplicate {
		m.QueueMu.Unlock()
		return 0, nil
	}
	if m.claudeUsageEvents == nil {
		m.claudeUsageEvents = make(map[string]struct{})
	}
	m.claudeUsageEvents[eventKey] = struct{}{}
	previous := m.claudeUsageCheckpoints[key]
	if previous.perModel == nil {
		previous.perModel = make(map[string]store.ClaudeModelUsage)
	}
	deltas := make([]store.TokenUsageDelta, 0, len(event.PerModel))
	advanced := !previous.seen
	for model, current := range event.PerModel {
		prior, found := previous.perModel[model]
		reset := found && (current.InputTokens < prior.InputTokens ||
			current.OutputTokens < prior.OutputTokens || current.CostUSD < prior.CostUSD)
		delta := store.TokenUsageDelta{UserID: event.UserID, Model: model, Day: event.Day}
		if !found || reset {
			advanced = true
			delta.InputTokens, delta.OutputTokens, delta.CostUSD = current.InputTokens, current.OutputTokens, current.CostUSD
		} else {
			delta.InputTokens = current.InputTokens - prior.InputTokens
			delta.OutputTokens = current.OutputTokens - prior.OutputTokens
			delta.CostUSD = current.CostUSD - prior.CostUSD
		}
		if delta.InputTokens != 0 || delta.OutputTokens != 0 || delta.CostUSD != 0 {
			advanced = true
			deltas = append(deltas, delta)
		}
		previous.perModel[model] = current
	}
	if len(event.PerModel) == 0 {
		advanced = !previous.seen || event.TotalCostUSD != previous.totalCost
		if advanced {
			cost := event.TotalCostUSD - previous.totalCost
			if event.TotalCostUSD < previous.totalCost {
				cost = event.TotalCostUSD
			}
			deltas = append(deltas, store.TokenUsageDelta{
				UserID: event.UserID, Model: event.FallbackModel, Day: event.Day,
				InputTokens: event.InputTokens, OutputTokens: event.OutputTokens,
				CacheReadInputTokens:     event.CacheReadInputTokens,
				CacheCreationInputTokens: event.CacheCreationInputTokens,
				CostUSD:                  cost,
			})
		}
	} else if advanced && (event.CacheReadInputTokens != 0 || event.CacheCreationInputTokens != 0) {
		found := false
		for i := range deltas {
			if deltas[i].Model == event.FallbackModel {
				deltas[i].CacheReadInputTokens += event.CacheReadInputTokens
				deltas[i].CacheCreationInputTokens += event.CacheCreationInputTokens
				found = true
			}
		}
		if !found {
			deltas = append(deltas, store.TokenUsageDelta{
				UserID: event.UserID, Model: event.FallbackModel, Day: event.Day,
				CacheReadInputTokens:     event.CacheReadInputTokens,
				CacheCreationInputTokens: event.CacheCreationInputTokens,
			})
		}
	}
	previous.totalCost = event.TotalCostUSD
	previous.seen = true
	m.claudeUsageCheckpoints[key] = previous
	m.QueueMu.Unlock()
	var billed float64
	for _, delta := range deltas {
		if err := m.AddTokenUsage(ctx, delta); err != nil {
			return 0, err
		}
		billed += delta.CostUSD
	}
	return billed, nil
}

func (m *Fake) AggregateTokenUsage(_ context.Context, q store.TokenUsageQuery) ([]store.TokenUsageRecord, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	var out []store.TokenUsageRecord
	allow := func(userID string) bool {
		if len(q.UserIDs) > 0 {
			for _, id := range q.UserIDs {
				if id == userID {
					return true
				}
			}
			return false
		}
		if q.UserID != "" && userID != q.UserID {
			return false
		}
		return true
	}
	for _, r := range m.TokenUsage {
		if !allow(r.UserID) {
			continue
		}
		if q.Model != "" && r.Model != q.Model {
			continue
		}
		if q.StartUTC != "" && r.Day < q.StartUTC {
			continue
		}
		if q.EndUTC != "" && r.Day > q.EndUTC {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func (m *Fake) GetAppSetting(_ context.Context, key string) (string, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.AppSettings == nil {
		return "", nil
	}
	return m.AppSettings[key], nil
}

func (m *Fake) SetAppSetting(_ context.Context, key, value string) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.AppSettings == nil {
		m.AppSettings = make(map[string]string)
	}
	m.AppSettings[key] = value
	return nil
}

func (m *Fake) GetBotThread(_ context.Context, platform, channelID, threadID string) (store.BotThread, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if t, ok := m.BotThreads[platform+"|"+channelID+"|"+threadID]; ok {
		return t, nil
	}
	return store.BotThread{}, store.ErrNotFound
}

func (m *Fake) GetBotThreadByConversation(_ context.Context, conversationID string) (store.BotThread, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, thread := range m.BotThreads {
		if thread.ConversationID == conversationID {
			return thread, nil
		}
	}
	return store.BotThread{}, store.ErrNotFound
}

func (m *Fake) IsBotConversation(_ context.Context, conversationID string) (bool, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for _, thread := range m.BotThreads {
		if thread.ConversationID == conversationID {
			return true, nil
		}
	}
	return false, nil
}

func (m *Fake) UpsertBotThread(_ context.Context, t store.BotThread) error {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.BotThreads == nil {
		m.BotThreads = make(map[string]store.BotThread)
	}
	m.BotThreads[t.Platform+"|"+t.ChannelID+"|"+t.ThreadID] = t
	return nil
}

func (m *Fake) ListTokenUsageModels(_ context.Context) ([]string, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	seen := map[string]struct{}{}
	var out []string
	for _, r := range m.TokenUsage {
		if _, ok := seen[r.Model]; ok {
			continue
		}
		seen[r.Model] = struct{}{}
		out = append(out, r.Model)
	}
	sort.Strings(out)
	return out, nil
}

func threadCaseKey(channelID, threadID string) string { return channelID + "|" + threadID }

func (m *Fake) GetThreadCase(_ context.Context, channelID, threadID string) (store.ThreadCase, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if c, ok := m.ThreadCases[threadCaseKey(channelID, threadID)]; ok {
		return c, nil
	}
	// A thread with no case yet is the normal first turn, not ErrNotFound —
	// callers branch on the zero version, not on an error.
	return store.ThreadCase{ChannelID: channelID, ThreadID: threadID}, nil
}

func (m *Fake) SaveThreadCase(_ context.Context, channelID, threadID, doc, updatedBy string) (store.ThreadCase, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if m.SaveThreadCaseErr != nil {
		return store.ThreadCase{}, m.SaveThreadCaseErr
	}
	if m.ThreadCases == nil {
		m.ThreadCases = make(map[string]store.ThreadCase)
	}
	if m.ThreadCaseHistory == nil {
		m.ThreadCaseHistory = make(map[string][]store.ThreadCase)
	}
	key := threadCaseKey(channelID, threadID)
	next := m.ThreadCases[key].Version + 1
	c := store.ThreadCase{
		ChannelID: channelID, ThreadID: threadID, Doc: doc,
		Version: next, UpdatedBy: updatedBy, UpdatedAt: time.Now().UTC(),
	}
	m.ThreadCases[key] = c
	m.ThreadCaseHistory[key] = append(m.ThreadCaseHistory[key], c)
	return c, nil
}

func (m *Fake) LatestAssistantMessage(_ context.Context, conversationID string) (store.Message, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	msgs := m.Messages[conversationID]
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && msgs[i].Content != "" {
			return msgs[i], nil
		}
	}
	return store.Message{}, store.ErrNotFound
}

func (m *Fake) ListThreadCaseHistory(_ context.Context, channelID, threadID string, limit int) ([]store.ThreadCase, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if limit <= 0 {
		limit = 20
	}
	all := m.ThreadCaseHistory[threadCaseKey(channelID, threadID)]
	out := make([]store.ThreadCase, 0, len(all))
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, all[i])
	}
	return out, nil
}
