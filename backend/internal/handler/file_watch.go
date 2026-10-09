package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service/filewatch"
)

// watchPingInterval keeps the socket alive through NAT and reverse proxies
// that reap idle connections. Matches the chat socket's cadence.
const watchPingInterval = 30 * time.Second

// watchRequest is the only message a client sends. The watch set is replaced
// wholesale on every message rather than merged, because only the client knows
// which files it still has open — a server-side union would accumulate watches
// for files closed long ago.
type watchRequest struct {
	Action string   `json:"action"`
	Paths  []string `json:"paths"`
}

type watchReadyFrame struct {
	Type     string   `json:"type"`
	Watching []string `json:"watching"`
}

type watchChangeFrame struct {
	Type  string `json:"type"`
	Path  string `json:"path"`
	Mtime string `json:"mtime,omitempty"`
	Size  int64  `json:"size,omitempty"`
}

type watchErrorFrame struct {
	Type string `json:"type"`
	Code string `json:"code"`
}

// WatchFiles turns one WebSocket into a change subscription over a handful of
// files inside the user's work dir.
//
// Access is decided before the upgrade by the same getUserFileAccess the REST
// file routes use, and every requested path goes through the same safePath
// sandbox — this endpoint adds no new authorization surface.
func (h *FileHandler) WatchFiles(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	conn, ctx, cancel, ok := acceptWS(c.Writer, c.Request, "file watch ws accept")
	if !ok {
		return
	}
	defer func() { _ = conn.CloseNow() }()
	defer cancel()

	sendCh := make(chan []byte, 64)
	writerDone := startWSWriter(ctx, conn, sendCh, func(data []byte) (websocket.MessageType, []byte) {
		return websocket.MessageText, data
	})
	defer writerDone.Wait()
	defer close(sendCh)

	send := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			return
		}
		select {
		case sendCh <- data:
		case <-ctx.Done():
		default:
		}
	}

	// No watcher means fsnotify was unavailable at boot. Say so rather than
	// going silent: the client's fallback is polling, and it can only pick it
	// if it hears something back.
	if h.Watcher == nil {
		send(watchErrorFrame{Type: "error", Code: "unavailable"})
		<-ctx.Done()
		return
	}

	sub, err := h.Watcher.Subscribe()
	if err != nil {
		send(watchErrorFrame{Type: "error", Code: "unavailable"})
		<-ctx.Done()
		return
	}
	defer sub.Close()

	startWSPing(ctx, cancel, conn, watchPingInterval)
	go forwardWatchEvents(ctx, sub, access, send)

	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg watchRequest
		if err := json.Unmarshal(raw, &msg); err != nil {
			send(watchErrorFrame{Type: "error", Code: "bad_request"})
			continue
		}
		if msg.Action != "watch" {
			send(watchErrorFrame{Type: "error", Code: "bad_request"})
			continue
		}

		abs := make([]string, 0, len(msg.Paths))
		rel := make([]string, 0, len(msg.Paths))
		rejected := false
		for _, p := range msg.Paths {
			full, err := access.safePath(p)
			if err != nil {
				// Reject the whole batch. Partially honouring it would leave
				// the client unable to tell which of its files are live, and
				// its degrade-to-polling decision depends on that being clear.
				send(watchErrorFrame{Type: "error", Code: "forbidden"})
				rejected = true
				break
			}
			abs = append(abs, full)
			rel = append(rel, p)
		}
		if rejected {
			continue
		}

		accepted, err := sub.Watch(abs, rel)
		switch {
		case errors.Is(err, filewatch.ErrIgnored):
			send(watchErrorFrame{Type: "error", Code: "ignored"})
		case errors.Is(err, filewatch.ErrCapacity):
			send(watchErrorFrame{Type: "error", Code: "capacity"})
		case err != nil:
			log.Printf("file watch: %v", err)
			send(watchErrorFrame{Type: "error", Code: "unavailable"})
		default:
			if accepted == nil {
				accepted = []string{}
			}
			send(watchReadyFrame{Type: "ready", Watching: accepted})
		}
	}
}

// forwardWatchEvents turns coalesced watcher events into wire frames, stamping
// each change with the validators the client compares against what it holds.
func forwardWatchEvents(ctx context.Context, sub *filewatch.Subscription, access userFileAccess, send func(any)) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sub.Events():
			if ev.Op == filewatch.OpRemoved {
				send(watchChangeFrame{Type: "removed", Path: ev.Path})
				continue
			}
			full, err := access.safePath(ev.Path)
			if err != nil {
				continue
			}
			info, err := os.Stat(full)
			if err != nil {
				// The file vanished between the event and the stat — from the
				// reader's point of view that is a removal, not a change.
				send(watchChangeFrame{Type: "removed", Path: ev.Path})
				continue
			}
			send(watchChangeFrame{
				Type:  "changed",
				Path:  ev.Path,
				Mtime: info.ModTime().UTC().Format(time.RFC3339Nano),
				Size:  info.Size(),
			})
		}
	}
}
