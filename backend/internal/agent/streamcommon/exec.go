package streamcommon

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DayMug/DayMug/backend/internal/agent"
)

// SetEnv replaces or appends key=value in env; later wins.
func SetEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

// accountEnv is the credential layer both adapters share, in precedence
// order (later wins):
//  1. Server process env, so PATH, locale, etc. carry through.
//  2. opts.AccountEnv from YAML — operator-provided per-account overrides
//     (ANTHROPIC_API_KEY, OPENAI_API_KEY, …).
//  3. opts.ConfigDir under configDirEnv — the account's credential dir.
//
// configDirEnv is the only thing that differs between the CLIs
// (CLAUDE_CONFIG_DIR vs CODEX_HOME), so it's a parameter rather than a
// branch: a single YAML "ConfigDir" field serves both without renaming.
func accountEnv(opts agent.RunRequest, configDirEnv string) []string {
	env := os.Environ()
	for k, v := range opts.AccountEnv {
		env = SetEnv(env, k, v)
	}
	if opts.ConfigDir != "" {
		env = SetEnv(env, configDirEnv, opts.ConfigDir)
	}
	return env
}

// RunEnv builds the env for a headless turn (`claude -p` / `codex exec`):
// the shared credential layer, then any sandbox-supplied extras, then —
// PTY mode only — NO_COLOR=1 and TERM=dumb. Without those two the child
// detects a real terminal and wraps its stream-json output in ANSI escape
// sequences, which breaks the NDJSON parser on the reading end.
func RunEnv(opts agent.RunRequest, extraEnv []string, configDirEnv string) []string {
	env := append(accountEnv(opts, configDirEnv), extraEnv...)
	if _, isPty := opts.Spawner.(*agent.PtySpawner); isPty {
		env = SetEnv(env, "NO_COLOR", "1")
		env = SetEnv(env, "TERM", "dumb")
	}
	return env
}

// Stderr retention limits. Callers turn this buffer into the user-facing error
// message that decorates a non-zero exit, so what matters is the child's first
// fatal line and whatever it said just before dying — never the megabytes of
// chatter in between. The limits exist because nothing else bounds this
// buffer: the stall watchdog in StreamLines only ticks on *stdout* lines, so a
// child that logs to stderr while keeping stdout silent (or an MCP server /
// bash tool that inherited the stderr fd and does the same) is invisible to it,
// and the only backstop is RunnerMaxSilentTimeout — two hours by default.
// An unbounded bytes.Buffer will happily absorb everything a chatty process
// emits in two hours; 32 KiB of head plus 16-32 KiB of tail is orders of
// magnitude more than any CLI diagnostic needs and makes the worst case a
// fixed ~64 KiB per running child.
const (
	stderrHeadLimit = 32 << 10
	stderrTailChunk = 16 << 10
)

// stderrCapper consumes everything handed to it — draining the pipe is the
// entire reason DrainStderr exists, and an io.LimitReader would instead stop
// reading and park the child on a full kernel pipe buffer — but only retains a
// bounded head and tail.
//
// The head goes straight into the caller's buffer, unbuffered, because both
// runners read the buffer right after proc.Wait() and before the deferred
// wait(); anything held back until the copy finishes could be missing at that
// point. The tail is flushed in behind a truncation marker once the copy ends,
// which is exactly what wait() waits for.
type stderrCapper struct {
	out  *bytes.Buffer
	head int // bytes still admitted into out verbatim

	// prev/cur hold the most recent stderrTailChunk..2*stderrTailChunk bytes.
	// Two rotating chunks rather than a byte ring or an in-place shift: those
	// move the whole retained window on every Write, so a child that flushes
	// stderr one byte at a time would turn a bounded memory cost into an
	// unbounded CPU one. Rotating whole chunks is amortised O(1).
	prev, cur []byte
	dropped   int64
}

func (c *stderrCapper) Write(p []byte) (int, error) {
	n := len(p)
	if c.head > 0 {
		take := min(c.head, len(p))
		c.out.Write(p[:take])
		c.head -= take
		p = p[take:]
	}
	for len(p) > 0 {
		if len(c.cur) == stderrTailChunk {
			c.dropped += int64(len(c.prev))
			// Recycle prev's array as the new cur; its contents are the bytes
			// we just gave up on.
			c.prev, c.cur = c.cur, c.prev[:0]
		}
		take := min(stderrTailChunk-len(c.cur), len(p))
		c.cur = append(c.cur, p[:take]...)
		p = p[take:]
	}
	return n, nil
}

// flush appends the retained tail so the message the caller reads after wait()
// ends with the child's last words instead of stopping mid-sentence at the head
// limit. The marker is only written when bytes were actually discarded — head
// and tail are contiguous otherwise, and claiming a gap that doesn't exist
// would be worse than saying nothing.
func (c *stderrCapper) flush() {
	if len(c.prev) == 0 && len(c.cur) == 0 {
		return
	}
	if c.dropped > 0 {
		if out := c.out.Bytes(); len(out) > 0 && out[len(out)-1] != '\n' {
			c.out.WriteByte('\n')
		}
		fmt.Fprintf(c.out, "[... %d bytes of stderr dropped ...]\n", c.dropped)
	}
	c.out.Write(c.prev)
	c.out.Write(c.cur)
}

// DrainStderr copies r into the returned buffer on a goroutine so a chatty
// error stream can't block the child on the kernel-side pipe buffer. Retention
// is capped (see stderrHeadLimit / stderrTailChunk): everything is read, but a
// runaway stderr is reduced to its head, a truncation marker, and its tail.
// The returned wait func blocks until the copy finishes; callers defer it so
// the buffer is complete by the time they inspect it after proc.Wait(). A nil r
// (PTY mode merges stderr into stdout) yields an empty buffer and a no-op
// wait.
func DrainStderr(r io.Reader) (buf *bytes.Buffer, wait func()) {
	buf = &bytes.Buffer{}
	if r == nil {
		return buf, func() {}
	}
	// Copy into the capper, not into buf: bytes.Buffer implements
	// io.ReaderFrom, and io.Copy would take that fast path straight past the
	// cap.
	capper := &stderrCapper{out: buf, head: stderrHeadLimit}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(capper, r)
		capper.flush()
		close(done)
	}()
	return buf, func() { <-done }
}

// DrainOneshot consumes an in-flight RunWithSession stream (ch) plus its
// completion error (errCh) and returns just the final assistant text. We
// prefer agent.KindResult (which the upstream stream emits when the
// assistant has finished a turn) and fall back to the concatenated
// agent.KindDelta stream when the run is cut short before a result frame
// arrives. Sub-agent frames belong to a nested invocation and don't
// represent the parent turn's user-facing text, so they're skipped — a
// Task-using summarization run mustn't mix the worker's transcript into the
// headline result.
func DrainOneshot(ch <-chan agent.StreamEvent, errCh <-chan error) (string, error) {
	var (
		result strings.Builder
		deltas strings.Builder
	)
	for evt := range ch {
		if evt.Subagent {
			continue
		}
		switch evt.Kind {
		case agent.KindResult:
			result.WriteString(evt.Content)
		case agent.KindDelta:
			deltas.WriteString(evt.Content)
		}
	}
	if err := <-errCh; err != nil {
		return "", err
	}
	if s := strings.TrimSpace(result.String()); s != "" {
		return s, nil
	}
	return strings.TrimSpace(deltas.String()), nil
}
