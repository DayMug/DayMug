package wechat

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantOK   bool
		wantName string
		wantRest string
	}{
		{"bare command", "/new", true, "new", ""},
		{"command with payload", "/new 帮我看看这个 bug", true, "new", "帮我看看这个 bug"},
		{"payload on the next line", "/new\n第一行\n第二行", true, "new", "第一行\n第二行"},
		{"leading whitespace tolerated", "  \n /new ", true, "new", ""},
		{"case folded", "/NEW", true, "new", ""},
		{"plain text is not a command", "hello", false, "", ""},
		{"slash mid-sentence is not a command", "run /new please", false, "", ""},
		{"a path is not a command", "/home/ada/x.go 看一下", false, "", ""},
		{"a bare slash is not a command", "/", false, "", ""},
		{"digits cannot start a name", "/2fa", false, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, ok := ParseCommand(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ParseCommand(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if cmd.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", cmd.Name, tc.wantName)
			}
			if cmd.Rest != tc.wantRest {
				t.Errorf("Rest = %q, want %q", cmd.Rest, tc.wantRest)
			}
		})
	}
}

// newSeqIDs hands out predictable ids so tests can assert on rotation.
func newSeqIDs() func() string {
	n := 0
	return func() string {
		n++
		return "t" + strconv.Itoa(n)
	}
}

func TestThreadsAssignsStableIDPerPeer(t *testing.T) {
	tracker := NewThreads(ThreadsOptions{NewID: newSeqIDs()})

	first := tracker.Current("u1@im.wechat")
	if first != "t1" {
		t.Fatalf("first thread = %q, want t1", first)
	}
	if again := tracker.Current("u1@im.wechat"); again != first {
		t.Errorf("second call = %q, want the same thread %q", again, first)
	}
	if other := tracker.Current("u2@im.wechat"); other == first {
		t.Errorf("a different peer got the same thread %q", other)
	}
}

func TestRouteNewRotatesThread(t *testing.T) {
	tracker := NewThreads(ThreadsOptions{NewID: newSeqIDs()})
	peer := "u1@im.wechat"

	before := tracker.Route(peer, "先聊点别的")
	if before.Reset {
		t.Error("an ordinary message must not reset the thread")
	}
	if before.Prompt != "先聊点别的" {
		t.Errorf("Prompt = %q, want the message verbatim", before.Prompt)
	}

	reset := tracker.Route(peer, "/new")
	if !reset.Reset {
		t.Error("/new did not report a reset")
	}
	if reset.ThreadID == before.ThreadID {
		t.Errorf("thread id %q unchanged after /new", reset.ThreadID)
	}
	// A bare /new has nothing to answer yet.
	if reset.Prompt != "" {
		t.Errorf("Prompt = %q, want empty for a bare /new", reset.Prompt)
	}

	after := tracker.Route(peer, "现在说这个")
	if after.ThreadID != reset.ThreadID {
		t.Errorf("follow-up landed in %q, want the new thread %q", after.ThreadID, reset.ThreadID)
	}
}

func TestRouteNewCarriesPayloadIntoTheNewThread(t *testing.T) {
	tracker := NewThreads(ThreadsOptions{NewID: newSeqIDs()})
	peer := "u1@im.wechat"
	old := tracker.Current(peer)

	got := tracker.Route(peer, "/new 帮我重构这个函数")
	if !got.Reset {
		t.Error("/new with a payload did not reset the thread")
	}
	if got.ThreadID == old {
		t.Error("thread id was not rotated")
	}
	if got.Prompt != "帮我重构这个函数" {
		t.Errorf("Prompt = %q, want the payload without the command", got.Prompt)
	}
}

// The agent CLIs have their own slash commands; anything we do not recognise
// has to reach them untouched.
func TestRouteLeavesUnknownCommandsAlone(t *testing.T) {
	tracker := NewThreads(ThreadsOptions{NewID: newSeqIDs()})
	peer := "u1@im.wechat"
	before := tracker.Current(peer)

	got := tracker.Route(peer, "/compact")
	if got.Reset {
		t.Error("/compact must not reset the thread")
	}
	if got.ThreadID != before {
		t.Errorf("thread id = %q, want the existing %q", got.ThreadID, before)
	}
	if got.Prompt != "/compact" {
		t.Errorf("Prompt = %q, want the command passed through verbatim", got.Prompt)
	}
}

func TestThreadsNeverReusesAnIDAfterReset(t *testing.T) {
	tracker := NewThreads(ThreadsOptions{})
	peer := "u1@im.wechat"

	seen := map[string]bool{tracker.Current(peer): true}
	for range 20 {
		id := tracker.Reset(peer)
		if seen[id] {
			t.Fatalf("thread id %q was reused — a new topic would resume the old session", id)
		}
		seen[id] = true
	}
}

func TestThreadsPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "threads.json")
	peer := "u1@im.wechat"

	first := NewThreads(ThreadsOptions{Store: &FileThreadStore{Path: path}})
	want := first.Current(peer)

	restarted := NewThreads(ThreadsOptions{Store: &FileThreadStore{Path: path}})
	if got := restarted.Current(peer); got != want {
		t.Errorf("thread after restart = %q, want the persisted %q", got, want)
	}

	rotated := restarted.Reset(peer)
	again := NewThreads(ThreadsOptions{Store: &FileThreadStore{Path: path}})
	if got := again.Current(peer); got != rotated {
		t.Errorf("thread after restart = %q, want the rotated %q", got, rotated)
	}
}

func TestFileThreadStoreLoadsEmptyWhenMissing(t *testing.T) {
	store := &FileThreadStore{Path: filepath.Join(t.TempDir(), "absent.json")}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Load = %v, want empty", got)
	}
}

// Continuity is a convenience; a store that cannot be read must not stop the
// bot from answering.
func TestThreadsToleratesUnreadableStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "threads.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	logged := 0
	tracker := NewThreads(ThreadsOptions{
		Store: &FileThreadStore{Path: path},
		Logf:  func(string, ...any) { logged++ },
	})
	if got := tracker.Current("u1@im.wechat"); got == "" {
		t.Error("Current returned no thread id")
	}
	if logged == 0 {
		t.Error("an unreadable store should be logged")
	}
}
