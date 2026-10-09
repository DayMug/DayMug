package service

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/DayMug/DayMug/backend/internal/store"
)

const (
	// titleAttachmentMaxFiles bounds how many attachments of one message get
	// opened; a drag-drop of fifty files needs a gist, not fifty reads.
	titleAttachmentMaxFiles = 3
	// titleAttachmentReadBytes is the read window per file. Generous against
	// titleAttachmentExcerptRunes so a multi-byte head still yields the cap.
	titleAttachmentReadBytes = 4096
	// titleAttachmentExcerptRunes keeps message + excerpts inside
	// agent.TitlePromptMaxRunes (600) so the transcript truncation marker
	// never eats the part that carries the meaning.
	titleAttachmentExcerptRunes = 160
)

// titleTextExts are the attachment types worth excerpting. Anything else
// (images, PDFs, archives) stays a bare path: reading it would feed the title
// model bytes, not a topic.
var titleTextExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".log": true,
	".json": true, ".yaml": true, ".yml": true, ".csv": true, ".tsv": true,
	".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true, ".vue": true,
	".sql": true, ".html": true, ".xml": true, ".toml": true, ".sh": true,
}

// expandTitleAttachments rewrites the lines of each prompt that are paths of
// text files in the conversation agent's uploads directory into
// "[attachment <name>] <head of the file>".
//
// Why: the composer sends an attachment as a bare absolute path. A prompt
// that is nothing but `…/uploads/20260924-02.md` gives the title model no
// topic, so it correctly abstains — and because retries re-read the same
// user messages, the conversation stayed untitled forever. The file's own
// heading is the real subject.
//
// Only paths inside the agent's own uploads directory are opened. The path
// arrives in user-controlled message text, so without that boundary anyone
// could have the daymug process read an arbitrary host file into a title.
// Best effort throughout: any failure leaves the line as it was.
func expandTitleAttachments(ctx context.Context, s store.Store, conv store.Conversation, prompts []string) []string {
	if s == nil || conv.UserID == "" {
		return prompts
	}
	agentUser, err := s.GetUser(ctx, conv.UserID)
	if err != nil {
		return prompts
	}
	homeOwner, err := AgentHomeOwner(ctx, s, agentUser)
	if err != nil {
		return prompts
	}
	rel, err := AgentUploadsDir(agentUser.ID)
	if err != nil {
		return prompts
	}
	uploadsDir := DaymugPath(homeOwner.WorkDir, rel)

	out := make([]string, len(prompts))
	for i, p := range prompts {
		out[i] = expandTitleAttachmentLines(p, uploadsDir)
	}
	return out
}

func expandTitleAttachmentLines(prompt, uploadsDir string) string {
	lines := strings.Split(prompt, "\n")
	opened := 0
	for i, line := range lines {
		if opened >= titleAttachmentMaxFiles {
			break
		}
		path := strings.TrimSpace(line)
		if !filepath.IsAbs(path) || !titleTextExts[strings.ToLower(filepath.Ext(path))] {
			continue
		}
		if within, err := PathWithin(uploadsDir, path); err != nil || !within {
			continue
		}
		excerpt, ok := readTitleExcerpt(path)
		if !ok {
			continue
		}
		opened++
		lines[i] = "[attachment " + filepath.Base(path) + "] " + excerpt
	}
	return strings.Join(lines, "\n")
}

// readTitleExcerpt returns the flattened head of a regular text file, or
// ok=false for anything missing, non-regular, binary, or blank.
func readTitleExcerpt(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	buf, err := io.ReadAll(io.LimitReader(f, titleAttachmentReadBytes))
	if err != nil || len(buf) == 0 {
		return "", false
	}
	// A window that ends mid-rune would read as invalid UTF-8 and be
	// mistaken for binary; trim the partial tail first.
	for trim := 0; trim < utf8.UTFMax && !utf8.Valid(buf); trim++ {
		buf = buf[:len(buf)-1]
	}
	if !utf8.Valid(buf) || strings.ContainsRune(string(buf), 0) {
		return "", false
	}
	text := strings.Join(strings.Fields(string(buf)), " ")
	if text == "" {
		return "", false
	}
	if r := []rune(text); len(r) > titleAttachmentExcerptRunes {
		text = string(r[:titleAttachmentExcerptRunes]) + "…"
	}
	return text, true
}
