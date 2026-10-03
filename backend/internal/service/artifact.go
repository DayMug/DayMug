package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/prompts"
)

const (
	artifactDirectivePrefix = prompts.ArtifactMarkerPrefix
	maxPublishedArtifacts   = 10
)

// Extraction deliberately carries no byte cap. It runs before the turn's
// transport is known, so any number chosen here is wrong for every channel at
// once: the web reply uploads nothing at all (the file already sits on local
// disk and is served by path), Slack streams a gigabyte, Feishu stops at 30
// MiB, Telegram at 50 MiB. A single cap here also failed silently — the marker
// was stripped, the answer kept claiming the file was attached, and the agent
// had no way to learn otherwise, so it just re-sent the same oversized file.
// Each connector now enforces its own platform limit and reports the refusal.

// WebArtifactSystemPrompt and IMArtifactSystemPrompt teach agents how to
// explicitly publish generated files; the marker is stripped from the visible
// answer and the path validated before anything is exposed. The text lives in
// internal/prompts (single home for prompt wording); these aliases keep
// existing call sites stable.
const (
	WebArtifactSystemPrompt = prompts.WebArtifact
	IMArtifactSystemPrompt  = prompts.IMArtifact
)

// AbsolutePathsSystemPrompt asks agents to report unambiguous file locations.
// Files are still published through artifact markers rather than filesystem
// links, so showing the path does not bypass the authenticated file endpoint.
const AbsolutePathsSystemPrompt = prompts.AbsolutePaths

// Artifact is one validated agent-generated file. LocalPath is kept off the
// wire; Path and URL are the safe browser-facing representation. URL points at
// the inline-serving read endpoint for renderable images and at the
// attachment-disposition download endpoint for everything else — see
// artifactURL.
type Artifact struct {
	Name      string `json:"name"`
	MIME      string `json:"mime"`
	Path      string `json:"path"`
	URL       string `json:"url"`
	LocalPath string `json:"-"`
	Size      int64  `json:"-"`
}

// ArtifactOutcome is everything one turn's publish markers produced: the files
// that passed validation and the markers that did not. The two travel together
// because a caller that reports only the first half is the reason a dropped
// file reads as a lie in the chat thread.
type ArtifactOutcome struct {
	Published []Artifact
	Rejected  []ArtifactRejection
}

// ArtifactRejection is one publish marker that yielded no file. Path is what
// the marker declared, verbatim, and Reason a short explanation carrying no
// host path — both are shown back to the user, so neither may leak the
// server's filesystem layout. Detail keeps the underlying error, which does
// name absolute paths, for the server log alone.
type ArtifactRejection struct {
	Path   string
	Reason string
	Detail error
}

// String renders the rejection for a chat thread: what the agent asked to
// publish and why it did not arrive.
func (r ArtifactRejection) String() string {
	if r.Path == "" {
		return r.Reason
	}
	return r.Path + "：" + r.Reason
}

// LogLine renders the rejection for the server log, where the resolved host
// path is wanted rather than hidden.
func (r ArtifactRejection) LogLine() string {
	if r.Detail != nil {
		return r.Detail.Error()
	}
	return r.String()
}

// ExtractArtifacts removes strict DAYMUG_ARTIFACT lines and resolves their
// paths under workDir. fileRootDir is the base used by the owner's file API;
// it can be an ancestor of workDir when a conversation runs from a nested cwd.
// Invalid markers are removed but reported, so a bad marker neither fails an
// otherwise valid answer nor disappears without a trace.
func ExtractArtifacts(content, workDir, fileRootDir, ownerID string) (string, []Artifact, []ArtifactRejection) {
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	artifacts := make([]Artifact, 0, 2)
	var rejected []ArtifactRejection
	seen := make(map[string]struct{})
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		path, marked := artifactDirectivePath(trimmed)
		if !marked {
			kept = append(kept, line)
			continue
		}
		if len(artifacts) >= maxPublishedArtifacts {
			reason := fmt.Sprintf("one reply may publish at most %d files", maxPublishedArtifacts)
			rejected = append(rejected, ArtifactRejection{Path: path, Reason: reason, Detail: errors.New(reason)})
			continue
		}
		artifact, reason, err := resolveArtifact(path, workDir, fileRootDir, ownerID)
		if err != nil {
			rejected = append(rejected, ArtifactRejection{Path: path, Reason: reason, Detail: err})
			continue
		}
		if _, ok := seen[artifact.LocalPath]; ok {
			continue
		}
		seen[artifact.LocalPath] = struct{}{}
		artifacts = append(artifacts, artifact)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), artifacts, rejected
}

func artifactDirectivePath(line string) (string, bool) {
	if !strings.HasPrefix(line, artifactDirectivePrefix) || !strings.HasSuffix(line, "]") {
		return "", false
	}
	path := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, artifactDirectivePrefix), "]"))
	if len(path) >= 2 {
		if (path[0] == '"' && path[len(path)-1] == '"') || (path[0] == '\'' && path[len(path)-1] == '\'') {
			path = strings.TrimSpace(path[1 : len(path)-1])
		}
	}
	return path, true
}

// resolveArtifact validates one declared path. It returns a short reason
// alongside the detailed error: the reason is safe to show the user, the error
// names absolute host paths and belongs in the log.
func resolveArtifact(path, workDir, fileRootDir, ownerID string) (Artifact, string, error) {
	if path == "" || workDir == "" {
		return Artifact{}, "publish marker has no path", errors.New("published artifact path is empty")
	}
	if fileRootDir == "" {
		return Artifact{}, "workspace is not resolvable", errors.New("published artifact file root is empty")
	}
	workRoot, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return Artifact{}, "workspace is not resolvable", fmt.Errorf("resolve artifact work directory: %w", err)
	}
	workRoot, err = filepath.Abs(workRoot)
	if err != nil {
		return Artifact{}, "workspace is not resolvable", fmt.Errorf("resolve artifact work directory: %w", err)
	}
	fileRoot, err := filepath.EvalSymlinks(fileRootDir)
	if err != nil {
		return Artifact{}, "workspace is not resolvable", fmt.Errorf("resolve artifact file root: %w", err)
	}
	fileRoot, err = filepath.Abs(fileRoot)
	if err != nil {
		return Artifact{}, "workspace is not resolvable", fmt.Errorf("resolve artifact file root: %w", err)
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(workRoot, target)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return Artifact{}, missingArtifactReason(path), fmt.Errorf("resolve published artifact: %w", err)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return Artifact{}, missingArtifactReason(path), fmt.Errorf("resolve published artifact: %w", err)
	}
	// Artifacts keep the stricter EvalSymlinks above (the file must already
	// exist to be published) and only share the containment test. The target
	// must remain inside both the conversation cwd and the file API root.
	within, err := relIsWithin(workRoot, target)
	if err != nil || !within {
		return Artifact{}, "outside the conversation working directory", errors.New("published artifact escapes the working directory")
	}
	within, err = relIsWithin(fileRoot, target)
	if err != nil || !within {
		return Artifact{}, "outside the accessible file root", errors.New("published artifact escapes the file root")
	}
	rel, err := filepath.Rel(fileRoot, target)
	if err != nil || rel == "." {
		return Artifact{}, "outside the accessible file root", errors.New("published artifact escapes the file root")
	}
	info, err := os.Stat(target)
	if err != nil {
		return Artifact{}, "file not found", fmt.Errorf("stat published artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Artifact{}, "not a regular file", errors.New("published artifact is not a regular file")
	}
	if info.Size() <= 0 {
		return Artifact{}, "file is empty", errors.New("published artifact is empty")
	}
	mime, err := sniffArtifactMIME(target)
	if err != nil {
		return Artifact{}, "file could not be read", err
	}
	wirePath := "./" + filepath.ToSlash(rel)
	return Artifact{
		Name:      filepath.Base(target),
		MIME:      mime,
		Path:      wirePath,
		URL:       artifactURL(ownerID, wirePath, mime),
		LocalPath: target,
		Size:      info.Size(),
	}, "", nil
}

// missingArtifactReason explains a marker whose path resolved to nothing. A
// relative marker is joined onto the conversation cwd, so "file not found" on
// its own reads as "the file you just made does not exist" — the common cause
// is instead a path that lost its directories on the way into the marker
// (".probe/shot.png" published as "shot.png"). Naming the base the lookup used
// tells those two apart without printing a host path.
func missingArtifactReason(path string) string {
	if filepath.IsAbs(path) {
		return "file not found"
	}
	return "file not found under the conversation working directory"
}

// artifactURL routes each artifact to the endpoint that is safe for it. Only
// formats the chat renderer displays inline get the read endpoint, which
// serves the file with its own Content-Type and no Content-Disposition — an
// agent-authored .html or .svg served that way would execute in DayMug's own
// origin. Everything else goes through the download endpoint, whose
// Content-Disposition: attachment makes the browser save it instead.
func artifactURL(ownerID, wirePath, mime string) string {
	endpoint := "/files/download?path="
	if InlineImageMIME(mime) {
		endpoint = "/files/read?path="
	}
	return "/api/users/" + url.PathEscape(ownerID) + endpoint + url.QueryEscape(wirePath)
}

// RebaseArtifactMetadata repairs attachment paths written before artifact URLs
// accounted for conversations running below the owner's file root. It only
// changes metadata when the stored root-relative path is missing but the same
// path resolves to a validated file under workDir; all other metadata fields
// are preserved and the database row is left untouched.
func RebaseArtifactMetadata(metadata json.RawMessage, workDir, fileRootDir, ownerID string) json.RawMessage {
	if len(metadata) == 0 || workDir == "" || fileRootDir == "" {
		return metadata
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &fields); err != nil {
		return metadata
	}
	rawAttachments, ok := fields["attachments"]
	if !ok {
		return metadata
	}
	var attachments []Artifact
	if err := json.Unmarshal(rawAttachments, &attachments); err != nil {
		return metadata
	}

	changed := false
	for i := range attachments {
		if artifactPathExists(fileRootDir, attachments[i].Path) {
			continue
		}
		rebased, _, err := resolveArtifact(attachments[i].Path, workDir, fileRootDir, ownerID)
		if err != nil {
			continue
		}
		attachments[i] = rebased
		changed = true
	}
	if !changed {
		return metadata
	}
	encodedAttachments, err := json.Marshal(attachments)
	if err != nil {
		return metadata
	}
	fields["attachments"] = encodedAttachments
	encoded, err := json.Marshal(fields)
	if err != nil {
		return metadata
	}
	return encoded
}

func artifactPathExists(rootDir, wirePath string) bool {
	if rootDir == "" || wirePath == "" || filepath.IsAbs(wirePath) {
		return false
	}
	root, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		return false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return false
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, wirePath))
	if err != nil {
		return false
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	within, err := relIsWithin(root, target)
	if err != nil || !within {
		return false
	}
	info, err := os.Stat(target)
	return err == nil && info.Mode().IsRegular()
}

func sniffArtifactMIME(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open published artifact: %w", err)
	}
	defer func() { _ = f.Close() }()
	sample := make([]byte, 512)
	n, err := io.ReadFull(f, sample)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("read published artifact: %w", err)
	}
	return http.DetectContentType(sample[:n]), nil
}

// InlineImageMIME reports whether the chat renderer can safely display the
// bytes inline. Deliberately an allowlist of raster formats: image/svg+xml is
// an XML document with script access to the embedding origin, so it is a file
// download like any other artifact.
func InlineImageMIME(mime string) bool {
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return true
	}
	return false
}
