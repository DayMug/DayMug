package codexapp

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestRPCClientReadsMessageBeyondScannerLimit(t *testing.T) {
	const oldScannerLimit = 16 * 1024 * 1024
	payload := strings.Repeat("x", oldScannerLimit+1)
	result, err := json.Marshal(map[string]string{"payload": payload})
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	stdout.Grow(len(result) + 32)
	stdout.WriteString(`{"id":1,"result":`)
	stdout.Write(result)
	stdout.WriteByte('}')

	client := newClient(io.Discard, bytes.NewReader(stdout.Bytes()))
	response := make(chan wireMessage, 1)
	client.pending["1"] = response
	client.start()

	select {
	case msg := <-response:
		var got map[string]string
		if err := json.Unmarshal(msg.Result, &got); err != nil {
			t.Fatal(err)
		}
		if got["payload"] != payload {
			t.Fatalf("payload length = %d, want %d", len(got["payload"]), len(payload))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("oversized JSON-RPC message was not delivered")
	}

	<-client.done
	if err := client.readError(); err != nil {
		t.Fatalf("read error = %v", err)
	}
}
