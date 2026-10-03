package agent

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// spawnRetryAttempts is the number of extra Spawn attempts made after a
// transient ENOENT failure (i.e. up to spawnRetryAttempts+1 total tries).
//
// The claude CLI installs as a symlink (e.g. ~/.local/bin/claude) pointing at
// a versioned binary under ~/.local/share/claude/versions/<v>. Its background
// self-upgrade repoints that symlink at a freshly-downloaded version and then
// deletes the old version directory. There is a sub-second window during that
// swap where fork/exec of the symlink resolves to a path that no longer exists
// and fails with ENOENT — surfacing to the user as a spurious
// "start claude: ... no such file or directory" on a later conversation turn.
// A short bounded retry rides out that window.
var (
	spawnRetryAttempts = 3
	spawnRetryBackoff  = 200 * time.Millisecond
)

// SpawnWithRetry calls spawner.Spawn, retrying only on transient ENOENT
// failures with a short backoff. Any other error (or success) returns
// immediately. The retry is abandoned early if ctx is cancelled.
func SpawnWithRetry(ctx context.Context, spawner ProcessSpawner, req SpawnRequest) (RunningProcess, error) {
	proc, err := spawner.Spawn(ctx, req)
	for attempt := 0; err != nil && errors.Is(err, syscall.ENOENT) && attempt < spawnRetryAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(spawnRetryBackoff):
		}
		proc, err = spawner.Spawn(ctx, req)
	}
	return proc, err
}
