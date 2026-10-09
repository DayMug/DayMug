package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/service/imbridge"
)

func TestBuildWebUploadMetadataCanonicalizesAttachmentPreviews(t *testing.T) {
	uploads, err := service.AgentUploadsDir("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := buildWebUploadMetadata("agent-1", "owner 1", []webUploadAttachment{
		{Name: "shot.png", MIME: "image/png", Path: uploads + "/shot.png"},
		{Name: "notes.txt", MIME: "text/plain", Path: "./" + uploads + "/notes.txt"},
		{Name: "escape.png", MIME: "image/png", Path: uploads + "/../escape.png"},
	})
	if err != nil {
		t.Fatalf("build metadata: %v", err)
	}
	var got imbridge.IMMessageMetadata
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if len(got.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(got.Attachments))
	}
	attachment := got.Attachments[0]
	if attachment.Path != uploads+"/shot.png" {
		t.Errorf("path = %q", attachment.Path)
	}
	// The read URL addresses the home owner, whose root the stored path is
	// relative to — and the id is escaped, since it becomes a path segment.
	if !strings.Contains(attachment.URL, "/api/users/owner%201/files/read?path=") {
		t.Errorf("unexpected canonical URL %q", attachment.URL)
	}
	file := got.Attachments[1]
	if file.Name != "notes.txt" || file.MIME != "text/plain" {
		t.Errorf("file attachment = %+v", file)
	}
	if file.Path != uploads+"/notes.txt" {
		t.Errorf("file path = %q", file.Path)
	}
}

// The client echoes back whatever it likes, so a path naming another agent's
// scope — or anything else under the shared home root — must not survive into
// a read URL the owner is authorized for.
func TestBuildWebUploadMetadataRejectsForeignScopes(t *testing.T) {
	other, err := service.AgentUploadsDir("agent-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		other + "/secret.png",
		".ssh/id_rsa",
		"/etc/passwd",
		service.DaymugDir + "/agents/agent-1/case/C1-T1.md",
	} {
		raw, err := buildWebUploadMetadata("agent-1", "owner-1", []webUploadAttachment{
			{Name: "x", MIME: "image/png", Path: path},
		})
		if err != nil {
			t.Fatalf("build metadata: %v", err)
		}
		if raw != nil {
			t.Errorf("path %q produced metadata %s, want it dropped", path, raw)
		}
	}
}
