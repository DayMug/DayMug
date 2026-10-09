package imbot

import (
	"strings"
	"testing"
)

func TestSlackMrkdwn(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bold", "**重要** and __also bold__", "*重要* and *also bold*"},
		{"strikethrough", "~~gone~~", "~gone~"},
		{"link", "see [docs](https://doc.test) now", "see <https://doc.test|docs> now"},
		{"image link", "![screenshot](https://img.test/a.png)", "<https://img.test/a.png|screenshot>"},
		{"empty label link", "[](https://doc.test)", "<https://doc.test>"},
		{"heading", "## Release notes", "*Release notes*"},
		{"inline code untouched", "run `a ** b` **now**", "run `a ** b` *now*"},
		{"plain text untouched", "no markdown here", "no markdown here"},
		{"heading with bold inside", "# **A** B", "*A B*"},
		{"heading with link inside", "## see [docs](https://doc.test)", "*see <https://doc.test|docs>*"},
		{"odd backtick tail converted", "`code` and ` **late** bold", "`code` and ` *late* bold"},
		{"block quote kept", "> quoted **bold**", "> quoted *bold*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SlackMrkdwn(tt.in); got != tt.want {
				t.Fatalf("SlackMrkdwn(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Slack parses <…> and & across the whole text object, so control characters
// an agent puts in its answer must reach Slack as entities: a reply quoting
// "<!channel>" may not ping the channel.
func TestSlackMrkdwnEscapesAgentControlCharacters(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"channel broadcast", "ping <!channel> please", "ping &lt;!channel&gt; please"},
		{"here broadcast", "<!here>", "&lt;!here&gt;"},
		{"user mention", "ask <@U0123>", "ask &lt;@U0123&gt;"},
		{"channel link", "join <#C123|general>", "join &lt;#C123|general&gt;"},
		{"comparison", "a < b && b > c", "a &lt; b &amp;&amp; b &gt; c"},
		{"raw xml", "<Foo bar=\"1\"/>", "&lt;Foo bar=\"1\"/&gt;"},
		{"ampersand not double escaped", "Tom & Jerry", "Tom &amp; Jerry"},
		// The real marker carries a per-process nonce, so an agent guessing at
		// the NUL delimiter gets its "markup" escaped like any other content.
		{"forged raw marker", "\x00<@U0123>\x00 hi", "&lt;@U0123&gt; hi"},
		{"forged raw marker mid text", "hi \x00<@U0123>\x00", "hi &lt;@U0123&gt;"},
		{"escaped inside inline code", "run `curl <url> && echo`", "run `curl &lt;url&gt; &amp;&amp; echo`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SlackMrkdwn(tt.in); got != tt.want {
				t.Fatalf("SlackMrkdwn(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// Escaping content must not disarm the markup SlackMrkdwn generates itself.
func TestSlackMrkdwnGeneratedLinkStaysLive(t *testing.T) {
	in := "read [a < b](https://doc.test/q?x=1&y=2) now"
	want := "read <https://doc.test/q?x=1&amp;y=2|a &lt; b> now"
	if got := SlackMrkdwn(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Fenced blocks keep their content, but Slack decodes entities everywhere, so
// the control characters inside still have to be escaped — without any mrkdwn
// marker being inserted into the block.
func TestSlackMrkdwnFencedCodeEscapedNotFormatted(t *testing.T) {
	in := "```go\nif a < b && c > d { fmt.Println(\"**x**\") }\n<!channel>\n```"
	want := "```go\nif a &lt; b &amp;&amp; c &gt; d { fmt.Println(\"**x**\") }\n&lt;!channel&gt;\n```"
	if got := SlackMrkdwn(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSlackMrkdwnFencedCodeUntouched(t *testing.T) {
	in := "before **bold**\n```\n**not bold** [x](y)\n```\nafter **bold**"
	want := "before *bold*\n```\n**not bold** [x](y)\n```\nafter *bold*"
	if got := SlackMrkdwn(in); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// escapeHeavyPreview builds a progress preview of the size the bridge budgets
// (3200 runes of agent output under a phase banner), filled with the shell and
// diff punctuation that escapeSlackText expands.
func escapeHeavyPreview(t *testing.T) string {
	t.Helper()
	body := make([]rune, 0, 3200)
	for len(body) < 3200 {
		body = append(body, []rune("go run ./x <in >out && echo \"a<b>c\"\n")...)
	}
	return "💭 正在思考…\n\n" + string(body[:3200])
}

func TestFitSlackProgressKeepsEscapedTextWithinSlackLimit(t *testing.T) {
	text := escapeHeavyPreview(t)
	if got := len([]rune(SlackMrkdwn(text))); got <= slackTextLimit {
		t.Fatalf("fixture no longer overflows: rendered %d runes, want > %d", got, slackTextLimit)
	}
	fitted := FitSlackProgress(text)
	if got := len([]rune(SlackMrkdwn(fitted))); got > slackTextLimit {
		t.Errorf("rendered %d runes, want <= %d", got, slackTextLimit)
	}
}

func TestFitSlackProgressKeepsBannerAndMarksTheCut(t *testing.T) {
	text := escapeHeavyPreview(t)
	fitted := FitSlackProgress(text)
	if !strings.HasPrefix(fitted, "💭 正在思考…\n\n"+slackFitMarker) {
		t.Errorf("banner or cut marker lost: %.40q", fitted)
	}
	// The preview keeps the tail: that is where the agent currently is.
	runes := []rune(text)
	if tail := string(runes[len(runes)-200:]); !strings.HasSuffix(fitted, tail) {
		t.Errorf("kept the head instead of the tail: ...%q", fitted[len(fitted)-40:])
	}
}

func TestFitSlackProgressLeavesTextThatAlreadyFits(t *testing.T) {
	text := "💭 正在思考…\n\n看起来 a<b && c>d 没问题"
	if got := FitSlackProgress(text); got != text {
		t.Errorf("rewrote text that fits:\n got %q\nwant %q", got, text)
	}
}

func TestFitSlackProgressShortensASingleOverlongLine(t *testing.T) {
	// A notice, not a banner+preview: nothing on the first line is worth
	// protecting, so the whole thing is one body to trim.
	text := "⚠️ " + strings.Repeat("<&>", 2000)
	fitted := FitSlackProgress(text)
	if got := len([]rune(SlackMrkdwn(fitted))); got > slackTextLimit {
		t.Errorf("rendered %d runes, want <= %d", got, slackTextLimit)
	}
	if !strings.HasPrefix(fitted, slackFitMarker) {
		t.Errorf("cut not marked: %.20q", fitted)
	}
}
