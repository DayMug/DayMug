package wechat

import (
	"log"
	"os"
	"sync"
)

// WireDebugEnv turns on raw protocol body logging when set to "1".
const WireDebugEnv = "DAYMUG_WECHAT_WIRE_DEBUG"

// wireDebugMaxBytes bounds one logged body. A media message references the CDN
// rather than inlining bytes, so this is far more than one message needs while
// still keeping a burst of long-poll results out of the journal.
const wireDebugMaxBytes = 16 << 10

// wireDebugEnabled is read once: the value cannot change within a process, and
// the check sits on every request.
var wireDebugEnabled = sync.OnceValue(func() bool {
	return os.Getenv(WireDebugEnv) == "1"
})

// logWire prints one raw protocol body.
//
// This exists because the protocol was reverse-engineered from plugin source
// with no vendor documentation, which makes the bytes a real gateway sends the
// only ground truth there is. A field these structs fail to declare is dropped
// silently by json.Unmarshal — the failure leaves no trace in a decoded struct,
// so reading Go types can never reveal it. Logging happens before decoding for
// exactly that reason.
//
// Off unless explicitly enabled: bodies carry AES media keys and context
// tokens, so this is an opt-in for debugging against a real account and must
// not be left on.
func logWire(endpoint, direction string, body []byte) {
	if !wireDebugEnabled() {
		return
	}
	log.Printf("wechat wire %s %s: %s", direction, endpoint, truncate(string(body), wireDebugMaxBytes))
}
