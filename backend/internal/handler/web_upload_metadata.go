package handler

import (
	"encoding/json"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/service"

	"github.com/DayMug/DayMug/backend/internal/service/imbridge"
)

const maxWebUploadAttachments = 20

// webUploadAttachment is the small client-to-server shape sent alongside a
// prompt. URL is deliberately absent: the server derives it from the
// conversation owner so history can never persist an arbitrary remote image.
type webUploadAttachment struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Path string `json:"path"`
}

// buildWebUploadMetadata turns the client's echo of an upload response into
// persisted message metadata.
//
// agentID is the conversation's agent and homeOwnerID the human whose home
// root anchors that agent's scope. Every accepted path must sit directly in
// that agent's uploads directory: the client is free to send any string here,
// so pinning the prefix to a scope the caller derived server-side is what
// stops a crafted path from addressing another agent's files — or anything
// else under the home root — through the read URL built below.
func buildWebUploadMetadata(agentID, homeOwnerID string, input []webUploadAttachment) (json.RawMessage, error) {
	if agentID == "" || homeOwnerID == "" || len(input) == 0 {
		return nil, nil
	}
	uploadsDir, err := service.AgentUploadsDir(agentID)
	if err != nil {
		return nil, nil
	}
	limit := len(input)
	if limit > maxWebUploadAttachments {
		limit = maxWebUploadAttachments
	}
	attachments := make([]imbridge.IMImageMetadata, 0, limit)
	for _, item := range input[:limit] {
		mime := strings.TrimSpace(item.MIME)
		clean := path.Clean(filepath.ToSlash(strings.TrimSpace(item.Path)))
		if path.Dir(clean) != uploadsDir || path.Base(clean) == "." {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = path.Base(clean)
		}
		attachments = append(attachments, imbridge.IMImageMetadata{
			Name: name,
			MIME: mime,
			Path: clean,
			URL:  "/api/users/" + url.PathEscape(homeOwnerID) + "/files/read?path=" + url.QueryEscape(clean),
		})
	}
	if len(attachments) == 0 {
		return nil, nil
	}
	return json.Marshal(imbridge.IMMessageMetadata{Attachments: attachments})
}
