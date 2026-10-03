package service

import (
	"context"

	"github.com/DayMug/DayMug/backend/internal/store"
)

// PromptOutcome is the transport-neutral result of one dispatched prompt.
// Content is the final parent-agent reply, including an accumulated partial
// reply when the backend ended without a result event.
type PromptOutcome struct {
	Content   string
	Artifacts []Artifact
	// RejectedArtifacts carries the publish markers that produced no file, so a
	// mirroring transport can tell the user instead of leaving the reply
	// claiming an attachment that never arrived.
	RejectedArtifacts []ArtifactRejection
	Err               error
	Cancelled         bool
}

// PromptObservation follows one prompt after it has been accepted by the
// dispatcher. Implementations may reserve external resources in ObservePrompt,
// but must wait for Started before publishing user-visible side effects: a
// queued prompt can still be recalled before its database row is claimed.
type PromptObservation interface {
	Started(ctx context.Context)
	Finish(ctx context.Context, outcome PromptOutcome)
	Close()
}

// PromptGuardrailObserver is the optional extension used by mirrored
// transports to show the same next-message reminder as the browser.
type PromptGuardrailObserver interface {
	Guardrail(ctx context.Context, snapshot GuardrailSnapshot)
}

// PromptObserver optionally mirrors the lifecycle of web-chat prompts to an
// attached transport. Returning nil leaves the normal chat path unchanged.
type PromptObserver interface {
	ObservePrompt(ctx context.Context, prompt store.Message) PromptObservation
}
