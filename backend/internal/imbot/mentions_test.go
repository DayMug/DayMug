package imbot

import (
	"strconv"
	"strings"
	"testing"
)

// bracket renders a directory hit unmistakably, so a test can tell a resolved
// mention from the "@name" text that was left alone.
func bracket(userID string) string { return "[" + userID + "]" }

func TestResolveMentions(t *testing.T) {
	participants := map[string]string{
		"吕广超 Louis": "U_LOUIS",
		"吕广超":       "U_OTHER_LU",
		"Bob Chen":  "U_BOB",
		"Lou":       "U_LOU",
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "resolves the longest matching display name",
			in:   "@吕广超 Louis 已确认：不是 CloudFront 整体故障。",
			want: "[U_LOUIS] 已确认：不是 CloudFront 整体故障。",
		},
		{
			// A Chinese sentence runs straight on from the name, so CJK cannot
			// be treated as a word boundary the way ASCII is.
			name: "resolves a name glued to following Chinese text",
			in:   "@吕广超说的那个图挂了",
			want: "[U_OTHER_LU]说的那个图挂了",
		},
		{
			name: "leaves an unknown name alone",
			in:   "@关立志 看一下",
			want: "@关立志 看一下",
		},
		{
			name: "never resolves a channel-wide alias",
			in:   "@channel @here @everyone",
			want: "@channel @here @everyone",
		},
		{
			name: "leaves an email address alone",
			in:   "写信给 ops@Bob Chen.example.com",
			want: "写信给 ops@Bob Chen.example.com",
		},
		{
			name: "does not let a short name claim a longer one",
			in:   "@Louis is out",
			want: "@Louis is out",
		},
		{
			name: "skips inline code spans",
			in:   "grep `@Bob Chen` in the log, then ping @Bob Chen",
			want: "grep `@Bob Chen` in the log, then ping [U_BOB]",
		},
		{
			name: "skips fenced blocks",
			in:   "before @Lou\n```\n@Lou\n```\nafter @Lou",
			want: "before [U_LOU]\n```\n@Lou\n```\nafter [U_LOU]",
		},
		{
			name: "resolves several mentions on one line",
			in:   "@Lou and @Bob Chen, please look",
			want: "[U_LOU] and [U_BOB], please look",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveMentions(tt.in, participants, bracket); got != tt.want {
				t.Fatalf("resolveMentions() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveMentionsKeepsNameWhenRenderDeclines(t *testing.T) {
	participants := map[string]string{"Bob Chen": "not-an-id"}
	in := "ping @Bob Chen"
	if got := resolveMentions(in, participants, func(string) string { return "" }); got != in {
		t.Fatalf("resolveMentions() = %q, want the plain name kept", got)
	}
}

func TestResolveMentionsWithoutParticipants(t *testing.T) {
	in := "ping @Bob Chen"
	if got := resolveMentions(in, nil, bracket); got != in {
		t.Fatalf("resolveMentions() = %q, want unchanged", got)
	}
}

func TestMentionDirectoryScopesParticipantsToTheirThread(t *testing.T) {
	var d mentionDirectory
	d.remember("C1|100.1", "Alice", "U1")
	d.remember("C1|200.2", "Bob", "U2")

	if got := d.participants("C1|100.1"); len(got) != 1 || got["Alice"] != "U1" {
		t.Fatalf("thread 100.1 participants = %v, want only Alice", got)
	}
	if got := d.participants("C1|200.2"); got["Alice"] != "" {
		t.Fatalf("Alice leaked into another thread: %v", got)
	}
}

func TestMentionDirectoryDropsUnusableEntries(t *testing.T) {
	var d mentionDirectory
	d.remember("", "Alice", "U1")
	d.remember("C1|100.1", "", "U1")
	d.remember("C1|100.1", "Alice", "")
	// Connectors fall back to the raw id when name resolution fails; "@U0123"
	// is not a name anyone writes on purpose.
	d.remember("C1|100.1", "U0123", "U0123")

	if got := d.participants("C1|100.1"); len(got) != 0 {
		t.Fatalf("participants = %v, want none recorded", got)
	}
}

func TestMentionDirectoryEvictsOldestThread(t *testing.T) {
	var d mentionDirectory
	for i := 0; i <= mentionDirectoryThreadCap; i++ {
		d.remember("C1|"+strconv.Itoa(i), "Alice", "U"+strconv.Itoa(i))
	}
	if got := d.participants("C1|0"); got != nil {
		t.Fatalf("oldest thread survived eviction: %v", got)
	}
	if got := d.participants("C1|" + strconv.Itoa(mentionDirectoryThreadCap)); got == nil {
		t.Fatal("newest thread was evicted")
	}
}

func TestMentionDirectoryCapsOneThread(t *testing.T) {
	var d mentionDirectory
	for i := 0; i < mentionDirectoryNameCap+5; i++ {
		d.remember("C1|100.1", "user"+strconv.Itoa(i), "U"+strconv.Itoa(i))
	}
	if got := d.participants("C1|100.1"); len(got) != mentionDirectoryNameCap {
		t.Fatalf("participants = %d, want capped at %d", len(got), mentionDirectoryNameCap)
	}
}

// A resolved Slack mention has to survive SlackMrkdwn, which escapes every
// other angle bracket in the agent's answer.
func TestSlackMentionMarkupSurvivesEscaping(t *testing.T) {
	s := &SlackConnector{}
	resolved := resolveMentions("ping @Bob Chen about <!channel>",
		map[string]string{"Bob Chen": "UBOB123"}, s.slackMentionMarkup)

	got := SlackMrkdwn(resolved)
	if want := "ping <@UBOB123> about &lt;!channel&gt;"; got != want {
		t.Fatalf("SlackMrkdwn() = %q, want %q", got, want)
	}
	if strings.Contains(got, "\x00") {
		t.Fatalf("raw marker leaked into outbound text: %q", got)
	}
}

func TestSlackMentionMarkupRejectsNonUserIDs(t *testing.T) {
	s := &SlackConnector{}
	for _, id := range []string{"", "C1", "U1> <!channel", "ou_feishu"} {
		if got := s.slackMentionMarkup(id); got != "" {
			t.Fatalf("slackMentionMarkup(%q) = %q, want no markup", id, got)
		}
	}
}

func TestFeishuCardMentionMarkup(t *testing.T) {
	if got, want := feishuCardMentionMarkup("ou_abc123"), `<at id="ou_abc123"></at>`; got != want {
		t.Fatalf("feishuCardMentionMarkup() = %q, want %q", got, want)
	}
	if got := feishuCardMentionMarkup(`ou_x"><at id="ou_admin`); got != "" {
		t.Fatalf("feishuCardMentionMarkup() = %q, want no markup for a malformed id", got)
	}
}

// One reply must not ping one person twice. The agent opens its answer by
// addressing whoever asked, resolveMentions makes that live, and the courtesy
// prefix used to add a second mention of the same id on top.
func TestPrefixMentionSkipsAMentionTheAgentAlreadyWrote(t *testing.T) {
	s := &SlackConnector{}
	markup := s.slackMentionMarkup("UBOB123")
	resolved := resolveMentions("@Bob Chen 已完成，见下文", map[string]string{"Bob Chen": "UBOB123"}, s.slackMentionMarkup)

	got := SlackMrkdwn(prefixMention(resolved, markup))
	if want := "<@UBOB123> 已完成，见下文"; got != want {
		t.Fatalf("prefixMention() = %q, want %q", got, want)
	}
}

func TestPrefixMentionAddsTheMentionWhenTheAgentWroteNone(t *testing.T) {
	s := &SlackConnector{}
	got := SlackMrkdwn(prefixMention("已完成", s.slackMentionMarkup("UBOB123")))
	if want := "<@UBOB123> 已完成"; got != want {
		t.Fatalf("prefixMention() = %q, want %q", got, want)
	}
}

// A thread already polluted with doubled mentions teaches the agent to write the
// name twice itself; that is still one notification, so it renders once.
func TestPrefixMentionFoldsRepeatedMentions(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"back to back", `<at id="ou_bob"></at> <at id="ou_bob"></at> 已完成`, `<at id="ou_bob"></at> 已完成`},
		{"three with padding", `<at id="ou_bob"></at>  <at id="ou_bob"></at>	<at id="ou_bob"></at> 已完成`, `<at id="ou_bob"></at> 已完成`},
		{"no separator", `<at id="ou_bob"></at><at id="ou_bob"></at>已完成`, `<at id="ou_bob"></at>已完成`},
		{"across a newline stays", "<at id=\"ou_bob\"></at> 第一行\n<at id=\"ou_bob\"></at> 第二行", "<at id=\"ou_bob\"></at> 第一行\n<at id=\"ou_bob\"></at> 第二行"},
		{"someone else in between stays", `<at id="ou_bob"></at> <at id="ou_amy"></at> 请看`, `<at id="ou_bob"></at> <at id="ou_amy"></at> 请看`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prefixMention(tt.text, feishuCardMentionMarkup("ou_bob")); got != tt.want {
				t.Fatalf("prefixMention() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A sender id no platform markup can address leaves the answer untouched rather
// than gluing a bare separator onto it.
func TestPrefixMentionWithoutMarkupIsANoop(t *testing.T) {
	if got := prefixMention("已完成", ""); got != "已完成" {
		t.Fatalf("prefixMention() = %q, want the text unchanged", got)
	}
}
