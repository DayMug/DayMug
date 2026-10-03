package streamcommon

import (
	"strings"
	"testing"
)

// A session-log line carrying a whole file body must survive the scan;
// bufio's 64 KiB default would abort the import with ErrTooLong.
func TestNewTranscriptScanner_ReadsOversizedLine(t *testing.T) {
	line := strings.Repeat("x", 512*1024)
	sc := NewTranscriptScanner(strings.NewReader(line + "\n"))
	if !sc.Scan() {
		t.Fatalf("scan failed: %v", sc.Err())
	}
	if len(sc.Bytes()) != len(line) {
		t.Errorf("read %d bytes, want %d", len(sc.Bytes()), len(line))
	}
	if err := sc.Err(); err != nil {
		t.Errorf("scanner error: %v", err)
	}
}
