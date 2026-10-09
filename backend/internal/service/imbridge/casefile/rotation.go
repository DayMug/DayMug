package casefile

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/config"
)

const (
	// RotateContextRatio is the fraction of the model's context window at
	// which the session behind the thread is retired and the next turn starts
	// clean from the case.
	//
	// 0.70, not 0.9: the agent has to have enough room left to write a decent
	// case. Rotating at the point of overflow is what compaction already does,
	// and it produces exactly the summary-under-duress this design exists to
	// avoid.
	//
	// This is the fallback, not the only value: `case_rotate_context_ratio` in
	// the deployment YAML overrides it. Tuning it needs measurement rather than
	// argument — the right number depends on how much context this deployment's
	// agents actually need in hand to write their cases — and a knob that costs
	// a release to turn is a knob nobody measures.
	RotateContextRatio = config.DefaultCaseRotateContextRatio

	// RotateForceRatio is where rotation stops being conditional on the case
	// having absorbed the session (see AbsorbedSession).
	//
	// The guard below it can hold a session open indefinitely — a turn that
	// records nothing keeps the same session for the next turn, which records
	// nothing either. That is the right trade while there is window left to
	// write a case in, and the wrong one at the very top: a session that cannot
	// take another turn answers nobody at all. Above this line the platform
	// rotates and accepts the loss.
	RotateForceRatio = 0.92
)

// ContextOverThreshold reports whether the last turn on a conversation left
// the model's context at or above ratio. A ratio <= 0 means the built-in
// RotateContextRatio.
//
// The payload is conversations.last_context_usage, which the stream consumers
// persist from the provider's own token accounting — this is a measurement,
// not an estimate, and it is why rotation can be a platform decision instead
// of something the agent is asked to volunteer.
//
// An empty payload means "no measurement yet" and reports false. That is the
// safe direction — a thread that never rotates still answers, where one that
// rotated on a guess would throw away a live session — but it is also silent:
// case mode shipped with the IM path not persisting the reading at all, and
// the symptom was indistinguishable from "the threshold was never reached".
func ContextOverThreshold(payload string, ratio float64) bool {
	if strings.TrimSpace(payload) == "" {
		return false
	}
	if ratio <= 0 {
		ratio = RotateContextRatio
	}
	var usage struct {
		Used  float64 `json:"used"`
		Total float64 `json:"total"`
	}
	if err := json.Unmarshal([]byte(payload), &usage); err != nil {
		return false
	}
	if usage.Total <= 0 {
		return false
	}
	return usage.Used/usage.Total >= ratio
}

// AbsorbedSession reports whether the case (doc, last saved at caseUpdatedAt)
// already holds what the session about to be retired — created at
// sessionCreatedAt — learned.
//
// Rotation trades a session's transcript for the case, and that trade is only
// sound when the case actually took delivery. Two ways it has not:
//
//   - the case is still the empty template — the next session would be
//     assembled from nothing, and everything the retired one found is gone;
//   - the case predates the conversation being retired — it carries an earlier
//     session's work, not this one's.
//
// The second test is a timestamp comparison rather than an authorship record,
// which admits one false positive: the *other* agent on the thread saving the
// shared case inside this conversation's lifetime reads as "absorbed". That
// direction is the acceptable one — the thread's state did advance and is
// readable by whoever comes next — and closing it would cost a schema column
// to catch a case that only arises when two agents work one thread at once.
func AbsorbedSession(doc string, caseUpdatedAt, sessionCreatedAt time.Time) bool {
	if !CarriesWork(doc) {
		return false
	}
	return caseUpdatedAt.After(sessionCreatedAt)
}
