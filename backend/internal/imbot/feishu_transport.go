package imbot

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
)

// maxFeishuResponseBytes caps every response body the Feishu REST client is
// allowed to read.
//
// The lark SDK offers no streaming download: Im.MessageResource.Get calls
// larkcore.Request, which does io.ReadAll on the body and hands us a
// bytes.Buffer that already holds the whole resource. Feishu allows a single
// message resource up to 100 MB, so by the time the bridge's size-limited
// writer sees its first byte the file is entirely on the heap and the limiter
// is decoration. Capping at the transport is the only layer left where the
// bytes can be refused before they are buffered.
//
// The value matches service.DefaultUploadMaxBytes (25 MiB), the per-file
// inbound attachment cap — anything the bridge would reject anyway is not
// worth buffering. No legitimate Feishu JSON API response comes close, so a
// single cap can cover the whole client instead of just the download path.
const maxFeishuResponseBytes int64 = 25 << 20

// errFeishuResponseTooLarge aborts a download whose body exceeds the cap.
// Callers surface it as an ordinary download failure; the bridge then skips
// that one attachment and keeps the rest of the message.
var errFeishuResponseTooLarge = errors.New("feishu 响应体超过 25 MiB 上限")

// newFeishuRESTClient builds the lark client used for every REST call, with
// the response cap installed. Extra options are applied after the cap so tests
// can point the client at an httptest server; a caller that supplies its own
// HttpClient deliberately opts out.
func newFeishuRESTClient(appID, appSecret string, options ...lark.ClientOptionFunc) *lark.Client {
	opts := make([]lark.ClientOptionFunc, 0, len(options)+1)
	opts = append(opts, lark.WithHttpClient(&cappedHTTPClient{max: maxFeishuResponseBytes}))
	opts = append(opts, options...)
	return lark.NewClient(appID, appSecret, opts...)
}

// cappedHTTPClient refuses an oversized response before the SDK buffers it.
// A declared Content-Length over the cap is rejected without reading a byte;
// a chunked or mis-declared body is cut off once it passes the cap, so the
// worst case held in memory is the cap rather than the full resource.
type cappedHTTPClient struct {
	inner larkcore.HttpClient
	max   int64
}

func (c *cappedHTTPClient) Do(req *http.Request) (*http.Response, error) {
	inner := c.inner
	if inner == nil {
		inner = http.DefaultClient
	}
	resp, err := inner.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.Body == nil {
		return resp, nil
	}
	if resp.ContentLength > c.max {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: Content-Length %d", errFeishuResponseTooLarge, resp.ContentLength)
	}
	resp.Body = &cappedBody{inner: resp.Body, limit: c.max}
	return resp, nil
}

type cappedBody struct {
	inner io.ReadCloser
	limit int64
	read  int64
}

// Read allows exactly limit bytes through. One extra byte is read past the
// limit purely to tell "body is exactly at the cap" (fine) from "body is
// bigger than the cap" (rejected); it is never handed back to the caller
// without the error.
func (b *cappedBody) Read(p []byte) (int, error) {
	if allowed := b.limit - b.read + 1; int64(len(p)) > allowed {
		p = p[:allowed]
	}
	n, err := b.inner.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, errFeishuResponseTooLarge
	}
	return n, err
}

func (b *cappedBody) Close() error { return b.inner.Close() }
