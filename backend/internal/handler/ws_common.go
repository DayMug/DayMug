package handler

import (
	"context"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// acceptWS upgrades an HTTP request to a WebSocket and derives the
// per-connection context. On failure the error is logged under logLabel and
// ok is false (websocket.Accept has already written the HTTP error to the
// client). Callers own the teardown order and must defer conn.CloseNow()
// and cancel() themselves.
func acceptWS(w http.ResponseWriter, r *http.Request, logLabel string) (conn *websocket.Conn, ctx context.Context, cancel context.CancelFunc, ok bool) {
	conn, err := websocket.Accept(w, r, wsAcceptOptions(r))
	if err != nil {
		log.Printf("%s: %v", logLabel, err)
		return nil, nil, nil, false
	}
	ctx, cancel = context.WithCancel(r.Context())
	return conn, ctx, cancel, true
}

// wsAcceptOptions enforces a same-origin handshake. Auth rides on the session
// cookie, and SameSite=Lax still lets a page on a sibling subdomain open a
// socket with it, so without an Origin check any such page could drive a
// user's agent — or an admin's shell.
//
// websocket.Accept already admits Origin == Host and requests without an
// Origin (non-browser clients). On top of that we admit:
//   - X-Forwarded-Host, for reverse proxies that rewrite Host to the upstream
//     address; a browser page cannot set this header on a handshake, so it
//     gives a cross-site attacker nothing.
//   - any loopback origin when the request itself arrived on a loopback Host:
//     the Vite dev proxy runs with changeOrigin, so Host is the backend port
//     while Origin is the dev server's.
func wsAcceptOptions(r *http.Request) *websocket.AcceptOptions {
	var patterns []string
	for _, h := range strings.Split(r.Header.Get("X-Forwarded-Host"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			patterns = append(patterns, h)
		}
	}
	if isLoopbackHost(r.Host) {
		patterns = append(patterns, "localhost", "localhost:*", "127.0.0.1", "127.0.0.1:*", "[::1]", "[::1]:*")
	}
	return &websocket.AcceptOptions{OriginPatterns: patterns}
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// startWSWriter starts the single goroutine that owns conn.Write: it drains
// ch and writes each frame produced by frame(), exiting when ch is closed or
// a write fails. A single writer guarantees frames never interleave on the
// wire. The returned WaitGroup completes once the writer has exited; callers
// that close ch may Wait on it to drain in-flight frames, callers that never
// close ch simply ignore it (their writer exits via write failure after
// CloseNow).
func startWSWriter[T any](ctx context.Context, conn *websocket.Conn, ch <-chan T, frame func(T) (websocket.MessageType, []byte)) *sync.WaitGroup {
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		for v := range ch {
			mt, data := frame(v)
			if err := conn.Write(ctx, mt, data); err != nil {
				return
			}
		}
	}()
	return &done
}

// startWSPing runs the keep-alive ping loop for a WebSocket. Without it,
// idle TCP connections get reaped silently by NAT / reverse proxies after
// ~60-300s of no traffic. A failed ping (peer gone, network broken) cancels
// the connection's context so the rest of the handler tears down promptly.
// coder/websocket's Ping requires a concurrent Reader to receive the pong;
// each caller's read loop provides that.
func startWSPing(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
}
