package imbot

import (
	"sync"
	"testing"
)

// A contact lookup that fails transiently (429, timeout) used to be memoized as
// the empty string, pinning that sender to their raw platform id for the whole
// connector lifetime. The next message must retry instead.
func TestCachedNameRetriesAfterFailedLookup(t *testing.T) {
	var b baseConnector
	calls := 0
	fetch := func() string {
		calls++
		if calls == 1 {
			return "" // simulated contact-API failure
		}
		return "Alice"
	}

	if got := b.cachedName("ou_alice", fetch); got != "" {
		t.Fatalf("first lookup = %q, want empty", got)
	}
	if got := b.cachedName("ou_alice", fetch); got != "Alice" {
		t.Fatalf("second lookup = %q, want %q (failure must not be cached)", got, "Alice")
	}
	if calls != 2 {
		t.Fatalf("fetch called %d times, want 2", calls)
	}
}

func TestCachedNameMemoizesSuccessfulLookup(t *testing.T) {
	var b baseConnector
	calls := 0
	fetch := func() string {
		calls++
		return "Alice"
	}

	for range 3 {
		if got := b.cachedName("ou_alice", fetch); got != "Alice" {
			t.Fatalf("cachedName = %q, want %q", got, "Alice")
		}
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times, want 1", calls)
	}
}

// The cache is read and written from platform event goroutines; -race would
// flag a regression that drops the mutex.
func TestCachedNameConcurrentAccess(t *testing.T) {
	var b baseConnector
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i%3))
			_ = b.cachedName(id, func() string { return "name-" + id })
		}(i)
	}
	wg.Wait()
}
