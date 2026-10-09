// Package logfile gives the server a durable copy of its own log.
//
// The process already writes to stderr, but whether anything keeps that is the
// service manager's business — and on the reference deployment journald quietly
// stopped persisting, so a fortnight of IM upload failures left no trace at all.
// Everything DayMug logs about a failure it chose not to escalate (see
// imbridge.respondBestEffort) is only useful if it is still readable tomorrow.
//
// Deliberately a leaf package with no dependencies beyond the standard library:
// rotation here is size-based and dumb, which is all a single-binary install
// needs and one less thing to keep working.
package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	// DefaultMaxSize caps one log file before it is rotated.
	DefaultMaxSize int64 = 32 << 20
	// DefaultKeep is how many rotated generations survive alongside the
	// current file, so the whole footprint stays bounded.
	DefaultKeep = 5
)

// Writer appends to a log file, rotating it once it outgrows maxSize. Safe for
// concurrent use: the standard logger serialises its own writes, but gin and
// any goroutine holding the same Writer do not.
type Writer struct {
	path    string
	maxSize int64
	keep    int

	mu   sync.Mutex
	file *os.File
	size int64
}

// Open prepares path for appending, creating its directory if needed. maxSize
// and keep fall back to the defaults when non-positive.
func Open(path string, maxSize int64, keep int) (*Writer, error) {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if keep <= 0 {
		keep = DefaultKeep
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	w := &Writer{path: path, maxSize: maxSize, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	// The log carries workdirs, account paths and agent errors quoting user
	// content, so it is owner-only. Chmod as well because O_CREATE's mode is
	// ignored for a file that already exists — installs predating this kept
	// a world-readable 0644 log.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	w.file, w.size = f, info.Size()
	return nil
}

// Write appends p, rotating first when it would push the file past maxSize. A
// single record is never split across two files.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate shifts daymug.log -> .1 -> .2 ... and drops the oldest generation.
func (w *Writer) rotate() error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close log file: %w", err)
	}
	w.file = nil
	for i := w.keep; i >= 1; i-- {
		older := fmt.Sprintf("%s.%d", w.path, i)
		if i == w.keep {
			_ = os.Remove(older)
			continue
		}
		// Rename is a no-op error when generation i does not exist yet, which
		// is the normal case on the first few rotations.
		_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		// The current file is gone but unrenameable; reopening keeps logging
		// alive rather than failing every subsequent write.
		if openErr := w.open(); openErr != nil {
			return openErr
		}
		return fmt.Errorf("rotate log file: %w", err)
	}
	return w.open()
}

// Close releases the underlying file. Further writes report os.ErrClosed.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
