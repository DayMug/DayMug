package storetest

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/DayMug/DayMug/backend/internal/store"
)

func (m *Fake) liveConversationLocked(id string) bool {
	for _, c := range m.Conversations {
		if c.ID == id {
			return true
		}
	}
	return false
}

func sortScheduledPrompts(prompts []store.ScheduledPrompt) {
	sort.SliceStable(prompts, func(i, j int) bool {
		if !prompts[i].SendAt.Equal(prompts[j].SendAt) {
			return prompts[i].SendAt.Before(prompts[j].SendAt)
		}
		return prompts[i].CreatedAt.Before(prompts[j].CreatedAt)
	})
}

func (m *Fake) CreateScheduledPrompt(_ context.Context, p store.ScheduledPrompt) (store.ScheduledPrompt, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	if !m.liveConversationLocked(p.ConversationID) {
		return store.ScheduledPrompt{}, fmt.Errorf("FOREIGN KEY constraint failed")
	}
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	for _, existing := range m.ScheduledPrompts {
		if existing.ID == p.ID {
			return store.ScheduledPrompt{}, fmt.Errorf("UNIQUE constraint failed: scheduled_prompts.id")
		}
	}
	p.SendAt = p.SendAt.UTC().Truncate(time.Second)
	p.CreatedAt = fakeNow()
	m.ScheduledPrompts = append(m.ScheduledPrompts, p)
	return p, nil
}

func (m *Fake) ListScheduledPrompts(_ context.Context, conversationID string) ([]store.ScheduledPrompt, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	var out []store.ScheduledPrompt
	for _, p := range m.ScheduledPrompts {
		if p.ConversationID == conversationID {
			out = append(out, p)
		}
	}
	sortScheduledPrompts(out)
	return out, nil
}

func (m *Fake) ListDueScheduledPrompts(_ context.Context, now time.Time) ([]store.ScheduledPrompt, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	cutoff := now.UTC().Truncate(time.Second)
	var out []store.ScheduledPrompt
	for _, p := range m.ScheduledPrompts {
		if !p.SendAt.After(cutoff) && m.liveConversationLocked(p.ConversationID) {
			out = append(out, p)
		}
	}
	sortScheduledPrompts(out)
	return out, nil
}

func (m *Fake) NextScheduledPromptTime(_ context.Context) (time.Time, bool, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	var (
		next  time.Time
		found bool
	)
	for _, p := range m.ScheduledPrompts {
		if !m.liveConversationLocked(p.ConversationID) {
			continue
		}
		if !found || p.SendAt.Before(next) {
			next, found = p.SendAt, true
		}
	}
	return next, found, nil
}

func (m *Fake) DeleteScheduledPrompt(_ context.Context, conversationID, id string) (store.ScheduledPrompt, error) {
	m.QueueMu.Lock()
	defer m.QueueMu.Unlock()
	for i, p := range m.ScheduledPrompts {
		if p.ID == id && p.ConversationID == conversationID {
			m.ScheduledPrompts = append(m.ScheduledPrompts[:i], m.ScheduledPrompts[i+1:]...)
			return p, nil
		}
	}
	return store.ScheduledPrompt{}, store.ErrNotFound
}
