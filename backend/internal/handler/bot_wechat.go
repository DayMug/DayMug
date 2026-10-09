package handler

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/imbot/wechat"
)

// WeChat pairing is a two-step handshake driven by the browser: start it to
// get a QR code, then poll until the phone confirms. The credentials it yields
// are handed back to the form rather than written straight to the bot, because
// a pairing is just as likely to be part of creating a bot that does not exist
// yet as of re-pairing one that does. That also keeps the flow identical to
// how Slack and Feishu credentials reach the same form.
const (
	wechatPairingStartTimeout = 20 * time.Second
	// wechatPairingPollTimeout must outlast the gateway's long-poll window
	// (~30s) plus transport slack. A shorter deadline made every poll look
	// like a gateway failure, which surfaced in the browser as a 502.
	wechatPairingPollTimeout = 60 * time.Second
)

type wechatPairingStartResponse struct {
	Challenge string `json:"challenge"`
	// QRImage is base64 PNG, ready for an <img> data URL. Rendered here from
	// the URL the gateway returns, which is not itself an image.
	QRImage string `json:"qr_image,omitempty"`
	// QRContent is that same URL, so a client can offer it as a fallback when
	// the image will not render.
	QRContent string `json:"qr_content,omitempty"`
}

type wechatPairingStatusResponse struct {
	// Status is one of pending / scanned / confirmed / expired / blocked.
	// Anything the gateway reports that is not terminal collapses to pending,
	// so a slow scan is never mistaken for a failure.
	Status   string `json:"status"`
	BotToken string `json:"bot_token,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
}

func (h *BotHandler) wechatClient() *wechat.Client {
	return wechat.NewClient(wechat.ClientOptions{BaseURL: h.WeChatGateway, BotAgent: "DayMug"})
}

// StartWeChatPairing issues a QR challenge for the caller to scan.
func (h *BotHandler) StartWeChatPairing(c *gin.Context) {
	if _, ok := h.loadAgent(c); !ok {
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), wechatPairingStartTimeout)
	defer cancel()

	qr, err := h.wechatClient().RequestQRCode(ctx)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "微信网关未能返回二维码：" + err.Error()})
		return
	}

	resp := wechatPairingStartResponse{Challenge: qr.Code, QRContent: qr.ImageContent}
	if img, imgErr := qr.PNG(0); imgErr == nil {
		resp.QRImage = base64.StdEncoding.EncodeToString(img)
	}
	// A challenge with no rendered image is still usable by a client that
	// encodes QRContent itself, so a render failure does not fail the request.
	c.JSON(http.StatusOK, resp)
}

// PollWeChatPairing reports the state of a pending scan exactly once. The
// browser drives the retry loop so a closed tab ends the pairing instead of
// leaving a request parked on the server.
func (h *BotHandler) PollWeChatPairing(c *gin.Context) {
	if _, ok := h.loadAgent(c); !ok {
		return
	}
	challenge := strings.TrimSpace(c.Query("challenge"))
	if challenge == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "challenge is required"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), wechatPairingPollTimeout)
	defer cancel()

	// One round trip per request: the browser owns the retry loop, so holding
	// the request open here would only tie up a connection per pending scan.
	client := h.wechatClient()
	// Zero lets the protocol layer pick a deadline that clears the long-poll
	// window; hitting it comes back as "wait", not as an error.
	status, err := client.PollQRCode(ctx, challenge, 0)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "查询扫码状态失败：" + err.Error()})
		return
	}

	phase := status.Phase()
	if phase != wechat.QRPhaseConfirmed {
		c.JSON(http.StatusOK, wechatPairingStatusResponse{Status: phase})
		return
	}

	creds, err := status.Credentials(client.BaseURL())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, wechatPairingStatusResponse{
		Status: wechat.QRPhaseConfirmed, BotToken: creds.Token, BaseURL: creds.BaseURL,
	})
}
