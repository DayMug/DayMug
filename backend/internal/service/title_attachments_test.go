package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/agent/agenttest"
	"github.com/DayMug/DayMug/backend/internal/store"
)

func writeUpload(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExpandTitleAttachmentLines(t *testing.T) {
	root := t.TempDir()
	uploads := filepath.Join(root, "uploads")
	outside := t.TempDir()

	md := writeUpload(t, uploads, "a.md", []byte("# 重构任务 02\n\n统一提示词读取"))
	bin := writeUpload(t, uploads, "b.md", []byte("abc\x00def"))
	img := writeUpload(t, uploads, "c.png", []byte("# not text"))
	secret := writeUpload(t, outside, "secret.md", []byte("TOP SECRET"))

	tests := []struct {
		name      string
		prompt    string
		want      string // substring that must appear
		notWant   string // substring that must not appear
		unchanged bool
	}{
		{name: "text upload expands", prompt: md, want: "[attachment a.md] # 重构任务 02 统一提示词读取", notWant: uploads},
		{name: "typed text is kept beside the attachment", prompt: "fix this\n\n" + md, want: "fix this"},
		{name: "path outside uploads is never read", prompt: secret, notWant: "TOP SECRET", unchanged: true},
		{name: "dotdot escape is never read", prompt: filepath.Join(uploads, "..", "..", filepath.Base(outside), "secret.md"), notWant: "TOP SECRET"},
		{name: "binary content is left alone", prompt: bin, unchanged: true},
		{name: "non-text extension is left alone", prompt: img, unchanged: true},
		{name: "missing file is left alone", prompt: filepath.Join(uploads, "gone.md"), unchanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandTitleAttachmentLines(tt.prompt, uploads)
			if tt.unchanged && got != tt.prompt {
				t.Fatalf("got %q, want unchanged %q", got, tt.prompt)
			}
			if tt.want != "" && !strings.Contains(got, tt.want) {
				t.Fatalf("got %q, want substring %q", got, tt.want)
			}
			if tt.notWant != "" && strings.Contains(got, tt.notWant) {
				t.Fatalf("got %q, must not contain %q", got, tt.notWant)
			}
		})
	}
}

func TestExpandTitleAttachmentLinesCapsFileCount(t *testing.T) {
	uploads := t.TempDir()
	var lines []string
	for _, n := range []string{"1.md", "2.md", "3.md", "4.md"} {
		lines = append(lines, writeUpload(t, uploads, n, []byte("body "+n)))
	}
	got := expandTitleAttachmentLines(strings.Join(lines, "\n"), uploads)
	if c := strings.Count(got, "[attachment "); c != titleAttachmentMaxFiles {
		t.Fatalf("expanded %d attachments, want %d: %q", c, titleAttachmentMaxFiles, got)
	}
}

func TestReadTitleExcerptTrimsPartialRuneAtWindowEnd(t *testing.T) {
	dir := t.TempDir()
	// Fill the read window so it ends in the middle of a 3-byte rune.
	data := []byte(strings.Repeat("a", titleAttachmentReadBytes-1) + "中")
	got, ok := readTitleExcerpt(writeUpload(t, dir, "x.md", data))
	if !ok || got == "" {
		t.Fatalf("partial trailing rune was mistaken for binary: ok=%v", ok)
	}
}

// The reported bug: a conversation whose only user message is an uploaded
// file's path never got a title. The generator must now see the file's head.
func TestMaybeAutoTitleFeedsAttachmentExcerptToGenerator(t *testing.T) {
	s := newMessagePersistTestStore(t)
	ctx := context.Background()
	home := t.TempDir()
	if err := s.CreateUser(ctx, store.User{ID: "owner", Name: "Owner", Username: "owner", WorkDir: home}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	rel, err := AgentUploadsDir("owner")
	if err != nil {
		t.Fatal(err)
	}
	att := writeUpload(t, DaymugPath(home, rel), "20260924-02.md", []byte("# 统一提示词读取与渲染层"))
	if err := s.CreateConversation(ctx, "conv1", "", "owner", t.TempDir(), "claude", ""); err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := s.SaveMessage(ctx, store.Message{ID: "m1", ConversationID: "conv1", Role: "user", Content: att}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	titler := &agenttest.FakeTitler{Out: "统一提示词层"}
	p := &MessagePersister{Store: s, TitleGen: titler}
	p.MaybeAutoTitle("conv1")

	if titler.CallCount() != 1 {
		t.Fatalf("generator calls = %d, want 1", titler.CallCount())
	}
	if got := titler.GotInput[0][0]; !strings.Contains(got, "统一提示词读取与渲染层") {
		t.Fatalf("generator saw %q, want the attachment heading", got)
	}
}
