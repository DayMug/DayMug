package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type wireMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("CAS error %d: %s", e.Code, e.Message) }

type rpcClient struct {
	stdin     io.Writer
	decoder   *json.Decoder
	writeMu   sync.Mutex
	nextID    atomic.Uint64
	pendingMu sync.Mutex
	pending   map[string]chan wireMessage
	incoming  chan wireMessage
	done      chan struct{}
	errMu     sync.Mutex
	err       error
}

func newClient(stdin io.Writer, stdout io.Reader) *rpcClient {
	return &rpcClient{stdin: stdin, decoder: json.NewDecoder(stdout), pending: make(map[string]chan wireMessage), incoming: make(chan wireMessage, 256), done: make(chan struct{})}
}

func (c *rpcClient) start() {
	go func() {
		defer close(c.incoming)
		defer close(c.done)
		for {
			var msg wireMessage
			if err := c.decoder.Decode(&msg); err != nil {
				if !errors.Is(err, io.EOF) {
					c.setReadError(fmt.Errorf("decode CAS message: %w", err))
				}
				return
			}
			if len(msg.ID) > 0 && msg.Method == "" {
				c.pendingMu.Lock()
				ch := c.pending[string(msg.ID)]
				c.pendingMu.Unlock()
				if ch != nil {
					ch <- msg
					continue
				}
			}
			c.incoming <- msg
		}
	}()
}

func (c *rpcClient) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	key := fmt.Sprintf("%d", id)
	ch := make(chan wireMessage, 1)
	c.pendingMu.Lock()
	c.pending[key] = ch
	c.pendingMu.Unlock()
	defer func() { c.pendingMu.Lock(); delete(c.pending, key); c.pendingMu.Unlock() }()
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		if err := c.readError(); err != nil {
			return err
		}
		return io.EOF
	case response := <-ch:
		if response.Error != nil {
			return response.Error
		}
		if out != nil && len(response.Result) > 0 {
			return json.Unmarshal(response.Result, out)
		}
		return nil
	}
}

func (c *rpcClient) notify(method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *rpcClient) respondError(id json.RawMessage, code int, message string) error {
	var decoded any
	if err := json.Unmarshal(id, &decoded); err != nil {
		return err
	}
	return c.write(map[string]any{"id": decoded, "error": map[string]any{"code": code, "message": message}})
}

func (c *rpcClient) respondResult(id json.RawMessage, result any) error {
	var decoded any
	if err := json.Unmarshal(id, &decoded); err != nil {
		return err
	}
	return c.write(map[string]any{"id": decoded, "result": result})
}

func (c *rpcClient) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = fmt.Fprintf(c.stdin, "%s\n", b)
	return err
}

func (c *rpcClient) setReadError(err error) { c.errMu.Lock(); c.err = err; c.errMu.Unlock() }
func (c *rpcClient) readError() error       { c.errMu.Lock(); defer c.errMu.Unlock(); return c.err }
func (c *rpcClient) stopped() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
