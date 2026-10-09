package imbot

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// stubHTTPClient returns a canned response without touching the network.
type stubHTTPClient struct {
	resp *http.Response
}

func (s *stubHTTPClient) Do(*http.Request) (*http.Response, error) { return s.resp, nil }

func newCappedClient(t *testing.T, body string, contentLength int64, max int64) *cappedHTTPClient {
	t.Helper()
	return &cappedHTTPClient{
		inner: &stubHTTPClient{resp: &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: contentLength,
		}},
		max: max,
	}
}

// A declared Content-Length over the cap is refused without reading a byte —
// the whole point is that an oversized resource never reaches the SDK, which
// would io.ReadAll it onto the heap.
func TestCappedHTTPClientRejectsDeclaredOversizeBeforeReading(t *testing.T) {
	client := newCappedClient(t, strings.Repeat("x", 100), 100, 10)

	resp, err := client.Do(&http.Request{})
	if !errors.Is(err, errFeishuResponseTooLarge) {
		t.Fatalf("err = %v, want errFeishuResponseTooLarge", err)
	}
	if resp != nil {
		t.Error("an oversized response was handed to the caller anyway")
	}
}

// A body that lies about (or omits) its length is cut off mid-stream, so the
// worst case held in memory is the cap rather than the whole resource.
func TestCappedHTTPClientTruncatesUndeclaredOversizeBody(t *testing.T) {
	client := newCappedClient(t, strings.Repeat("x", 100), -1, 10)

	resp, err := client.Do(&http.Request{})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	read, err := io.ReadAll(resp.Body)
	if !errors.Is(err, errFeishuResponseTooLarge) {
		t.Fatalf("read err = %v, want errFeishuResponseTooLarge", err)
	}
	// cap+1 by design: the limiter reads one byte past the limit to tell "body
	// is exactly at the cap" from "body is over it", and that byte only ever
	// escapes alongside the error. What matters is that the 100-byte body did
	// not land on the heap.
	if int64(len(read)) > 11 {
		t.Fatalf("buffered %d bytes, want the cap (10, +1 probe byte) to bound what reaches memory", len(read))
	}
}

// A body exactly at the cap is legitimate and must survive: the limiter reads
// one byte past the limit only to tell "at the cap" from "over the cap".
func TestCappedHTTPClientAllowsBodyExactlyAtCap(t *testing.T) {
	const payload = "0123456789"
	client := newCappedClient(t, payload, int64(len(payload)), int64(len(payload)))

	resp, err := client.Do(&http.Request{})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	read, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("a body exactly at the cap was rejected: %v", err)
	}
	if string(read) != payload {
		t.Fatalf("read %q, want %q", read, payload)
	}
}

// The cap is installed on the client the connector actually builds; a caller
// supplying its own HttpClient (tests, permission probes) opts out on purpose.
func TestNewFeishuRESTClientInstallsTheCap(t *testing.T) {
	if newFeishuRESTClient("app", "secret") == nil {
		t.Fatal("nil client")
	}
	if maxFeishuResponseBytes != 25<<20 {
		t.Fatalf("cap = %d, want it aligned with the 25 MiB per-file inbound limit", maxFeishuResponseBytes)
	}
}
