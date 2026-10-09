package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// PromptIntake takes one web-chat prompt from the moment it is submitted to
// the moment it is either queued for the conversation's worker or steered
// into the turn already running. The WebSocket handler only renders frames
// (through the submission's callbacks); everything that touches the store, the
// dispatcher or the running job lives here.
type PromptIntake struct {
	Store       store.Store
	Dispatcher  *Dispatcher
	Broadcaster *Broadcaster
	UserHub     *UserHub
	Drainer     *Drainer
	Pause       *PauseGate
	Persist     *MessagePersister
}

// PromptSubmission is one prompt as the transport received it.
type PromptSubmission struct {
	ConversationID string
	// UserID is the user-hub room whose activity view is refreshed when the
	// prompt is held by an operator pause.
	UserID  string
	Content string
	// Steer asks to append the prompt to the live turn instead of queueing it
	// behind that turn. Only an explicit composer action sets it.
	Steer bool
	// AttachmentMetadata renders the client's attachment echo into persisted
	// message metadata once the conversation's agent and the agent's home
	// owner are known. It is transport-specific (it builds the read URLs the
	// browser loads), so the caller supplies it. Optional.
	AttachmentMetadata func(agentID, homeOwnerID string) (json.RawMessage, error)
	// OnQueued runs once the prompt row is durable and before any worker or
	// steer can act on it, so the transport's echo and acknowledgement always
	// precede the turn's own events. Optional.
	OnQueued func(saved store.Message)
	// OnStaleSteer runs when an explicit steer found no live turn to join.
	// The prompt stays queued; the transport tells the client its "running"
	// snapshot was stale. Called before the worker is kicked. Optional.
	OnStaleSteer func(err error)
}

// Submit persists the prompt as pending, then either steers it into the
// running turn or kicks the conversation's worker. Errors carry the client-
// facing message; nothing has been persisted when one is returned.
func (in *PromptIntake) Submit(ctx context.Context, sub PromptSubmission) (store.Message, error) {
	if in.Dispatcher == nil {
		return store.Message{}, NewServiceError(http.StatusServiceUnavailable, "prompt dispatcher unavailable", nil)
	}
	if sub.ConversationID == "" {
		return store.Message{}, BadRequest("init required before input")
	}
	var conversation store.Conversation
	if in.Store != nil {
		var err error
		if conversation, err = in.Store.GetConversation(ctx, sub.ConversationID); err != nil {
			return store.Message{}, NotFound("conversation not found")
		}
	}
	metadata, agentID, homeRoot, err := in.attachmentMetadata(ctx, conversation, sub)
	if err != nil {
		return store.Message{}, BadRequest("invalid attachment metadata")
	}
	content := appendWebAttachmentRefs(sub.Content, metadata, agentID, homeRoot)

	// The row is persisted as 'pending' immediately (so a browser close keeps
	// the message in history); the worker is kicked only after OnQueued, so
	// peer tabs always see the prompt before the worker's prompt_started /
	// streaming events. Without this ordering the dispatcher's broadcast (from
	// a separate goroutine) can beat the transport's echo and peer tabs render
	// the reply before the prompt that triggered it.
	enqueueCtx, enqueueCancel := context.WithTimeout(ctx, 10*time.Second)
	var saved store.Message
	if len(metadata) > 0 {
		saved, err = in.Dispatcher.SavePromptWithMetadata(enqueueCtx, sub.ConversationID, content, metadata)
	} else {
		saved, err = in.Dispatcher.SavePrompt(enqueueCtx, sub.ConversationID, content)
	}
	enqueueCancel()
	if err != nil {
		log.Printf("[terminal] enqueue conv=%s: %v", sub.ConversationID, err)
		return store.Message{}, Internal("failed to enqueue prompt", err)
	}
	if sub.OnQueued != nil {
		sub.OnQueued(saved)
	}

	paused := in.Pause.Paused()
	if paused {
		BroadcastConversationActivity(context.Background(), in.UserHub, in.Drainer, in.Store, sub.UserID)
	}
	// Try to auto-summarize now that a fresh user prompt has landed. The
	// generator may decide the signal is too thin and return an empty title;
	// it retries after the assistant reply.
	if in.Persist != nil {
		go in.Persist.MaybeAutoTitle(sub.ConversationID)
	}

	draining := in.Drainer.IsDraining()
	steered := false
	if sub.Steer && !paused && !draining && in.Broadcaster != nil {
		steered = in.steer(ctx, sub, saved)
	}
	if !steered && !draining {
		in.Dispatcher.KickWorker(sub.ConversationID)
	}
	return saved, nil
}

// attachmentMetadata needs both ids: the agent names the uploads scope a path
// must sit in, its owner anchors that scope and serves the file back. A lookup
// failure drops both the metadata and the server-side prompt refs; the prompt
// itself still goes through.
func (in *PromptIntake) attachmentMetadata(
	ctx context.Context,
	conversation store.Conversation,
	sub PromptSubmission,
) (json.RawMessage, string, string, error) {
	if sub.AttachmentMetadata == nil {
		return nil, "", "", nil
	}
	agentID := conversation.UserID
	homeOwnerID := ""
	homeRoot := ""
	if in.Store != nil && conversation.UserID != "" {
		if agentUser, err := in.Store.GetUser(ctx, conversation.UserID); err == nil {
			if homeOwner, ownerErr := AgentHomeOwner(ctx, in.Store, agentUser); ownerErr == nil {
				homeOwnerID = homeOwner.ID
				homeRoot = homeOwner.WorkDir
			}
		}
	}
	metadata, err := sub.AttachmentMetadata(agentID, homeOwnerID)
	return metadata, agentID, homeRoot, err
}

// appendWebAttachmentRefs makes the server authoritative for the part of an
// upload prompt the agent consumes. Browsers still append refs for immediate
// feedback, but a stale client or a recalled attachment can omit them while
// sending valid attachment metadata. Without this fallback the transcript
// renders an image card even though the provider receives text only and has no
// path it can open.
func appendWebAttachmentRefs(content string, metadata json.RawMessage, agentID, homeRoot string) string {
	if len(metadata) == 0 || agentID == "" || !filepath.IsAbs(homeRoot) {
		return content
	}
	var envelope struct {
		Attachments []struct {
			Path string `json:"path"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(metadata, &envelope); err != nil {
		return content
	}
	uploadsRel, err := AgentUploadsDir(agentID)
	if err != nil {
		return content
	}
	uploadsRoot := DaymugPath(homeRoot, uploadsRel)
	refs := make([]string, 0, len(envelope.Attachments))
	seen := make(map[string]struct{}, len(envelope.Attachments))
	for _, attachment := range envelope.Attachments {
		clean := path.Clean(filepath.ToSlash(strings.TrimSpace(attachment.Path)))
		if path.Dir(clean) != uploadsRel || path.Base(clean) == "." {
			continue
		}
		ref := DaymugPath(homeRoot, clean)
		if within, pathErr := PathWithin(uploadsRoot, ref); pathErr != nil || !within {
			continue
		}
		if strings.Contains(content, ref) {
			continue
		}
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	if len(refs) == 0 {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return strings.Join(refs, "\n")
	}
	return strings.TrimRight(content, "\n") + "\n\n" + strings.Join(refs, "\n")
}

// steer appends a persisted prompt to the live turn. Regular sends always
// preserve FIFO ordering; steering is an explicit composer action because it
// changes the task that is already running. The prompt was persisted before
// this point, so a completion race or a backend that cannot steer safely falls
// back to the same durable queue.
func (in *PromptIntake) steer(ctx context.Context, sub PromptSubmission, saved store.Message) bool {
	steerCtx, steerCancel := context.WithTimeout(ctx, 10*time.Second)
	attempted, steerErr := in.Broadcaster.SteerJob(steerCtx, sub.ConversationID, saved.ID, saved.Content)
	steerCancel()
	if attempted && steerErr == nil {
		markCtx, markCancel := context.WithTimeout(context.Background(), 10*time.Second)
		markErr := in.Dispatcher.CompleteSteeredPrompt(markCtx, saved.ID)
		markCancel()
		if markErr != nil {
			log.Printf("[terminal] mark steered prompt done conv=%s id=%s: %v", sub.ConversationID, saved.ID, markErr)
		}
		if data, err := json.Marshal(ServerMessage{
			Type:           "prompt_started",
			ConversationID: sub.ConversationID,
			MessageID:      saved.ID,
		}); err == nil {
			in.Broadcaster.Broadcast(sub.ConversationID, data)
		}
		return true
	}
	if attempted && steerErr != nil {
		log.Printf("[terminal] steer rejected conv=%s id=%s: %v; keeping prompt queued", sub.ConversationID, saved.ID, steerErr)
		// The client chose Insert because its last snapshot said a turn was
		// live. If that boundary disappeared before the control reached the
		// provider, the transport makes the stale state visible so the client
		// can leave its running state. The durable prompt stays queued.
		if errors.Is(steerErr, agent.ErrNoActiveTurn) && sub.OnStaleSteer != nil {
			sub.OnStaleSteer(steerErr)
		}
	}
	return false
}
