// Package filewatch turns filesystem changes into per-subscriber event
// streams for the files a browser currently has open.
//
// The design constraint that shapes everything here: only the files a client
// explicitly subscribes to are watched, never a directory tree. A workspace
// routinely contains node_modules, .git and build output; recursing into it
// would burn thousands of inotify watches to deliver events nobody asked for.
package filewatch

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Event operations. Kept as strings because they cross the wire verbatim as
// the WebSocket frame's "type".
const (
	OpChanged = "changed"
	OpRemoved = "removed"
)

// Limits. Both are deliberately small: a browser tab has a handful of files
// open, and a server that has drifted past these numbers is leaking
// subscriptions rather than serving unusually busy users.
const (
	MaxPathsPerSubscription = 16
	MaxDirsPerProcess       = 512
)

// defaultCoalesceWindow collapses the burst of events a single logical save
// produces. Long enough to absorb a write-then-chmod sequence, short enough
// that the browser still feels immediate.
const defaultCoalesceWindow = 150 * time.Millisecond

// ignoredDirs are path segments a subscription may not reach through. Files
// the user has open in a preview pane never live here, so a request naming
// one is either a bug or an attempt to turn this into a tree watch.
var ignoredDirs = map[string]struct{}{
	"node_modules": {},
	".git":         {},
	"dist":         {},
	".next":        {},
	"vendor":       {},
	"__pycache__":  {},
	".venv":        {},
	"target":       {},
}

var (
	// ErrCapacity means the subscription or the process is already watching
	// as much as it is allowed to.
	ErrCapacity = errors.New("filewatch: capacity exceeded")
	// ErrIgnored means a requested path passes through a directory this
	// package refuses to watch.
	ErrIgnored = errors.New("filewatch: path in ignored directory")
	// ErrClosed means the watcher has been shut down.
	ErrClosed = errors.New("filewatch: watcher closed")
)

// Event is one coalesced change notification. Path is the relative path the
// subscriber supplied, echoed back untouched so the client never has to map
// absolute paths it never sent.
type Event struct {
	Path string
	Op   string
}

// Watcher owns the process's single fsnotify handle and the refcounted set of
// directories behind it.
type Watcher struct {
	fsw *fsnotify.Watcher

	mu     sync.Mutex
	dirs   map[string]map[*Subscription]struct{} // absolute dir → subscribers
	closed bool
	window time.Duration
}

// Subscription is one client's view: a set of files it cares about and the
// channel their events arrive on.
type Subscription struct {
	w  *Watcher
	ch chan Event

	mu      sync.Mutex
	files   map[string]string // absolute file path → relative path
	pending map[string]Event  // relative path → coalesced event awaiting flush
	timer   *time.Timer
	closed  bool
}

// New starts the shared watcher and its fan-out goroutine.
func New() (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fsw:    fsw,
		dirs:   make(map[string]map[*Subscription]struct{}),
		window: defaultCoalesceWindow,
	}
	go w.run()
	return w, nil
}

// SetCoalesceWindow overrides the flush delay. Only useful in tests, which
// would otherwise spend the production window waiting on every assertion.
func (w *Watcher) SetCoalesceWindow(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.window = d
}

// Close stops the watcher and releases every directory it holds.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.dirs = make(map[string]map[*Subscription]struct{})
	w.mu.Unlock()
	return w.fsw.Close()
}

// Subscribe registers a new client. The returned Subscription watches nothing
// until Watch is called.
func (w *Watcher) Subscribe() (*Subscription, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, ErrClosed
	}
	return &Subscription{
		w: w,
		// Buffered so a browser that stops reading for a moment doesn't stall
		// the shared fan-out goroutine; overflow is dropped, and the client
		// reconciles on its next open anyway.
		ch:      make(chan Event, 64),
		files:   make(map[string]string),
		pending: make(map[string]Event),
	}, nil
}

// Events is the subscriber's read side. It is never closed — Close simply
// stops producing, so a reader blocked here just stops receiving.
func (s *Subscription) Events() <-chan Event { return s.ch }

// Watch replaces this subscription's file set wholesale. abs and rel must be
// the same length: abs[i] is the resolved path on disk, rel[i] the label to
// echo back in events.
//
// Replacement rather than incremental add/remove because only the client knows
// which files it still has open; a server-side union would accumulate watches
// for files closed long ago.
func (s *Subscription) Watch(abs, rel []string) ([]string, error) {
	if len(abs) != len(rel) {
		return nil, errors.New("filewatch: abs and rel length mismatch")
	}
	if len(abs) > MaxPathsPerSubscription {
		return nil, ErrCapacity
	}
	for _, r := range rel {
		if hasIgnoredSegment(r) {
			return nil, ErrIgnored
		}
	}

	next := make(map[string]string, len(abs))
	nextDirs := make(map[string]struct{}, len(abs))
	accepted := make([]string, 0, len(abs))
	for i, a := range abs {
		clean := filepath.Clean(a)
		next[clean] = rel[i]
		nextDirs[filepath.Dir(clean)] = struct{}{}
		accepted = append(accepted, rel[i])
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	prevDirs := s.dirSet()
	s.files = next
	s.mu.Unlock()

	// Apply the diff rather than dropping everything and re-adding: a
	// directory that survives the replacement must never lose its watch, not
	// even for the instant it would take to re-register it.
	if err := s.w.retain(s, nextDirs, prevDirs); err != nil {
		return nil, err
	}
	return accepted, nil
}

// Close releases this subscription's directory references and stops delivery.
func (s *Subscription) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	prev := s.dirSet()
	s.files = make(map[string]string)
	s.pending = make(map[string]Event)
	s.mu.Unlock()

	s.w.release(s, prev)
}

// dirSet returns the directories the current file set spans. Caller holds s.mu.
func (s *Subscription) dirSet() map[string]struct{} {
	dirs := make(map[string]struct{}, len(s.files))
	for abs := range s.files {
		dirs[filepath.Dir(abs)] = struct{}{}
	}
	return dirs
}

// retain moves this subscription's directory references from prev to next,
// adding and removing fsnotify watches as refcounts cross zero.
func (w *Watcher) retain(s *Subscription, next, prev map[string]struct{}) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrClosed
	}

	toAdd := make([]string, 0, len(next))
	for dir := range next {
		if _, held := prev[dir]; held {
			continue
		}
		if _, known := w.dirs[dir]; !known {
			if len(w.dirs) >= MaxDirsPerProcess {
				w.mu.Unlock()
				return ErrCapacity
			}
			w.dirs[dir] = make(map[*Subscription]struct{})
			toAdd = append(toAdd, dir)
		}
		w.dirs[dir][s] = struct{}{}
	}

	toRemove := make([]string, 0, len(prev))
	for dir := range prev {
		if _, keep := next[dir]; keep {
			continue
		}
		if subs, known := w.dirs[dir]; known {
			delete(subs, s)
			if len(subs) == 0 {
				delete(w.dirs, dir)
				toRemove = append(toRemove, dir)
			}
		}
	}
	w.mu.Unlock()

	for _, dir := range toAdd {
		if err := w.fsw.Add(dir); err != nil {
			// Roll the bookkeeping back so a failed Add doesn't leave a
			// directory that looks watched but never reports anything.
			w.mu.Lock()
			if subs, known := w.dirs[dir]; known {
				delete(subs, s)
				if len(subs) == 0 {
					delete(w.dirs, dir)
				}
			}
			w.mu.Unlock()
			return err
		}
	}
	for _, dir := range toRemove {
		_ = w.fsw.Remove(dir)
	}
	return nil
}

// release drops every reference this subscription holds.
func (w *Watcher) release(s *Subscription, dirs map[string]struct{}) {
	w.mu.Lock()
	toRemove := make([]string, 0, len(dirs))
	for dir := range dirs {
		subs, known := w.dirs[dir]
		if !known {
			continue
		}
		delete(subs, s)
		if len(subs) == 0 {
			delete(w.dirs, dir)
			toRemove = append(toRemove, dir)
		}
	}
	w.mu.Unlock()

	for _, dir := range toRemove {
		_ = w.fsw.Remove(dir)
	}
}

// WatchedDirCount reports how many directories currently hold a watch. Exported
// as the regression hook for the refcount and teardown assertions — a leak here
// is invisible until the process runs out of inotify budget.
func (w *Watcher) WatchedDirCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.dirs)
}

// run fans directory events out to the subscriptions that asked for the
// specific file each event names.
func (w *Watcher) run() {
	for {
		select {
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.dispatch(ev)
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// A watch-level error (the directory went away, the backend hit a
			// limit) is not worth tearing the process down for: the affected
			// client's next open re-subscribes, and its ready-frame timeout
			// degrades it to polling in the meantime.
		}
	}
}

func (w *Watcher) dispatch(ev fsnotify.Event) {
	path := filepath.Clean(ev.Name)
	dir := filepath.Dir(path)

	w.mu.Lock()
	subs := make([]*Subscription, 0, len(w.dirs[dir]))
	for s := range w.dirs[dir] {
		subs = append(subs, s)
	}
	window := w.window
	w.mu.Unlock()

	op := OpChanged
	// A rename moves the inode away from this name; from the reader's point of
	// view the file at that path is gone. The destination side of an atomic
	// save arrives separately as Create, which is already OpChanged.
	if ev.Has(fsnotify.Remove) || ev.Has(fsnotify.Rename) {
		op = OpRemoved
	}

	for _, s := range subs {
		s.enqueue(path, op, window)
	}
}

// enqueue coalesces an event for one subscription, flushing after window.
func (s *Subscription) enqueue(abs, op string, window time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	rel, watched := s.files[abs]
	if !watched {
		// The directory is watched for atomic-save reasons; the subscription
		// is still per-file, so siblings must not leak.
		return
	}

	s.pending[rel] = Event{Path: rel, Op: op}
	if s.timer == nil {
		s.timer = time.AfterFunc(window, s.flush)
	}
}

func (s *Subscription) flush() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	events := make([]Event, 0, len(s.pending))
	for _, ev := range s.pending {
		events = append(events, ev)
	}
	s.pending = make(map[string]Event)
	s.timer = nil
	s.mu.Unlock()

	for _, ev := range events {
		select {
		case s.ch <- ev:
		default:
		}
	}
}

func hasIgnoredSegment(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if _, bad := ignoredDirs[part]; bad {
			return true
		}
	}
	return false
}
