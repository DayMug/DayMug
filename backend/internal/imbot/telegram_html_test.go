package imbot

import "testing"

func TestTelegramHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "escapes the html control characters",
			in:   "a < b & c > d",
			want: "a &lt; b &amp; c &gt; d",
		},
		{
			name: "converts bold",
			in:   "**bold** and __also bold__",
			want: "<b>bold</b> and <b>also bold</b>",
		},
		{
			name: "converts strikethrough",
			in:   "~~gone~~",
			want: "<s>gone</s>",
		},
		{
			name: "heading becomes a bold line without nesting bold",
			in:   "## **Release** notes",
			want: "<b>Release notes</b>",
		},
		{
			name: "inline code keeps content verbatim but escaped",
			in:   "run `if a<b then` now",
			want: "run <code>if a&lt;b then</code> now",
		},
		{
			name: "an unmatched backtick stays literal text",
			in:   "a`b",
			want: "a`b",
		},
		{
			name: "fenced block carries the language hint",
			in:   "```go\nx := 1 & 2\n```",
			want: "<pre><code class=\"language-go\">x := 1 &amp; 2</code></pre>",
		},
		{
			name: "fenced block without a language hint",
			in:   "```\nplain\n```",
			want: "<pre><code>plain</code></pre>",
		},
		{
			name: "links become anchors with the url escaped",
			in:   "see [docs](https://x.test/a?b=1&c=2)",
			want: `see <a href="https://x.test/a?b=1&amp;c=2">docs</a>`,
		},
		{
			name: "a link without a label falls back to the url",
			in:   "[](https://x.test)",
			want: `<a href="https://x.test">https://x.test</a>`,
		},
		{
			name: "agent text cannot inject live markup",
			in:   "<b onmouseover=\"x\">not bold</b>",
			want: "&lt;b onmouseover=\"x\"&gt;not bold&lt;/b&gt;",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TelegramHTML(tt.in); got != tt.want {
				t.Errorf("TelegramHTML(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// An agent reply truncated mid-code-block would otherwise ship an unbalanced
// <pre><code>, which Telegram rejects with HTTP 400 — losing the whole answer
// rather than just its formatting.
func TestTelegramHTMLClosesUnterminatedFence(t *testing.T) {
	got := TelegramHTML("intro\n```py\nx = 1")
	want := "intro\n<pre><code class=\"language-py\">x = 1</code></pre>"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
