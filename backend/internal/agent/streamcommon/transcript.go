package streamcommon

import (
	"bufio"
	"io"
)

// transcriptScanBuf bounds a single session-log line. Both CLIs write
// JSONL whose lines can be huge — a Write tool_use block or a
// function_call's arguments carries the whole file body inline — and
// bufio.Scanner's 64 KiB default aborts the scan with ErrTooLong on the
// first oversized line, silently truncating the import.
const transcriptScanBuf = 16 * 1024 * 1024

// NewTranscriptScanner returns a scanner sized for session-log lines.
func NewTranscriptScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), transcriptScanBuf)
	return sc
}
