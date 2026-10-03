package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// ErrSuspendedQuestionMismatch reports an answer aimed at a question other
// than the one a restart left unanswered.
var ErrSuspendedQuestionMismatch = errors.New("the user question is no longer active")

// SuspendedAnswerPrompt turns the answer to a question a restart left
// unanswered into the text of a new prompt. The provider process that asked is
// gone, so the answer cannot go back through the tool call; the resumed
// session sees the interrupted question followed by this message instead.
func SuspendedAnswerPrompt(payload, requestID string, answers map[string][]string) (string, error) {
	var req agent.UserQuestionRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return "", err
	}
	if req.RequestID == "" || req.RequestID != requestID || len(req.Questions) == 0 {
		return "", ErrSuspendedQuestionMismatch
	}
	var b strings.Builder
	b.WriteString("Answers to the question you asked before the service restarted — please continue from here:\n")
	for _, q := range req.Questions {
		b.WriteString("\n- ")
		b.WriteString(strings.TrimSpace(q.Question))
		b.WriteString("\n  → ")
		picked := answers[q.ID]
		if len(picked) == 0 {
			b.WriteString("(no answer)")
		} else {
			b.WriteString(strings.Join(picked, ", "))
		}
	}
	return b.String(), nil
}

// AnswerSuspendedQuestion delivers an answer to a question a restart left
// unanswered by queueing it as a new prompt, which resumes the session, then
// forgets the question and tells every tab it is resolved. It reports false
// when there is no such question — the room is busy, nothing is stored, or
// the request id is stale — leaving the caller to report the question gone.
func (i *PromptIntake) AnswerSuspendedQuestion(ctx context.Context, convID, userID, requestID string, answers map[string][]string, onQueued func(store.Message)) bool {
	if i.Store == nil || i.Broadcaster.IsBusy(convID) {
		return false
	}
	payload, err := i.Store.GetConversationSuspendedQuestion(ctx, convID)
	if err != nil || payload == "" {
		return false
	}
	content, err := SuspendedAnswerPrompt(payload, requestID, answers)
	if err != nil {
		return false
	}
	if _, err := i.Submit(ctx, PromptSubmission{
		ConversationID: convID,
		UserID:         userID,
		Content:        content,
		OnQueued:       onQueued,
	}); err != nil {
		log.Printf("[prompt] queue suspended-question answer conv=%s: %v", convID, err)
		return false
	}
	if err := i.Store.SetConversationSuspendedQuestion(ctx, convID, ""); err != nil {
		log.Printf("[prompt] clear suspended question conv=%s: %v", convID, err)
	}
	if data, err := json.Marshal(ServerMessage{Type: "user_question_resolved", RequestID: requestID}); err == nil {
		i.Broadcaster.Broadcast(convID, data)
	}
	return true
}
