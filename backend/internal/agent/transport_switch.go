package agent

import "context"

// TransportSwitch fronts several transports of the same provider type (the
// Claude CLI vs the Agent SDK, `codex exec` vs app-server) and delegates each
// call to whichever one the administrator currently selects. It lets the
// selection change at runtime without rebuilding the backend map every
// handler holds: the map keeps one stable entry per provider type, and the
// switch resolves on every call.
//
// The transports it switches between must share a session log layout, which
// is what makes a conversation resumable across a switch: claudecli and the
// Agent SDK both write Claude Code's projects/*.jsonl, codexcli and
// app-server both write the same rollout JSONL.
//
// Optional extensions (steering, user questions, TTY) are deliberately not
// implemented here. Callers reach them through the accessors in optional.go,
// which unwrap the switch first, so "can this conversation steer?" is answered
// by the transport actually selected rather than by the union of all of them.
type TransportSwitch struct {
	impls    map[string]Backend
	fallback string
	selected func() string
}

// NewTransportSwitch builds a switch over impls. selected returns the active
// transport key; an unknown or empty key resolves to fallback, which must be a
// key of impls.
func NewTransportSwitch(selected func() string, fallback string, impls map[string]Backend) *TransportSwitch {
	if _, ok := impls[fallback]; !ok {
		panic("agent: transport switch fallback " + fallback + " has no backend")
	}
	return &TransportSwitch{impls: impls, fallback: fallback, selected: selected}
}

// Current returns the backend for the transport selected right now.
func (s *TransportSwitch) Current() Backend {
	if s.selected != nil {
		if b, ok := s.impls[s.selected()]; ok && b != nil {
			return b
		}
	}
	return s.impls[s.fallback]
}

func (s *TransportSwitch) Name() string { return s.Current().Name() }

func (s *TransportSwitch) Capabilities() Capabilities { return s.Current().Capabilities() }

func (s *TransportSwitch) RunWithSession(ctx context.Context, prompt, workDir string, req RunRequest, outputCh chan<- StreamEvent) error {
	return s.Current().RunWithSession(ctx, prompt, workDir, req, outputCh)
}

func (s *TransportSwitch) RunOneshot(ctx context.Context, prompt, workDir string, req RunRequest) (string, error) {
	return s.Current().RunOneshot(ctx, prompt, workDir, req)
}

func (s *TransportSwitch) SessionExists(workDir, sessionID, configDir string) bool {
	return s.Current().SessionExists(workDir, sessionID, configDir)
}

func (s *TransportSwitch) SessionLogPath(workDir, sessionID, configDir string) string {
	return s.Current().SessionLogPath(workDir, sessionID, configDir)
}

// Resolve returns the concrete backend behind b: the selected transport when
// b is a TransportSwitch, b itself otherwise. A caller that runs a turn and
// then wires that turn's steering hook must resolve once and use the result
// for both, so an admin switch mid-turn can't pair one transport's run with
// another's hook.
func Resolve(b Backend) Backend {
	if s, ok := b.(*TransportSwitch); ok && s != nil {
		return s.Current()
	}
	return b
}
