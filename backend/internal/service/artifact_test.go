package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractArtifactsPublishesOnlyFilesInsideWorkDir(t *testing.T) {
	workDir := t.TempDir()
	imagePath := filepath.Join(workDir, "chart.png")
	if err := os.WriteFile(imagePath, []byte("\x89PNG\r\n\x1a\nartifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	content := "done\n[DAYMUG_ARTIFACT chart.png]\n[DAYMUG_ARTIFACT " + outside + "]"
	cleaned, artifacts, errs := ExtractArtifacts(content, workDir, workDir, "agent-1")
	if cleaned != "done" {
		t.Fatalf("cleaned content = %q", cleaned)
	}
	if len(artifacts) != 1 || artifacts[0].LocalPath != imagePath {
		t.Fatalf("artifacts = %+v", artifacts)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Detail.Error(), "escapes") {
		t.Fatalf("errors = %v", errs)
	}
	// The rejection is shown back to the user, so it echoes the marker verbatim
	// and explains itself in words rather than in resolved host paths.
	if errs[0].Path != outside || errs[0].Reason != "outside the conversation working directory" {
		t.Fatalf("rejection = %+v", errs[0])
	}
	if artifacts[0].URL == "" || artifacts[0].Path != "./chart.png" {
		t.Fatalf("wire artifact = %+v", artifacts[0])
	}
}

// Only inline-renderable rasters may use the read endpoint; anything the
// browser might execute in DayMug's origin has to arrive as a download.
func TestArtifactURLRoutesNonImagesThroughDownload(t *testing.T) {
	workDir := t.TempDir()
	files := map[string][]byte{
		"chart.png":  []byte("\x89PNG\r\n\x1a\nartifact"),
		"notes.txt":  []byte("plain text artifact"),
		"page.html":  []byte("<html><body>artifact</body></html>"),
		"report.pdf": []byte("%PDF-1.4\nartifact"),
	}
	content := "done"
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(workDir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
		content += "\n[DAYMUG_ARTIFACT " + name + "]"
	}

	_, artifacts, errs := ExtractArtifacts(content, workDir, workDir, "agent-1")
	if len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	if len(artifacts) != len(files) {
		t.Fatalf("artifacts = %+v", artifacts)
	}
	for _, artifact := range artifacts {
		wantRead := artifact.Name == "chart.png"
		gotRead := strings.Contains(artifact.URL, "/files/read?path=")
		gotDownload := strings.Contains(artifact.URL, "/files/download?path=")
		if gotRead != wantRead || gotDownload == wantRead {
			t.Errorf("%s (%s) URL = %q, want read endpoint = %v", artifact.Name, artifact.MIME, artifact.URL, wantRead)
		}
	}
}

func TestExtractArtifactsUsesFileRootForNestedConversationWorkDir(t *testing.T) {
	fileRoot := t.TempDir()
	workDir := filepath.Join(fileRoot, "projects", "report")
	if err := os.MkdirAll(filepath.Join(workDir, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(workDir, "output", "preview.png")
	if err := os.WriteFile(imagePath, []byte("\x89PNG\r\n\x1a\nartifact"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, artifacts, errs := ExtractArtifacts(
		"[DAYMUG_ARTIFACT ./output/preview.png]",
		workDir,
		fileRoot,
		"agent-1",
	)
	if len(errs) != 0 || len(artifacts) != 1 {
		t.Fatalf("artifacts = %+v, errors = %v", artifacts, errs)
	}
	artifact := artifacts[0]
	wantPath := "./projects/report/output/preview.png"
	if artifact.Path != wantPath || !strings.Contains(artifact.URL, "path=.%2Fprojects%2Freport%2Foutput%2Fpreview.png") {
		t.Fatalf("wire artifact = %+v, want path %q", artifact, wantPath)
	}
}

func TestRebaseArtifactMetadataRepairsNestedConversationPath(t *testing.T) {
	fileRoot := t.TempDir()
	workDir := filepath.Join(fileRoot, "pdf-export")
	if err := os.MkdirAll(filepath.Join(workDir, "work", "thumbs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(workDir, "work", "thumbs", "page.png"),
		[]byte("\x89PNG\r\n\x1a\nartifact"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	metadata := json.RawMessage(`{"usage":{"total":7},"attachments":[{"name":"page.png","mime":"image/png","path":"./work/thumbs/page.png","url":"/api/users/agent-1/files/read?path=.%2Fwork%2Fthumbs%2Fpage.png"}]}`)

	rebased := RebaseArtifactMetadata(metadata, workDir, fileRoot, "agent-1")
	var got struct {
		Usage struct {
			Total int `json:"total"`
		} `json:"usage"`
		Attachments []Artifact `json:"attachments"`
	}
	if err := json.Unmarshal(rebased, &got); err != nil {
		t.Fatal(err)
	}
	if got.Usage.Total != 7 || len(got.Attachments) != 1 {
		t.Fatalf("metadata = %s", rebased)
	}
	attachment := got.Attachments[0]
	if attachment.Path != "./pdf-export/work/thumbs/page.png" ||
		!strings.Contains(attachment.URL, "path=.%2Fpdf-export%2Fwork%2Fthumbs%2Fpage.png") {
		t.Fatalf("attachment = %+v", attachment)
	}
}

func TestAssistantMetadataPublishesOrdinaryFilesToo(t *testing.T) {
	metadata := BuildAssistantMetadataFor(AssistantMetadata{Artifacts: []Artifact{
		{Name: "chart.png", MIME: "image/png", Path: "./chart.png", URL: "/chart"},
		{Name: "report.pdf", MIME: "application/pdf", Path: "./report.pdf", URL: "/report"},
	}})
	var decoded struct {
		Attachments []Artifact `json:"attachments"`
	}
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Attachments) != 2 || decoded.Attachments[1].Name != "report.pdf" {
		t.Fatalf("metadata attachments = %+v", decoded.Attachments)
	}
}

// A marker naming a file that was never written must not vanish into the log:
// the answer above it says the file is attached, and the agent — which cannot
// see the drop — will otherwise re-send the same broken marker forever. The
// reason shown must also stay free of the resolved host path.
func TestExtractArtifactsReportsMissingFileWithoutHostPath(t *testing.T) {
	workDir := t.TempDir()

	cleaned, artifacts, rejected := ExtractArtifacts("done\n[DAYMUG_ARTIFACT ./never-written.pdf]", workDir, workDir, "agent-1")
	if cleaned != "done" || len(artifacts) != 0 {
		t.Fatalf("cleaned = %q artifacts = %+v", cleaned, artifacts)
	}
	if len(rejected) != 1 {
		t.Fatalf("rejected = %+v, want exactly one report", rejected)
	}
	if rejected[0].Path != "./never-written.pdf" || rejected[0].Reason != "file not found under the conversation working directory" {
		t.Fatalf("rejection = %+v", rejected[0])
	}
	if notice := rejected[0].String(); strings.Contains(notice, workDir) {
		t.Fatalf("user-facing rejection %q leaks the host path", notice)
	}
	if line := rejected[0].LogLine(); !strings.Contains(line, workDir) {
		t.Fatalf("log line %q dropped the resolved path operators need", line)
	}
}

// A marker that lost its directories is the failure this reason exists for: the
// file really is inside the working directory, just not where the shortened
// path points. Saying only "file not found" sent users looking for a file the
// agent had in fact written, so the relative case names the base it searched.
// An absolute marker gets the plain wording — there is no cwd to blame.
func TestExtractArtifactsDistinguishesRelativeAndAbsoluteMisses(t *testing.T) {
	workDir := t.TempDir()
	nested := filepath.Join(workDir, ".probe")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "shot.png"), []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		marker string
		want   string
	}{
		{
			name:   "basename dropped the directory",
			marker: "[DAYMUG_ARTIFACT shot.png]",
			want:   "file not found under the conversation working directory",
		},
		{
			name:   "absolute path that does not exist",
			marker: "[DAYMUG_ARTIFACT " + filepath.Join(workDir, "shot.png") + "]",
			want:   "file not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, artifacts, rejected := ExtractArtifacts("done\n"+tc.marker, workDir, workDir, "agent-1")
			if len(artifacts) != 0 || len(rejected) != 1 {
				t.Fatalf("artifacts = %+v rejected = %+v", artifacts, rejected)
			}
			if rejected[0].Reason != tc.want {
				t.Fatalf("reason = %q, want %q", rejected[0].Reason, tc.want)
			}
		})
	}
}

// The same file publishes cleanly once the marker keeps the directory segment
// the shortened form dropped — the fix the prompt now asks agents for.
func TestExtractArtifactsPublishesNestedRelativePath(t *testing.T) {
	workDir := t.TempDir()
	nested := filepath.Join(workDir, ".probe")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "shot.png"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, artifacts, rejected := ExtractArtifacts("done\n[DAYMUG_ARTIFACT .probe/shot.png]", workDir, workDir, "agent-1")
	if len(rejected) != 0 {
		t.Fatalf("rejected = %+v, want none", rejected)
	}
	if len(artifacts) != 1 || artifacts[0].Path != "./.probe/shot.png" {
		t.Fatalf("artifacts = %+v", artifacts)
	}
}

// Extraction must not second-guess the transport: a 30 MiB artifact is fine for
// Slack (1 GiB) and for the web reply (no upload at all), and the cap that used
// to sit here silently dropped it for every channel at once.
func TestExtractArtifactsPublishesFilesLargerThanAnyIMCap(t *testing.T) {
	workDir := t.TempDir()
	big := filepath.Join(workDir, "candidate.pdf")
	body := append([]byte("%PDF-1.4\n"), make([]byte, 30<<20)...)
	if err := os.WriteFile(big, body, 0o600); err != nil {
		t.Fatal(err)
	}

	_, artifacts, rejected := ExtractArtifacts("done\n[DAYMUG_ARTIFACT candidate.pdf]", workDir, workDir, "agent-1")
	if len(rejected) != 0 {
		t.Fatalf("rejected = %+v, want none — size belongs to the connector, not to extraction", rejected)
	}
	if len(artifacts) != 1 || artifacts[0].Size != int64(len(body)) {
		t.Fatalf("artifacts = %+v", artifacts)
	}
}
