package imbridge

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
	"github.com/DayMug/DayMug/backend/internal/store/storetest"
)

// attachmentBridge wires a bridge whose single agent's scope hangs off a real
// home root, and returns both plus the agent's own (deliberately different)
// work_dir — the split the whole layout exists to create.
func attachmentBridge(t *testing.T) (*IMBridge, store.User, string) {
	t.Helper()
	homeRoot := t.TempDir()
	owner := store.User{ID: "owner-1", Username: "alice", Email: "alice@example.com", WorkDir: homeRoot}
	agentUser := store.User{
		ID:      "agent1",
		Name:    "bot",
		OwnerID: owner.ID,
		Email:   owner.Email,
		WorkDir: filepath.Join(homeRoot, "project"),
	}
	ms := storetest.New()
	ms.Users = []store.User{owner, agentUser}
	return &IMBridge{Runtime: &service.Runtime{Store: ms}}, agentUser, homeRoot
}

func imAttachmentsDir(t *testing.T, homeRoot, agentID, platform string) string {
	t.Helper()
	rel, err := service.AgentIMAttachmentsDir(agentID, platform)
	if err != nil {
		t.Fatalf("AgentIMAttachmentsDir: %v", err)
	}
	return service.DaymugPath(homeRoot, rel)
}

// os.MkdirAll happily accepts an existing symlink, so swapping the agent's
// per-platform directory for a link used to redirect every inbound IM
// attachment out of the home root.
func TestMaterializeInboundImages_UploadsDirSymlinkRejected(t *testing.T) {
	b, agentUser, homeRoot := attachmentBridge(t)
	outside := t.TempDir()
	uploadsDir := imAttachmentsDir(t, homeRoot, agentUser.ID, imbot.PlatformSlack)
	if err := os.MkdirAll(filepath.Dir(uploadsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, uploadsDir); err != nil {
		t.Fatal(err)
	}

	png := []byte("\x89PNG\r\n\x1a\n" + "0123456789abcdef")
	msg := imbot.Message{Platform: imbot.PlatformSlack, Attachments: []imbot.Attachment{{
		ID:   "a1",
		Name: "shot.png",
		MIME: "image/png",
		Size: int64(len(png)),
		Download: func(_ context.Context, dst io.Writer) error {
			_, err := dst.Write(png)
			return err
		},
	}}}

	got, _, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget())
	if err == nil {
		t.Fatalf("expected the symlinked uploads dir to be rejected, got %v", got)
	}

	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("attachment leaked outside the home root: %v", entries)
	}
}

// The same flow must still work when the per-platform directory is real.
func TestMaterializeInboundImages_RealUploadsDirAccepted(t *testing.T) {
	b, agentUser, homeRoot := attachmentBridge(t)

	png := []byte("\x89PNG\r\n\x1a\n" + "0123456789abcdef")
	msg := imbot.Message{Platform: imbot.PlatformSlack, Attachments: []imbot.Attachment{{
		ID:   "a1",
		Name: "shot.png",
		MIME: "image/png",
		Size: int64(len(png)),
		Download: func(_ context.Context, dst io.Writer) error {
			_, err := dst.Write(png)
			return err
		},
	}}}

	got, _, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got))
	}
	entries, err := os.ReadDir(imAttachmentsDir(t, homeRoot, agentUser.ID, imbot.PlatformSlack))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 stored file, got %v", entries)
	}
}

// The bytes must land in the owner's home rather than the conversation's
// project directory — that relocation is the whole point of the scope, and a
// write that still lands in the work_dir would go unnoticed otherwise.
func TestMaterializeInboundImages_WritesOutsideTheAgentWorkDir(t *testing.T) {
	b, agentUser, homeRoot := attachmentBridge(t)
	if err := os.MkdirAll(agentUser.WorkDir, 0o755); err != nil {
		t.Fatal(err)
	}

	msg := imbot.Message{Platform: imbot.PlatformSlack, Attachments: []imbot.Attachment{pngAttachment("a")}}
	got, _, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agentUser.WorkDir, service.DaymugDir)); !os.IsNotExist(err) {
		t.Fatalf("a .daymug directory appeared in the agent work_dir (stat err %v)", err)
	}
	wantDir := imAttachmentsDir(t, homeRoot, agentUser.ID, imbot.PlatformSlack)
	if filepath.Dir(got[0].LocalPath) != wantDir {
		t.Fatalf("LocalPath = %q, want a file in %q", got[0].LocalPath, wantDir)
	}
}

// Each connector gets its own directory, and the path handed to the agent has
// to name the same directory the bytes actually landed in — a stale prefix here
// produces a reference the agent cannot open.
func TestMaterializeInboundImages_SplitsByPlatform(t *testing.T) {
	b, agentUser, homeRoot := attachmentBridge(t)

	for _, platform := range []string{imbot.PlatformSlack, imbot.PlatformFeishu} {
		msg := imbot.Message{Platform: platform, Attachments: []imbot.Attachment{pngAttachment("a")}}
		got, _, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget())
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", platform, err)
		}
		if len(got) != 1 {
			t.Fatalf("%s: stored %d attachments, want 1", platform, len(got))
		}
		wantRel, err := service.AgentIMAttachmentsDir(agentUser.ID, platform)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got[0].Path, wantRel+"/") {
			t.Fatalf("%s: path = %q, want prefix %q", platform, got[0].Path, wantRel+"/")
		}
		// Path is home-root-relative (what the file API resolves); LocalPath is
		// what the agent opens. Both have to name the same real file.
		if _, err := os.Stat(service.DaymugPath(homeRoot, got[0].Path)); err != nil {
			t.Fatalf("%s: wire path does not resolve to a file: %v", platform, err)
		}
		if _, err := os.Stat(got[0].LocalPath); err != nil {
			t.Fatalf("%s: local path does not resolve to a file: %v", platform, err)
		}
	}

	// The two platforms must not share a bucket.
	for _, platform := range []string{imbot.PlatformSlack, imbot.PlatformFeishu} {
		entries, err := os.ReadDir(imAttachmentsDir(t, homeRoot, agentUser.ID, platform))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("%s dir holds %d files, want only its own", platform, len(entries))
		}
	}
}

// Two agents under one human share a home root, so only the id segment keeps
// their inbound files apart.
func TestMaterializeInboundImages_SeparatesAgentsSharingAHome(t *testing.T) {
	b, first, homeRoot := attachmentBridge(t)
	second := first
	second.ID = "agent2"

	for _, agentUser := range []store.User{first, second} {
		msg := imbot.Message{Platform: imbot.PlatformSlack, Attachments: []imbot.Attachment{pngAttachment("a")}}
		if _, _, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget()); err != nil {
			t.Fatalf("%s: unexpected error: %v", agentUser.ID, err)
		}
	}
	for _, agentUser := range []store.User{first, second} {
		entries, err := os.ReadDir(imAttachmentsDir(t, homeRoot, agentUser.ID, imbot.PlatformSlack))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("%s holds %d files, want only its own", agentUser.ID, len(entries))
		}
	}
}

// An agent whose owner cannot be resolved has no home root, and must not fall
// back to writing somewhere no read path looks.
func TestMaterializeInboundImages_OrphanAgentRefused(t *testing.T) {
	b, agentUser, _ := attachmentBridge(t)
	agentUser.Email = "ghost@example.com"

	msg := imbot.Message{Platform: imbot.PlatformSlack, Attachments: []imbot.Attachment{pngAttachment("a")}}
	if got, _, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget()); err == nil {
		t.Fatalf("expected an orphan agent to be refused, stored %v", got)
	}
}

// A connector that reports no platform still must not write into the agent
// scope's root, where it would sit alongside the per-platform directories.
func TestIMAttachmentsDirFallsBackWhenPlatformMissing(t *testing.T) {
	scope, err := service.AgentScopeDir("agent1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.AgentIMAttachmentsDir("agent1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got == scope || !strings.HasPrefix(got, scope+"/") {
		t.Fatalf("AgentIMAttachmentsDir(\"\") = %q, want a directory under %q", got, scope)
	}
	escaped, err := service.AgentIMAttachmentsDir("agent1", "../../etc")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(escaped, "..") {
		t.Fatalf("AgentIMAttachmentsDir sanitized to %q, which still traverses", escaped)
	}
}

// An agent id that sanitizes away would silently pool every such agent into one
// directory, which is exactly the sharing the scope exists to prevent.
func TestAgentScopeDirRejectsUnusableID(t *testing.T) {
	for _, id := range []string{"", "   ", "...", "../.."} {
		if got, err := service.AgentScopeDir(id); err == nil {
			t.Fatalf("AgentScopeDir(%q) = %q, want an error", id, got)
		}
	}
}

// pngAttachment builds one valid inbound image attachment.
func pngAttachment(id string) imbot.Attachment {
	png := []byte("\x89PNG\r\n\x1a\n" + "0123456789abcdef")
	return imbot.Attachment{
		ID: id, Name: id + ".png", MIME: "image/png", Size: int64(len(png)),
		Download: func(_ context.Context, dst io.Writer) error {
			_, err := dst.Write(png)
			return err
		},
	}
}

// A Feishu rich-text post can carry an unbounded number of inline images, so
// one message must not be able to spend the whole turn's allowance.
func TestMaterializeInboundImages_CapsAttachmentsPerMessage(t *testing.T) {
	b, agentUser, _ := attachmentBridge(t)
	msg := imbot.Message{}
	for i := range maxInboundAttachmentsPerMessage + 3 {
		msg.Attachments = append(msg.Attachments, pngAttachment(string(rune('a'+i))))
	}

	got, notice, err := b.materializeInboundImages(context.Background(), msg, agentUser, newInboundAttachmentBudget())
	if err != nil {
		t.Fatalf("a cap breach must not fail the whole message: %v", err)
	}
	if len(got) != maxInboundAttachmentsPerMessage {
		t.Fatalf("stored %d attachments, want the per-message cap %d", len(got), maxInboundAttachmentsPerMessage)
	}
	if notice == "" {
		t.Fatal("the agent was given a truncated attachment list with no notice that it is incomplete")
	}
}

// The allowance spans the trigger message plus every backfilled message, so a
// long thread cannot materialize without bound.
func TestMaterializeInboundImages_TurnBudgetIsSharedAcrossMessages(t *testing.T) {
	b, agentUser, _ := attachmentBridge(t)
	budget := &inboundAttachmentBudget{files: 1, bytes: maxInboundAttachmentBytesPerTurn}

	first, _, err := b.materializeInboundImages(context.Background(),
		imbot.Message{Attachments: []imbot.Attachment{pngAttachment("a")}}, agentUser, budget)
	if err != nil || len(first) != 1 {
		t.Fatalf("first message = %v (err %v), want its single attachment stored", first, err)
	}

	second, notice, err := b.materializeInboundImages(context.Background(),
		imbot.Message{Attachments: []imbot.Attachment{pngAttachment("b")}}, agentUser, budget)
	if err != nil {
		t.Fatalf("exhausting the budget must not fail the message: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second message stored %d attachments after the turn budget was spent", len(second))
	}
	if notice == "" {
		t.Fatal("no notice told the agent that the second message's attachments were dropped")
	}
}

func TestInboundAttachmentsSkippedTextSilentWhenNothingSkipped(t *testing.T) {
	if got := inboundAttachmentsSkippedText(0); got != "" {
		t.Fatalf("notice = %q, want silence when every attachment was stored", got)
	}
}
