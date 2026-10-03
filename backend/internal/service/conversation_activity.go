package service

import (
	"context"
	"encoding/json"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// ConversationActivitySnapshot builds the authoritative global activity frame.
// ownerID identifies the signed-in requester; the returned jobs are not
// filtered by it because the chat rail is an intentionally global operations
// view. The conversation row remains the source of truth for the agent id.
func ConversationActivitySnapshot(ctx context.Context, drainer *Drainer, s store.Store, ownerID string) ServerMessage {
	msg := ServerMessage{
		Type:                   "conversation_activity",
		RunningAgentIDs:        []string{},
		RunningConversationIDs: []string{},
		RunningConversations:   []RunningConversationActivity{},
		QueuedConversations:    []RunningConversationActivity{},
		WaitingConversations:   []RunningConversationActivity{},
		FailedConversationIDs:  drainer.FailedConversationIDs(),
	}
	if drainer == nil || s == nil || ownerID == "" {
		return msg
	}

	agents := map[string]struct{}{}
	runningConversations := map[string]struct{}{}
	queuedConversations := map[string]struct{}{}
	waitingConversations := map[string]struct{}{}
	// Foreground jobs come first, so a conversation that has both a live
	// turn and parked background work is reported by the turn.
	jobs := drainer.Jobs()
	foreground := len(jobs)
	jobs = append(jobs, drainer.ResidentActivities()...)
	for i, job := range jobs {
		if job.ConversationID == "" ||
			(job.Status != JobStatusRunning && job.Status != JobStatusQueued && job.Status != JobStatusWaiting) {
			continue
		}
		conv, err := s.GetConversation(ctx, job.ConversationID)
		if err != nil || conv.UserID == "" {
			continue
		}
		agentName := conv.UserID
		if agent, err := s.GetUser(ctx, conv.UserID); err == nil && agent.Name != "" {
			agentName = agent.Name
		}
		ownerName := ""
		ownerUsername := job.Username
		if owner, err := s.GetUser(ctx, job.UserID); err == nil {
			ownerName = owner.Name
			if owner.Username != "" {
				ownerUsername = owner.Username
			}
		}
		activity := RunningConversationActivity{
			ConversationID: conv.ID,
			AgentID:        conv.UserID,
			AgentName:      agentName,
			OwnerName:      ownerName,
			OwnerUsername:  ownerUsername,
			AccountName:    job.AccountName,
			Model:          conv.Model,
			StartedAt:      job.StartedAt,
			Background:     i >= foreground,
		}
		if job.Status == JobStatusWaiting {
			if _, ok := waitingConversations[conv.ID]; ok {
				continue
			}
			waitingConversations[conv.ID] = struct{}{}
			msg.WaitingConversations = append(msg.WaitingConversations, activity)
			continue
		}
		if job.Status == JobStatusQueued {
			if _, ok := queuedConversations[conv.ID]; ok {
				continue
			}
			queuedConversations[conv.ID] = struct{}{}
			msg.QueuedConversations = append(msg.QueuedConversations, activity)
			continue
		}
		if _, ok := runningConversations[conv.ID]; ok {
			continue
		}
		runningConversations[conv.ID] = struct{}{}
		msg.RunningConversationIDs = append(msg.RunningConversationIDs, conv.ID)
		msg.RunningConversations = append(msg.RunningConversations, activity)
		if _, ok := agents[conv.UserID]; !ok {
			agents[conv.UserID] = struct{}{}
			msg.RunningAgentIDs = append(msg.RunningAgentIDs, conv.UserID)
		}
	}

	// A manually paused dispatcher deliberately leaves new prompts only in
	// the durable pending queue; they have not reached PromptRunner yet and
	// therefore have no DrainerJob. Include those conversations so the global
	// activity view still tells operators what is waiting while the pause is
	// active (and after a paused client reconnects).
	pendingConversationIDs, err := s.ListConversationsWithPending(ctx)
	if err != nil {
		return msg
	}
	for _, conversationID := range pendingConversationIDs {
		if _, ok := runningConversations[conversationID]; ok {
			continue
		}
		if _, ok := queuedConversations[conversationID]; ok {
			continue
		}
		prompt, err := s.PeekNextPendingPrompt(ctx, conversationID)
		if err != nil {
			continue
		}
		conv, err := s.GetConversation(ctx, conversationID)
		if err != nil || conv.UserID == "" {
			continue
		}
		agent, err := s.GetUser(ctx, conv.UserID)
		if err != nil {
			continue
		}
		agentName := agent.Name
		if agentName == "" {
			agentName = conv.UserID
		}
		owner := agent
		if agent.OwnerID != "" {
			if resolved, err := s.GetUser(ctx, agent.OwnerID); err == nil {
				owner = resolved
			}
		}
		accountName := conv.AccountName
		if accountName == "" && agent.ProviderBindings != nil {
			accountName = agent.ProviderBindings[conv.Provider]
		}
		msg.QueuedConversations = append(msg.QueuedConversations, RunningConversationActivity{
			ConversationID: conv.ID,
			AgentID:        conv.UserID,
			AgentName:      agentName,
			OwnerName:      owner.Name,
			OwnerUsername:  owner.Username,
			AccountName:    accountName,
			Model:          conv.Model,
			StartedAt:      prompt.CreatedAt,
		})
		queuedConversations[conversationID] = struct{}{}
	}
	return msg
}

// BroadcastConversationActivity fans the latest global snapshot to every
// connected WebSocket. Sending the full set makes concurrent conversations
// and dropped intermediate frames self-correcting on the next lifecycle change.
func BroadcastConversationActivity(ctx context.Context, hub *UserHub, drainer *Drainer, s store.Store, ownerID string) {
	if hub == nil || ownerID == "" {
		return
	}
	data, err := json.Marshal(ConversationActivitySnapshot(ctx, drainer, s, ownerID))
	if err == nil {
		hub.BroadcastAll(data)
	}
}
