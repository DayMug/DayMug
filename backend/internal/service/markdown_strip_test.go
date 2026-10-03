package service

import "testing"

func TestStripMarkdown(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "just a sentence.", "just a sentence."},
		{"heading", "# Title\nbody", "Title\nbody"},
		{"bold and italic", "this is **bold** and *italic*", "this is bold and italic"},
		{"underscore emphasis", "__strong__ and _soft_", "strong and soft"},
		{"strikethrough", "old ~~gone~~ new", "old gone new"},
		{"inline code", "run `go test` now", "run go test now"},
		{"link keeps text", "see [the docs](https://x.io)", "see the docs"},
		{"image dropped to alt", "![diagram](a.png) shown", "diagram shown"},
		{"unordered list", "- one\n- two", "one\ntwo"},
		{"ordered list", "1. first\n2. second", "first\nsecond"},
		{"blockquote", "> quoted line", "quoted line"},
		{"horizontal rule removed", "above\n\n---\n\nbelow", "above\n\nbelow"},
		{
			"fenced code keeps body drops fence",
			"result:\n```go\nfmt.Println(1)\n```\ndone",
			"result:\nfmt.Println(1)\ndone",
		},
		{"trims surrounding whitespace", "\n\n  hi  \n\n", "hi"},
		{
			"combined markup",
			"## Done\n\n- Fixed **bug** in `parser`\n- See [PR](http://x)",
			"Done\n\nFixed bug in parser\nSee PR",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripMarkdown(c.in); got != c.want {
				t.Errorf("stripMarkdown(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
