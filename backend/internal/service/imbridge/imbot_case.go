package imbridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/imbridge/casefile"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// This file is the turn-side half of case-file mode: it moves the case between
// the store and the agent's disk, and decides where in the request it goes.
// What the case is, the limits it is held to, and every decision that can be
// made from its text alone live in package casefile.

// agentCaseRoot returns the absolute directory an agent's thread cases are
// materialized into. It sits under the same per-agent scope as that agent's
// inbound IM attachments, so everything a thread produced on disk is in one
// place — and, because the scope hangs off the owner's home rather than the
// conversation's cwd, one place per agent rather than one per project.
// It returns the home root alongside the case directory because the write path
// containment-checks against the root, not against the directory it just
// created: a symlinked `.daymug` would otherwise redirect every case out of the
// home and still pass a check anchored on its own target.
func agentCaseRoot(ctx context.Context, s store.Store, agentUser store.User) (homeRoot, caseRoot string, err error) {
	homeOwner, err := service.AgentHomeOwner(ctx, s, agentUser)
	if err != nil {
		return "", "", err
	}
	scope, err := service.AgentScopeDir(agentUser.ID)
	if err != nil {
		return "", "", err
	}
	return homeOwner.WorkDir, service.DaymugPath(homeOwner.WorkDir, scope+"/"+service.CaseSegment), nil
}

// unboundedTurn reports whether this turn skips the per-turn consumption
// guardrail. It follows the agent's case-mode setting rather than
// caseModeEnabled: a case directory that failed to resolve degrades the case
// block, not the operator's choice to let these threads run without pauses.
func (r *imRun) unboundedTurn() bool {
	return r.agentUser.CaseMode && r.msg.ChannelID != ""
}

// caseModeEnabled reports whether this turn runs in case-file mode.
//
// Three conditions, all required. The agent-level toggle is the operator's
// switch. The IM binding is the second: an agent with case mode on still has
// web conversations, and those have no thread to key a case by — the whole
// mechanism is addressed by (channel_id, thread_id). The third is a resolved
// case directory: without one there is nowhere to materialize the document,
// and a case the agent cannot open is worse than no case prompt at all.
func (r *imRun) caseModeEnabled() bool {
	if !r.agentUser.CaseMode || r.caseRoot == "" {
		return false
	}
	// Keyed off the inbound message, not r.thread: a thread's first turn has
	// no stored binding yet, and that turn is exactly when a case should be
	// opened. An empty ChannelID is the "not bound to an IM channel" case the
	// toggle is documented to exclude.
	return r.msg.ChannelID != ""
}

// casePath is where this agent sees the thread's case on disk. The name is
// built from connector-supplied ids, so it is sanitized into a single segment
// that cannot leave the case directory.
func (r *imRun) casePath() string {
	name := service.SanitizeUploadSegment(r.msg.ChannelID + "-" + r.msg.ThreadID)
	return filepath.Join(r.caseRoot, name+".md")
}

// caseFile is this agent's copy of the thread's case together with its marker
// files; see casefile.File.
func (r *imRun) caseFile() casefile.File {
	return casefile.File{Path: r.casePath()}
}

// loadCase reads the thread's case from the store. Failures degrade to "no
// case": a lookup error must not take down the turn, because a turn that
// answers nobody is strictly worse than a turn that answers without history.
func (r *imRun) loadCase(ctx context.Context) store.ThreadCase {
	c, err := r.bridge.Store.GetThreadCase(ctx, r.msg.ChannelID, r.msg.ThreadID)
	if err != nil {
		log.Printf("[case] load conv=%s: %v", r.conversation.ID, err)
		return store.ThreadCase{ChannelID: r.msg.ChannelID, ThreadID: r.msg.ThreadID}
	}
	return c
}

// materializeCase writes the stored case into the agent's case directory so the
// turn can read and edit it as an ordinary file, and returns the doc that was
// written.
//
// The store copy is authoritative, not the file: two agents on one thread have
// two scopes, and only the store sees both. Overwriting the local file each
// turn is what propagates the other agent's edits.
func (r *imRun) materializeCase(doc string, version int) (string, error) {
	if strings.TrimSpace(doc) == "" {
		doc = casefile.Template(r.caseTitle())
	}
	file := r.caseFile()
	if err := os.MkdirAll(filepath.Dir(file.Path), 0o755); err != nil {
		return "", fmt.Errorf("创建案卷目录: %w", err)
	}
	if within, err := service.PathWithin(r.homeRoot, file.Path); err != nil || !within {
		return "", fmt.Errorf("案卷路径 %s 解析后逃逸出家目录", file.Path)
	}
	if err := file.Write(doc); err != nil {
		return "", fmt.Errorf("写入案卷: %w", err)
	}
	// Best effort: a missing or unwritable marker only costs the next turn its
	// ability to recognise a human edit, which must not fail the run.
	_ = file.MarkSynced(version)
	return doc, nil
}

// prepareCase loads the thread's case and writes it into the work_dir, leaving
// the result on r.caseDoc for prompt assembly and for the post-run comparison.
//
// Every failure here is non-fatal and leaves r.caseDoc empty, which suppresses
// the case prompt block for this turn: an agent that answers with no case is
// degraded, an agent that refuses to answer because a file write failed is
// broken.
func (r *imRun) prepareCase(ctx context.Context) {
	if !r.caseModeEnabled() {
		return
	}
	file := r.caseFile()
	stored := r.loadCase(ctx)
	// A human edit made between turns is promoted to a stored revision before
	// anything else reads the case, so the correction reaches the other agent
	// on the thread too rather than living only in this work_dir.
	if local, ok := file.AdoptLocalEdit(stored.Doc, stored.Version); ok {
		author, retry := "human", false
		if file.HasUnsaved() {
			author, retry = r.agentUser.Name, true
		}
		saved, err := r.bridge.Store.SaveThreadCase(ctx, r.msg.ChannelID, r.msg.ThreadID, local, author)
		if err != nil {
			log.Printf("[case] adopt local edit conv=%s: %v", r.conversation.ID, err)
		} else {
			log.Printf("[case] adopted a local edit for thread %s/%s as v%d (by %s, retry=%t)",
				r.msg.ChannelID, r.msg.ThreadID, saved.Version, author, retry)
			file.ClearUnsaved()
			stored = saved
		}
	}
	doc, err := r.materializeCase(stored.Doc, stored.Version)
	if err != nil {
		log.Printf("[case] materialize conv=%s: %v", r.conversation.ID, err)
		return
	}
	r.caseDoc = doc
	r.caseHandover = r.loadHandover(ctx)
	r.caseRejection = file.ConsumeRejection()
}

// loadHandover reads the final reply of the conversation this turn rotated away
// from, so it can ride into the session replacing it.
//
// The case is meant to carry state, and it does. What it cannot carry is the
// text a human is answering: a follow-up like "not that one, check the others"
// is a reply to a message, and after rotation that message exists nowhere the
// new session can reach — the thread backfill starts after LastMessageID and
// drops the agent's own posts regardless. One message, tail-truncated, closes
// that gap without reintroducing the transcript rotation exists to shed.
func (r *imRun) loadHandover(ctx context.Context) string {
	if r.retiredConversationID == "" {
		return ""
	}
	msg, err := r.bridge.Store.LatestAssistantMessage(ctx, r.retiredConversationID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("[case] handover lookup conv=%s: %v", r.retiredConversationID, err)
		}
		return ""
	}
	return previewTail(msg.Content, casefile.HandoverExcerptRunes)
}

// caseTitle names the case after the conversation so a human opening the file
// can tell which thread it belongs to.
func (r *imRun) caseTitle() string {
	if t := strings.TrimSpace(r.conversation.Title); t != "" {
		return t
	}
	return r.msg.ChannelID + "/" + r.msg.ThreadID
}

// maxRotationChain bounds the walk up conversations.rotated_from, so a
// corrupted chain that loops costs a bounded number of reads, not a hang.
const maxRotationChain = 1000

// conversationSeq is the conversation's place on its thread: 1 for the one
// that opened it, one more for each rotation behind it, read off the
// rotated_from chain. A lookup failure ends the walk early — a wrong number in
// a title is cosmetic and must not fail the turn.
func (r *imRun) conversationSeq(ctx context.Context, id string) int {
	n := 1
	for id != "" && n < maxRotationChain {
		parent, err := r.bridge.Store.GetConversationRotatedFrom(ctx, id)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				log.Printf("[case] rotation chain lookup conv=%s: %v", id, err)
			}
			break
		}
		if parent == "" {
			break
		}
		n++
		id = parent
	}
	return n
}

func seqSuffix(n int) string {
	return "-" + strconv.Itoa(n)
}

// rotatedConversationTitle names the conversation replacing a rotated one: the
// retired conversation's title with the replacement's place on the thread
// appended ("X" → "X-2" → "X-3"). The thread's first conversation is never
// numbered; a later one's own suffix is stripped by its known number rather
// than by pattern, so a title whose summary ends in digits ("COVID-19") keeps
// them. replaceable reports that the retired conversation never got a real
// title, which leaves the replacement to auto-title under its number.
//
// Rotation starts a new session on the same thread and the same case, so a
// freshly generated title would only re-summarize it — at the cost of a
// title-model call — and break the visible link between the thread's
// conversations.
func rotatedConversationTitle(retired string, retiredSeq int, fallback string) (title string, replaceable bool) {
	base := strings.TrimSpace(retired)
	if retiredSeq > 1 {
		base = strings.TrimSuffix(base, seqSuffix(retiredSeq))
	}
	if base == "" || base == fallback {
		return fallback + seqSuffix(retiredSeq+1), true
	}
	return base + seqSuffix(retiredSeq+1), false
}

// persistCase reads back whatever the turn left on disk and stores it when it
// changed. Called after the agent finishes, on a detached context, for the same
// reason persistTurnBindings is: the turn's own context is already cancelled on
// the preemption path, and losing the case there would silently discard the
// only durable record of the turn.
func (r *imRun) persistCase(ctx context.Context, before string) {
	file := r.caseFile()
	raw, err := os.ReadFile(file.Path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[case] read back conv=%s: %v", r.conversation.ID, err)
		}
		return
	}
	doc := string(raw)
	if doc == before {
		return
	}
	reconciled, err := casefile.ReconcileAgentUpdate(before, doc)
	if err != nil {
		log.Printf("[case] rejected unsafe update conv=%s agent=%s: %v", r.conversation.ID, r.agentUser.Name, err)
		if writeErr := file.Write(before); writeErr != nil {
			log.Printf("[case] restore rejected update conv=%s: %v", r.conversation.ID, writeErr)
		}
		// Best effort: failing to write the notice costs the agent an
		// explanation, never the turn.
		if recordErr := file.RecordRejection(err.Error()); recordErr != nil {
			log.Printf("[case] record rejection conv=%s: %v", r.conversation.ID, recordErr)
		}
		return
	}
	if reconciled != doc {
		// The merge put back 「已排除」 entries this turn dropped, keeping the
		// rest of its work. Write the merged text out before storing it: the
		// file the agent reads next must match the revision that was saved, or
		// the next turn finds its copy ahead of the sync marker and re-adopts
		// the dropped version as a human edit.
		doc = reconciled
		if writeErr := file.Write(doc); writeErr != nil {
			log.Printf("[case] write reconciled case conv=%s: %v", r.conversation.ID, writeErr)
		}
		log.Printf("[case] restored 「已排除」 entries dropped by conv=%s agent=%s", r.conversation.ID, r.agentUser.Name)
	}
	saved, err := r.bridge.Store.SaveThreadCase(ctx, r.msg.ChannelID, r.msg.ThreadID, doc, r.agentUser.Name)
	if err == nil {
		// Advance the sync marker to the revision just written. Without this
		// the next turn would see the file ahead of its marker and mistake the
		// agent's own edit for a human's.
		_ = file.MarkSynced(saved.Version)
		file.ClearUnsaved()
	}
	if err != nil {
		log.Printf("[case] save conv=%s: %v", r.conversation.ID, err)
		// Leave the file as the agent wrote it — the next turn re-adopts it —
		// but record whose text it is, so the retry is not filed as a human
		// edit.
		if writeErr := file.MarkUnsaved(r.agentUser.Name); writeErr != nil {
			log.Printf("[case] mark unsaved conv=%s: %v", r.conversation.ID, writeErr)
		}
	}
}

// attachCase puts the case where it belongs for this turn and returns the
// prompt to run, having appended anything that must ride at the end of it.
//
// Where it goes is the whole point of this function, and it is a caching
// decision rather than a prompt-authoring one:
//
//   - Opening a session: the full case goes into the system prompt. Both
//     adapters render that at the very front (codex as the first `developer`
//     message via developer_instructions, claude via --append-system-prompt),
//     which is correct exactly once — a fresh session has no cached prefix to
//     protect, and the case is what the session is being assembled from.
//   - Continuing a session: nothing goes in the system prompt. The case is a
//     document the agent rewrites, so re-injecting it at the front on every
//     turn would change the prefix every turn and invalidate the entire cached
//     conversation behind it. Measured on one real thread, the difference is
//     ~7.6M vs ~59.4M full-price-equivalent input tokens.
//
// A resumed turn is not left blind: the agent read the case when the session
// opened, the file is on disk at the path it was given, and anything the
// platform still has to enforce rides at the END of the prompt where it costs
// its own tokens and nothing else's.
func (r *imRun) attachCase(opts *agent.RunRequest, prompt string) string {
	if !r.caseModeEnabled() || r.caseDoc == "" {
		return prompt
	}
	if !opts.IsResume {
		service.AppendSystem(opts, casefile.SystemPrompt(r.casePath(), r.caseDoc))
		if r.caseHandover != "" {
			service.AppendSystem(opts, casefile.HandoverPrompt(r.caseHandover))
		}
		if r.caseRejection != "" {
			// Rides at the end of the user message even on a fresh session:
			// it is a one-off notice about a rewrite that has to happen this
			// turn, not part of the durable state the system prompt carries.
			return prompt + "\n\n" + casefile.RejectionNotice(r.casePath(), r.caseRejection)
		}
		return prompt
	}
	reminder := casefile.TurnReminder(r.casePath(), r.caseDoc)
	if r.caseRejection != "" {
		reminder = casefile.RejectionNotice(r.casePath(), r.caseRejection) + " " + reminder
	}
	if r.caseRotationHeld {
		reminder = casefile.RotationHoldNotice(r.casePath()) + " " + reminder
	}
	if reminder != "" {
		return prompt + "\n\n" + reminder
	}
	return prompt
}
