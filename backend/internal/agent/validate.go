package agent

import (
	"encoding/json"
	"fmt"
)

// jsonKinds are the Kinds whose Content is a JSON document rather than prose.
// Everything not listed here carries free text: delta / result / thinking_delta
// are assistant output, tool_input_delta is a JSON *fragment* that is only
// parseable once reassembled, user_question_resolved is a bare request id, and
// session_info / session_warning / error are sentences written for the user.
var jsonKinds = map[string]bool{
	KindContextUsage:     true,
	KindSystemInit:       true,
	KindToolUseStart:     true,
	KindToolResult:       true,
	KindUsage:            true,
	KindModelCall:        true,
	KindRateLimit:        true,
	KindBackgroundTasks:  true,
	KindTaskNotification: true,
	KindLiveUsage:        true,
	KindUserQuestion:     true,
}

// Validate reports whether an event is internally consistent enough to be
// worth sending.
//
// It exists because a malformed payload fails quietly and late. Content is an
// opaque string on this side, so a tool_result whose JSON was mangled upstream
// travels the whole way to the browser (and into the message row) before
// anything notices, and what the user sees is a tool call that rendered
// blank — no error, no log line pointing at the adapter that produced it.
// Mentor hit exactly this with a regex that rewrote paths inside a serialized
// payload and ate an escape sequence; the frame vanished and only a server log
// remained. Checking at the boundary names the adapter and the Kind instead.
//
// What is deliberately *not* checked: the presence of a tool call id. Claude's
// stream omits it for tool blocks that never carried one, so requiring it here
// would reject frames that are legitimate today. Kinds and JSON validity are
// the invariants every producer already holds.
func (e StreamEvent) Validate() error {
	if e.Kind == "" {
		return fmt.Errorf("event has no Kind")
	}
	if !knownKinds[e.Kind] {
		return fmt.Errorf("unknown Kind %q", e.Kind)
	}
	if e.Kind == KindUserQuestionResolved && e.Content == "" {
		// The content *is* the request id being retracted; without it the
		// client cannot tell which question card to take down.
		return fmt.Errorf("%s carries no request id", KindUserQuestionResolved)
	}
	if jsonKinds[e.Kind] {
		if e.Content == "" {
			return fmt.Errorf("%s carries no payload", e.Kind)
		}
		if !json.Valid([]byte(e.Content)) {
			return fmt.Errorf("%s payload is not valid JSON", e.Kind)
		}
	}
	return nil
}

// knownKinds is the closed set an adapter may emit. A Kind absent from it is
// almost always a typo'd literal: the event then travels as far as the
// service layer's switch, matches nothing, and is dropped without a word.
var knownKinds = map[string]bool{
	KindDelta:                true,
	KindResult:               true,
	KindContextUsage:         true,
	KindSystemInit:           true,
	KindToolUseStart:         true,
	KindToolInputDelta:       true,
	KindToolResult:           true,
	KindUsage:                true,
	KindModelCall:            true,
	KindThinkingDelta:        true,
	KindSessionInfo:          true,
	KindSessionWarning:       true,
	KindUserQuestion:         true,
	KindUserQuestionResolved: true,
	KindRateLimit:            true,
	KindBackgroundTasks:      true,
	KindTaskNotification:     true,
	KindLiveUsage:            true,
	KindError:                true,
}
