package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPBarkSenderPayload(t *testing.T) {
	cases := []struct {
		name     string
		clickURL string
		wantURL  bool
	}{
		{"with click url", "https://daymug.example.com/chat/u1/c1", true},
		{"without click url", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got map[string]string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &got)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			s := NewHTTPBarkSender()
			if err := s.Send(context.Background(), srv.URL, "Title", "Body", c.clickURL); err != nil {
				t.Fatalf("Send: %v", err)
			}

			if got["title"] != "Title" || got["body"] != "Body" {
				t.Errorf("title/body mismatch: %#v", got)
			}
			url, present := got["url"]
			if c.wantURL {
				if !present || url != c.clickURL {
					t.Errorf("expected url %q in payload, got %q (present=%v)", c.clickURL, url, present)
				}
			} else if present {
				t.Errorf("expected no url field, got %q", url)
			}
		})
	}
}
