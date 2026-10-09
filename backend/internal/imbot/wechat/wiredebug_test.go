package wechat

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captureLog redirects the standard logger for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	flags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(nil)
		log.SetFlags(flags)
	})
	return buf
}

// enableWireDebug forces the switch on for one test. The production value is
// read once from the environment, which a test cannot change after start.
func enableWireDebug(t *testing.T) {
	t.Helper()
	previous := wireDebugEnabled
	wireDebugEnabled = func() bool { return true }
	t.Cleanup(func() { wireDebugEnabled = previous })
}

// The whole point of logging raw bytes is to reveal fields these structs do not
// declare — json.Unmarshal drops those silently, so a decoded value can never
// show them.
func TestWireDebugLogsFieldsTheStructsDoNotDeclare(t *testing.T) {
	enableWireDebug(t)
	buf := captureLog(t)

	const undeclared = `"a_field_go_never_heard_of":"surprise"`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0,` + undeclared + `}`))
	}))
	defer server.Close()

	c := NewClient(ClientOptions{BaseURL: server.URL})
	var out struct {
		Ret int `json:"ret"`
	}
	if err := c.doJSON(context.Background(), "ilink/bot/whatever", map[string]any{"hello": "world"}, &out, time.Second); err != nil {
		t.Fatalf("doJSON: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, undeclared) {
		t.Errorf("the undeclared field is missing from the log:\n%s", logged)
	}
	if !strings.Contains(logged, `"hello":"world"`) {
		t.Errorf("the request body is missing from the log:\n%s", logged)
	}
}

func TestWireDebugStaysSilentByDefault(t *testing.T) {
	buf := captureLog(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	c := NewClient(ClientOptions{BaseURL: server.URL})
	if err := c.doJSON(context.Background(), "ilink/bot/whatever", map[string]any{"k": "v"}, &struct{}{}, time.Second); err != nil {
		t.Fatalf("doJSON: %v", err)
	}

	// Bodies carry AES media keys and context tokens; leaking them without an
	// explicit opt-in is the failure this guards.
	if buf.Len() != 0 {
		t.Errorf("wire debug logged while disabled:\n%s", buf.String())
	}
}
