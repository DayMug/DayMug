package service

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// uploadsPruneInterval rate-limits the opportunistic sweep so a message
	// carrying ten attachments does not re-scan the directory ten times.
	uploadsPruneInterval = time.Hour

	// uploadsPruneTrackedDirs bounds the rate-limiter's memory. Each agent
	// work_dir gets one entry; past the bound the whole table is dropped, which
	// costs one extra sweep per directory and never leaks.
	uploadsPruneTrackedDirs = 1024
)

var (
	// uploadsRetention is how long a file under UploadsDir is kept before the
	// opportunistic sweep may delete it. Zero (the default) keeps uploads
	// forever.
	//
	// These files are not scratch data: their relative path is baked into the
	// agent prompt, into the persisted store.Message metadata of every IM turn,
	// and into the /api/users/:id/files/read URL the web transcript renders. A
	// conversation never expires, so any TTL can break an old transcript —
	// which is why deleting them is an operator opt-in (retention.uploads).
	uploadsRetention atomic.Int64

	uploadsPruneMu   sync.Mutex
	uploadsPruneSeen = map[string]time.Time{}
)

// SetUploadsRetention sets the age past which MaybePruneUploads deletes an
// upload. Zero or negative disables the sweep.
func SetUploadsRetention(d time.Duration) {
	uploadsRetention.Store(int64(d))
}

// MaybePruneUploads sweeps expired files out of uploadsDir, at most once per
// uploadsPruneInterval per directory, when an uploads retention is configured.
//
// The sweep is triggered on write rather than on a timer or at startup on
// purpose: the writer already knows the directory (work_dirs are per
// conversation and are not enumerable without walking the store), the cost is
// one ReadDir amortised over an hour, and it needs no background goroutine or
// process-lifecycle wiring. The trade-off is that a directory nobody writes to
// again is never swept — which is exactly the directory that has stopped
// growing, so it cannot be the source of unbounded accumulation.
//
// Errors are deliberately swallowed: failing to delete an expired attachment
// must never fail the upload that triggered the sweep.
func MaybePruneUploads(uploadsDir string) {
	ttl := time.Duration(uploadsRetention.Load())
	if ttl <= 0 {
		return
	}
	now := time.Now()
	if !claimUploadsPrune(uploadsDir, now) {
		return
	}
	_, _ = PruneUploads(uploadsDir, ttl, now)
}

func claimUploadsPrune(uploadsDir string, now time.Time) bool {
	uploadsPruneMu.Lock()
	defer uploadsPruneMu.Unlock()
	if last, ok := uploadsPruneSeen[uploadsDir]; ok && now.Sub(last) < uploadsPruneInterval {
		return false
	}
	if len(uploadsPruneSeen) >= uploadsPruneTrackedDirs {
		uploadsPruneSeen = make(map[string]time.Time, uploadsPruneTrackedDirs)
	}
	uploadsPruneSeen[uploadsDir] = now
	return true
}

// PruneUploads removes regular files directly under uploadsDir whose mtime is
// older than ttl, and reports how many it removed. Uploads are written once and
// never rewritten, so mtime is the arrival time.
//
// Only regular files at the top level are considered. Sub-directories are left
// alone (the uploads dir is flat; anything nested was put there by the user or
// the agent), and symlinks are skipped rather than unlinked so a stale link
// never decides the fate of whatever it points at.
func PruneUploads(uploadsDir string, ttl time.Duration, now time.Time) (int, error) {
	if ttl <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		return 0, err
	}
	cutoff := now.Add(-ttl)
	removed := 0
	var firstErr error
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(uploadsDir, entry.Name())); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}
