// Package streamcommon holds the adapter scaffolding shared by the
// claudecli and codexcli adapters: the stall-aware NDJSON line pump
// (StreamLines), child-process plumbing (SetEnv, DrainStderr,
// DrainOneshot), oversized session-log scanning, and the marshal-then-emit
// pattern every parser uses to turn a payload into an agent.StreamEvent.
// Centralising them keeps the two adapters from drifting apart on the
// byte-level payload contract the handler and frontend consume;
// provider-specific stream semantics (block-index tracking, live-usage
// synthesis, cost formulas) stay in the adapter packages on purpose.
package streamcommon

import (
	"encoding/json"
	"log"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// Event marshals payload and wraps it in a single StreamEvent of the
// given kind. Returns nil when marshalling fails — dropping a frame
// beats emitting a malformed payload the frontend would choke on, a
// convention both adapters already followed independently.
//
// The drop is logged because it is otherwise invisible: a tool call that
// failed to marshal simply never appears in the conversation, and there is no
// error anywhere to connect that absence to this line. Marshal only fails on
// values a payload map should never contain (channels, funcs, NaN, cycles),
// so a line here means a parser built something it did not intend to.
func Event(kind string, payload any) []agent.StreamEvent {
	content, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[streamcommon] dropping a %s event: its payload could not be marshalled: %v", kind, err)
		return nil
	}
	return []agent.StreamEvent{{Kind: kind, Content: string(content)}}
}

// UsageEvent wraps a typed usage report in a single KindUsage event, with the
// same drop-and-log contract as Event: a report only fails to marshal when a
// cost came out NaN or infinite, and billing a frame like that is worse than
// losing it.
func UsageEvent(r agent.UsageReport) []agent.StreamEvent {
	evt, err := agent.NewUsageEvent(r)
	if err != nil {
		log.Printf("[streamcommon] dropping a %s event: %v", agent.KindUsage, err)
		return nil
	}
	return []agent.StreamEvent{evt}
}

// WithJSONContent re-serialises payload into evt.Content. On marshal
// failure the event is returned unchanged: enrichment callers still
// hold a valid (merely un-enriched) payload, so passthrough beats
// dropping the event.
func WithJSONContent(evt agent.StreamEvent, payload any) agent.StreamEvent {
	content, err := json.Marshal(payload)
	if err != nil {
		return evt
	}
	evt.Content = string(content)
	return evt
}
