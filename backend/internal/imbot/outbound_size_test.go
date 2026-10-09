package imbot

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// Every outbound cap belongs to the platform that enforces it. DayMug used to
// reject anything over 25 MiB while extracting the publish marker — before the
// transport was even known — so a 50 MB PDF bound for Slack (which takes 1 GiB)
// was dropped silently, and the answer went out still claiming it was attached.
// Each connector now answers for its own limit, and says so out loud.
func TestOutboundUploadsRefuseOnlyPastTheirOwnPlatformLimit(t *testing.T) {
	openRefused := func() (io.ReadCloser, error) {
		return nil, errors.New("upload opened a file it had already refused")
	}

	tests := []struct {
		platform string
		limit    int64
		upload   func(OutboundAttachment) error
	}{
		{
			platform: "slack",
			limit:    slackMaxUploadBytes,
			upload: func(a OutboundAttachment) error {
				return (&SlackConnector{}).uploadInThread(context.Background(), Message{}, a)
			},
		},
		{
			platform: "feishu",
			limit:    feishuMaxUploadBytes,
			upload: func(a OutboundAttachment) error {
				return (&FeishuConnector{}).uploadInThread(context.Background(), Message{}, a)
			},
		},
		{
			platform: "telegram",
			limit:    telegramMaxUploadBytes,
			upload: func(a OutboundAttachment) error {
				return (&TelegramConnector{}).uploadInThread(context.Background(), Message{}, a)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.platform, func(t *testing.T) {
			if tc.limit <= 25<<20 {
				t.Fatalf("%s cap = %d bytes, at or below the old blanket 25 MiB — the bug is back", tc.platform, tc.limit)
			}
			err := tc.upload(OutboundAttachment{Name: "candidate.pdf", Size: tc.limit + 1, Open: openRefused})
			if err == nil {
				t.Fatalf("%s accepted a file past its own limit", tc.platform)
			}
			// The reader sees this text in the thread, so it has to name the
			// file and say the platform refused it — not read as a crash.
			if !strings.Contains(err.Error(), "candidate.pdf") || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("%s refusal = %v, want it to name the file and the limit", tc.platform, err)
			}
		})
	}
}
