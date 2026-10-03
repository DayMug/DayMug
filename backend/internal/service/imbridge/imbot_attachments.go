package imbridge

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DayMug/DayMug/backend/internal/imbot"
	"github.com/DayMug/DayMug/backend/internal/prompts"

	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

var errInboundImageTooLarge = errors.New("IM 附件超过 25 MiB 限制")

// Inbound attachment caps. The outbound side already bounds one agent reply to
// maxPublishedArtifacts (10) files of maxPublishedArtifactSize (25 MiB) each in
// service/artifact.go; inbound reuses those numbers instead of inventing a
// second scale, so both directions of one IM turn cost the same at worst.
//
// The per-message cap alone is not enough: a Feishu post can embed unboundedly
// many images, and persistThreadContext replays materialization over every
// message a thread backfill returns (up to 1000 on Feishu, unbounded on Slack).
// The per-turn caps are what actually bound one inbound turn.
const (
	// maxInboundAttachmentsPerMessage mirrors maxPublishedArtifacts.
	maxInboundAttachmentsPerMessage = 10
	// maxInboundAttachmentsPerTurn budgets the trigger message plus the whole
	// backfilled history window. Five messages' worth of the per-message cap:
	// enough that a normal conversation with screenshots is never trimmed,
	// small enough that a 1000-message backfill cannot write thousands of files.
	maxInboundAttachmentsPerTurn = 50
	// maxInboundAttachmentBytesPerTurn is the outbound batch ceiling
	// (10 x 25 MiB) applied to one inbound turn.
	maxInboundAttachmentBytesPerTurn int64 = maxInboundAttachmentsPerMessage * service.DefaultUploadMaxBytes
)

// inboundAttachmentBudget is the per-turn allowance shared by the triggering
// message and every message the thread backfill materializes. Not safe for
// concurrent use; one turn materializes attachments sequentially.
type inboundAttachmentBudget struct {
	files int
	bytes int64
}

func newInboundAttachmentBudget() *inboundAttachmentBudget {
	return &inboundAttachmentBudget{
		files: maxInboundAttachmentsPerTurn,
		bytes: maxInboundAttachmentBytesPerTurn,
	}
}

// inboundAttachmentsSkippedText gates the notice on an actual cap breach. A
// breach must not fail the whole message — the attachments already on disk are
// still useful, and a turn that errors out answers nobody — but the agent must
// not silently reason over a partial set either, so the notice rides in as
// visible prompt text the way prompts.FeishuHistoryTruncated does for history.
func inboundAttachmentsSkippedText(skipped int) string {
	if skipped <= 0 {
		return ""
	}
	return prompts.InboundAttachmentsSkipped(skipped)
}

// appendInboundNotice attaches a synthetic notice to the message text so it
// reaches both the persisted transcript and the CLI prompt.
func appendInboundNotice(text, notice string) string {
	if notice == "" {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return notice
	}
	return text + "\n" + notice
}

// IMImageMetadata describes one persisted inbound attachment. Path addresses
// it for the file API — home-root-relative, matching URL's user id — while
// LocalPath is the absolute path an agent must be given to open it, since the
// agent's cwd is a project directory somewhere below that root. LocalPath
// stays off the wire for the same reason service.Artifact's does: the browser
// has no use for the server's filesystem layout.
type IMImageMetadata struct {
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	Path      string `json:"path"`
	URL       string `json:"url"`
	LocalPath string `json:"-"`
}

type IMSenderMetadata struct {
	Platform string `json:"platform"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
}

type IMMessageMetadata struct {
	Attachments []IMImageMetadata `json:"attachments,omitempty"`
	Sender      *IMSenderMetadata `json:"sender,omitempty"`
}

// materializeInboundImages persists one message's attachments under the agent's
// DayMug scope, drawing from the per-turn budget. It returns the saved metadata
// and a notice describing what a cap forced it to skip (empty when nothing was
// skipped); a returned error means the message could not be ingested at all.
//
// Caps degrade, failures fail: exceeding a count/size limit skips the offending
// attachment and keeps going, while a broken connector, an unresolvable home
// root or a filesystem error still aborts the message.
func (b *IMBridge) materializeInboundImages(ctx context.Context, msg imbot.Message, agentUser store.User, budget *inboundAttachmentBudget) ([]IMImageMetadata, string, error) {
	if len(msg.Attachments) == 0 {
		return nil, "", nil
	}
	if budget == nil {
		budget = newInboundAttachmentBudget()
	}
	homeOwner, err := service.AgentHomeOwner(ctx, b.Store, agentUser)
	if err != nil {
		return nil, "", fmt.Errorf("解析 agent 归属用户的家目录: %w", err)
	}
	relDir, err := service.AgentIMAttachmentsDir(agentUser.ID, msg.Platform)
	if err != nil {
		return nil, "", fmt.Errorf("解析 IM 附件目录: %w", err)
	}
	uploadsDir := service.DaymugPath(homeOwner.WorkDir, relDir)
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("创建 IM 附件目录: %w", err)
	}
	// MkdirAll is happy when any segment of the scope is already a symlink to
	// some directory elsewhere on the host, which would silently redirect every
	// inbound attachment out of the home root. Verify after creating so the
	// check sees the final state.
	if within, err := service.PathWithin(homeOwner.WorkDir, uploadsDir); err != nil || !within {
		return nil, "", fmt.Errorf("IM 附件目录 %s 解析后逃逸出家目录", relDir)
	}
	// Nothing else ever revisits this directory, so the write path is where the
	// expired files from previous turns get collected.
	service.MaybePruneUploads(uploadsDir)

	result := make([]IMImageMetadata, 0, len(msg.Attachments))
	createdPaths := make([]string, 0, len(msg.Attachments))
	skipped := 0
	completed := false
	defer func() {
		if completed {
			return
		}
		for _, path := range createdPaths {
			_ = os.Remove(path)
		}
	}()
	for i, attachment := range msg.Attachments {
		// The remaining allowance for this one file: the per-file cap, clamped
		// by whatever is left of the turn's byte budget. Clamping (rather than
		// only accounting afterwards) is what makes the per-turn cap real —
		// a connector that under-reports Size still cannot overshoot it.
		perFile := int64(service.DefaultUploadMaxBytes)
		if budget.bytes < perFile {
			perFile = budget.bytes
		}
		if len(result) >= maxInboundAttachmentsPerMessage || budget.files <= 0 || perFile <= 0 {
			skipped += len(msg.Attachments) - i
			break
		}
		if attachment.Download == nil {
			return nil, "", fmt.Errorf("IM 图片 #%d 缺少下载器", i+1)
		}
		if attachment.Size > perFile {
			skipped++
			continue
		}

		tmp, err := os.CreateTemp(uploadsDir, ".imbot-attachment-*")
		if err != nil {
			return nil, "", fmt.Errorf("创建 IM 附件临时文件: %w", err)
		}
		tmpPath := tmp.Name()
		limited := &inboundImageWriter{dst: tmp, remaining: perFile}
		downloadErr := attachment.Download(ctx, limited)
		closeErr := tmp.Close()
		if downloadErr != nil || closeErr != nil {
			_ = os.Remove(tmpPath)
			if errors.Is(downloadErr, errInboundImageTooLarge) {
				skipped++
				continue
			}
			if downloadErr != nil {
				return nil, "", fmt.Errorf("下载 IM 附件 %q: %w", attachment.Name, downloadErr)
			}
			return nil, "", fmt.Errorf("关闭 IM 附件 %q: %w", attachment.Name, closeErr)
		}
		written := perFile - limited.remaining

		mime, err := sniffInboundAttachment(tmpPath, attachment.MIME)
		if err != nil {
			_ = os.Remove(tmpPath)
			return nil, "", fmt.Errorf("校验 IM 附件 %q: %w", attachment.Name, err)
		}
		cleanName := inboundAttachmentName(attachment.Name, attachment.ID, mime)
		storedName, dst, out, err := service.OpenUniqueUpload(uploadsDir, time.Now().Format("20060102150405"), cleanName)
		if err != nil {
			_ = os.Remove(tmpPath)
			return nil, "", fmt.Errorf("创建 IM 附件 %q: %w", attachment.Name, err)
		}
		src, err := os.Open(tmpPath)
		if err == nil {
			_, err = io.Copy(out, src)
			_ = src.Close()
		}
		closeErr = out.Close()
		_ = os.Remove(tmpPath)
		if err != nil || closeErr != nil {
			_ = os.Remove(dst)
			if err != nil {
				return nil, "", fmt.Errorf("保存 IM 附件 %q: %w", attachment.Name, err)
			}
			return nil, "", fmt.Errorf("关闭 IM 附件 %q: %w", attachment.Name, closeErr)
		}

		budget.files--
		budget.bytes -= written
		wirePath := relDir + "/" + storedName
		result = append(result, IMImageMetadata{
			Name:      firstNonEmptyString(attachment.Name, storedName),
			MIME:      mime,
			Path:      wirePath,
			URL:       "/api/users/" + url.PathEscape(homeOwner.ID) + "/files/read?path=" + url.QueryEscape(wirePath),
			LocalPath: dst,
		})
		createdPaths = append(createdPaths, dst)
	}
	completed = true
	return result, inboundAttachmentsSkippedText(skipped), nil
}

func sniffInboundAttachment(path, declaredMIME string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	sample := make([]byte, 512)
	n, err := f.Read(sample)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	detected := http.DetectContentType(sample[:n])
	declaredMIME = strings.ToLower(strings.TrimSpace(strings.SplitN(declaredMIME, ";", 2)[0]))
	if strings.HasPrefix(declaredMIME, "image/") {
		switch detected {
		case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
			return detected, nil
		default:
			if declaredMIME == "image/svg+xml" {
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					return "", err
				}
				if isSVG(f) {
					return declaredMIME, nil
				}
			}
			return "", fmt.Errorf("附件声明为图片但实际类型为 %s", detected)
		}
	}
	if detected == "application/octet-stream" && declaredMIME != "" {
		return declaredMIME, nil
	}
	return detected, nil
}

func isSVG(r io.Reader) bool {
	decoder := xml.NewDecoder(r)
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if start, ok := token.(xml.StartElement); ok {
			return strings.EqualFold(start.Name.Local, "svg")
		}
	}
}

type inboundImageWriter struct {
	dst       io.Writer
	remaining int64
}

func (w *inboundImageWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		written := 0
		if w.remaining > 0 {
			n, err := w.dst.Write(p[:w.remaining])
			written = n
			w.remaining -= int64(n)
			if err != nil {
				return n, err
			}
		}
		return written, errInboundImageTooLarge
	}
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func inboundAttachmentName(name, id, mime string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." {
		base = firstNonEmptyString(id, "attachment")
	}
	base = service.SanitizeUploadSegment(base)
	base = strings.Trim(base, "-.")
	if base == "" {
		base = "attachment"
	}
	if filepath.Ext(base) == "" {
		base += service.ExtensionFromMIME(mime)
	}
	return base
}
