package imbot

// Outbound @-mentions. Inbound, every connector rewrites the platform's escaped
// mention markup into a plain "@display name" so the agent can read who was
// addressed (see (*SlackConnector).inlineMentions and its 飞书 twin). This file
// is the inverse: an agent that writes "@display name" in its answer gets a
// native platform mention that actually notifies that person.
//
// The inverse has to be whitelisted where the forward direction does not. Agent
// text is untrusted, and on Slack it is deliberately HTML escaped end to end so
// a model cannot spell a live "<!channel>" — see SlackMrkdwn. So instead of
// letting the agent emit markup, DayMug resolves names against a directory of
// the humans who have actually spoken in this thread. Someone who never
// appeared in the thread, a bot, and every channel-wide broadcast alias are all
// unresolvable by construction.

import (
	"sort"
	"strings"
	"sync"
)

const (
	// mentionDirectoryThreadCap bounds how many threads one connector remembers
	// participants for. A connector lives as long as its bot's connection, so
	// without a cap a busy workspace grows this map forever.
	mentionDirectoryThreadCap = 512
	// mentionDirectoryNameCap bounds one thread's participants. A thread with
	// more distinct speakers than this is a channel-wide announcement, not a
	// conversation the agent is going to address someone in by name.
	mentionDirectoryNameCap = 64
)

// mentionDirectory maps, per thread, a participant's display name back to the
// platform user id needed to mention them. Held by baseConnector so both
// platforms that support this share one implementation.
type mentionDirectory struct {
	mu      sync.Mutex
	threads map[string]map[string]string // thread key → display name → user id
	order   []string                     // insertion order of thread keys, for eviction
}

// mentionThreadKey identifies one thread across a connector's lifetime.
func mentionThreadKey(channelID, threadID string) string {
	if channelID == "" || threadID == "" {
		return ""
	}
	return channelID + "|" + threadID
}

// remember records one participant of a thread. Callers must only pass humans:
// a bot in the directory would let ordinary prose mention another bot, which is
// exactly the trigger the explicit [HANDOFF @Bot] protocol exists to keep
// deliberate.
//
// A name equal to the user id is dropped rather than stored. Connectors fall
// back to the raw id when name resolution fails, and "@U0123" is not a name any
// reader — or agent — would write on purpose.
func (d *mentionDirectory) remember(threadKey, name, userID string) {
	name = strings.TrimSpace(name)
	if threadKey == "" || name == "" || userID == "" || name == userID {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.threads == nil {
		d.threads = map[string]map[string]string{}
	}
	participants, ok := d.threads[threadKey]
	if !ok {
		if len(d.order) >= mentionDirectoryThreadCap {
			delete(d.threads, d.order[0])
			d.order = d.order[1:]
		}
		participants = map[string]string{}
		d.threads[threadKey] = participants
		d.order = append(d.order, threadKey)
	}
	if _, known := participants[name]; !known && len(participants) >= mentionDirectoryNameCap {
		return
	}
	participants[name] = userID
}

// participants returns a snapshot of one thread's directory.
func (d *mentionDirectory) participants(threadKey string) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	known := d.threads[threadKey]
	if len(known) == 0 {
		return nil
	}
	out := make(map[string]string, len(known))
	for name, id := range known {
		out[name] = id
	}
	return out
}

// prefixMention prepends the markup that notifies a thread's trusted sender,
// unless the answer already mentions them.
//
// Two independent producers aim the same mention at the same person: the
// courtesy prefix here, and resolveMentions turning an agent-written "@Name"
// into live markup. An agent routinely opens its answer by addressing whoever
// asked — the prompt teaches it to write "@Name", and the thread history it
// reads back contains its own earlier prefixed replies re-inlined as "@Name" —
// so composing both unconditionally pinged one person twice for one reply.
// Whichever mention survives notifies them exactly once, so the prefix is the
// one to drop.
//
// A run of the same markup repeated back to back is folded to one as well: a
// thread already polluted with doubled mentions teaches the agent to write the
// name twice itself, and that is the same single notification twice over.
func prefixMention(text, markup string) string {
	if markup == "" {
		return text
	}
	text = collapseMentionRuns(text, markup)
	if strings.Contains(text, markup) {
		return text
	}
	return markup + " " + text
}

// collapseMentionRuns folds each run of markup separated only by spaces or tabs
// down to a single occurrence. Newlines are not crossed: the same name opening
// two consecutive lines is prose, not a duplicated address.
func collapseMentionRuns(text, markup string) string {
	if !strings.Contains(text, markup) {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		if !strings.HasPrefix(text[i:], markup) {
			b.WriteByte(text[i])
			i++
			continue
		}
		b.WriteString(markup)
		i += len(markup)
		for {
			next := i
			for next < len(text) && (text[next] == ' ' || text[next] == '\t') {
				next++
			}
			if !strings.HasPrefix(text[next:], markup) {
				break
			}
			i = next + len(markup)
		}
	}
	return b.String()
}

// resolveMentions rewrites every "@display name" that matches a thread
// participant into the markup render returns for them. A render returning ""
// leaves the text alone, so a participant whose id does not match the
// platform's own id shape degrades to the plain name the agent wrote.
//
// Fenced blocks and inline code spans are skipped: an answer explaining that
// `@alice` owns a script must not page alice.
func resolveMentions(text string, participants map[string]string, render func(userID string) string) string {
	if len(participants) == 0 || render == nil || !strings.Contains(text, "@") {
		return text
	}
	names := make([]string, 0, len(participants))
	for name := range participants {
		names = append(names, name)
	}
	// Longest first, so "@吕广超 Louis" resolves as one person rather than
	// matching a shorter "吕广超" that happens to share the prefix. Equal
	// lengths break lexicographically to keep the output deterministic.
	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}
		return names[i] < names[j]
	})

	lines := strings.Split(text, "\n")
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lines[i] = resolveMentionsInLine(line, names, participants, render)
	}
	return strings.Join(lines, "\n")
}

// resolveMentionsInLine splits on backticks the way mrkdwnSpans does: an
// odd-indexed segment with a closing backtick is code and passes through.
func resolveMentionsInLine(line string, names []string, participants map[string]string, render func(string) string) string {
	parts := strings.Split(line, "`")
	for i, part := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			continue
		}
		parts[i] = resolveMentionsInSegment(part, names, participants, render)
	}
	return strings.Join(parts, "`")
}

func resolveMentionsInSegment(segment string, names []string, participants map[string]string, render func(string) string) string {
	var b strings.Builder
	for i := 0; i < len(segment); {
		offset := strings.IndexByte(segment[i:], '@')
		if offset < 0 {
			b.WriteString(segment[i:])
			break
		}
		at := i + offset
		b.WriteString(segment[i:at])
		i = at + 1
		if at > 0 && isMentionWordByte(segment[at-1]) {
			// "ops@example.com" is an address, not a mention.
			b.WriteByte('@')
			continue
		}
		matched, markup := "", ""
		for _, name := range names {
			rest := segment[at+1:]
			if !strings.HasPrefix(rest, name) {
				continue
			}
			end := at + 1 + len(name)
			if end < len(segment) && isMentionWordByte(segment[end]) {
				// "@Lou" must not claim the "Lou" of "@Louis".
				continue
			}
			if markup = render(participants[name]); markup == "" {
				continue
			}
			matched = name
			break
		}
		if matched == "" {
			b.WriteByte('@')
			continue
		}
		b.WriteString(markup)
		i = at + 1 + len(matched)
	}
	return b.String()
}

// isMentionWordByte reports the ASCII characters that glue a name to its
// surroundings. CJK is deliberately excluded: "@吕广超说的那个问题" is an
// ordinary Chinese sentence that still addresses 吕广超, whereas treating every
// letter as a boundary would make Chinese names unmentionable in prose.
func isMentionWordByte(b byte) bool {
	switch {
	case b >= '0' && b <= '9', b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
		return true
	case b == '_', b == '-', b == '.':
		return true
	}
	return false
}
