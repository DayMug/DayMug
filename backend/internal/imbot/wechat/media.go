package wechat

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultCDNBase is where message media lives. The gateway normally hands back
// a complete URL; this is only the fallback for building one.
const DefaultCDNBase = "https://novac2c.cdn.weixin.qq.com/c2c"

// Upload media types (proto: UploadMediaType).
const (
	UploadMediaImage = 1
	UploadMediaVideo = 2
	UploadMediaFile  = 3
	UploadMediaVoice = 4
)

const (
	epGetUploadURL = "ilink/bot/getuploadurl"
	// cdnTimeout bounds one CDN transfer. Media is capped well below what a
	// minute of transfer allows, so a slower one is stuck rather than large.
	cdnTimeout = 60 * time.Second
	// cdnUploadAttempts retries a CDN write. A 4xx aborts immediately: it
	// means the presigned parameters are wrong, and repeating cannot fix that.
	cdnUploadAttempts = 3
)

// MediaRef is everything needed to fetch and decrypt one piece of media.
type MediaRef struct {
	// URL is the complete download URL when the gateway supplied one.
	URL string
	// QueryParam builds a URL against the CDN base when URL is empty.
	QueryParam string
	Key        []byte
}

// Empty reports a reference with nothing to fetch.
func (r MediaRef) Empty() bool { return r.URL == "" && r.QueryParam == "" }

func (r MediaRef) downloadURL(cdnBase string) string {
	if r.URL != "" {
		return r.URL
	}
	base := strings.TrimRight(cdnBase, "/")
	if base == "" {
		base = DefaultCDNBase
	}
	return base + "/download?encrypted_query_param=" + url.QueryEscape(r.QueryParam)
}

// mediaRefFrom resolves the CDN reference and key from a media block.
//
// hexKey is the item-level `aeskey` field, which the gateway populates for
// images and prefers over the base64 key inside the media block. Callers pass
// "" when their item type has no such field.
func mediaRefFrom(media *CDNMedia, hexKey string) MediaRef {
	if media == nil {
		return MediaRef{}
	}
	ref := MediaRef{URL: strings.TrimSpace(media.FullURL), QueryParam: media.EncryptQueryParam}
	if hexKey != "" {
		if raw, err := hex.DecodeString(strings.TrimSpace(hexKey)); err == nil {
			ref.Key = raw
		}
	}
	if len(ref.Key) == 0 && media.AESKey != "" {
		ref.Key = decodeMediaKey(media.AESKey)
	}
	return ref
}

// decodeMediaKey unpacks a media block's `aes_key`, which carries the same
// 16-byte key in either of two encodings depending on who produced it and for
// which item type: base64 of the raw bytes (the gateway's inbound images) or
// base64 of the 32-char hex text (inbound file/voice/video, and everything
// Tencent's plugin sends outbound). Guessing wrong yields a 32-byte blob that
// crypto/aes happily accepts as an AES-256 key, so the failure is silent
// garbage rather than an error — hence the explicit length test.
func decodeMediaKey(aesKey string) []byte {
	raw, err := base64.StdEncoding.DecodeString(aesKey)
	if err != nil {
		return nil
	}
	if len(raw) == aesKeyLen {
		return raw
	}
	if decoded, err := hex.DecodeString(string(raw)); err == nil && len(decoded) == aesKeyLen {
		return decoded
	}
	return nil
}

// MediaOf resolves the downloadable media carried by one message item, along
// with a suggested file name. It reports ok=false for items that carry none.
func MediaOf(item *MessageItem) (ref MediaRef, name string, ok bool) {
	if item == nil {
		return MediaRef{}, "", false
	}
	switch item.Type {
	case ItemTypeImage:
		if item.ImageItem == nil {
			return MediaRef{}, "", false
		}
		ref = mediaRefFrom(item.ImageItem.Media, item.ImageItem.AESKey)
		name = "image.jpg"
	case ItemTypeVoice:
		if item.VoiceItem == nil {
			return MediaRef{}, "", false
		}
		ref = mediaRefFrom(item.VoiceItem.Media, "")
		// Weixin voice is SILK, which nothing downstream decodes. The gateway
		// already supplies a transcription in VoiceItem.Text, so the audio is
		// only worth keeping as an opaque attachment.
		name = "voice.silk"
	case ItemTypeFile:
		if item.FileItem == nil {
			return MediaRef{}, "", false
		}
		ref = mediaRefFrom(item.FileItem.Media, "")
		name = strings.TrimSpace(item.FileItem.FileName)
		if name == "" {
			name = "file.bin"
		}
	case ItemTypeVideo:
		if item.VideoItem == nil {
			return MediaRef{}, "", false
		}
		ref = mediaRefFrom(item.VideoItem.Media, "")
		name = "video.mp4"
	default:
		return MediaRef{}, "", false
	}
	if ref.Empty() {
		return MediaRef{}, "", false
	}
	return ref, name, true
}

// DownloadMedia fetches one piece of media and writes the plaintext to dst.
// maxBytes caps the encrypted body; a larger one is refused before it is
// buffered rather than after.
func (c *Client) DownloadMedia(ctx context.Context, ref MediaRef, dst io.Writer, maxBytes int64) error {
	if ref.Empty() {
		return errors.New("wechat: media reference is empty")
	}
	ctx, cancel := context.WithTimeout(ctx, cdnTimeout)
	defer cancel()

	target := ref.downloadURL(c.cdnBase)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("wechat: build media request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("wechat: download media: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wechat: download media: HTTP %d", resp.StatusCode)
	}
	if maxBytes > 0 && resp.ContentLength > maxBytes {
		return fmt.Errorf("wechat: media is %d bytes, over the %d byte cap", resp.ContentLength, maxBytes)
	}
	limit := maxBytes
	if limit <= 0 {
		limit = maxResponseBytes
	}
	// One byte past the cap distinguishes "exactly at the cap" from "over it".
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("wechat: read media: %w", err)
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("wechat: media exceeds the %d byte cap", limit)
	}

	plaintext := body
	if len(ref.Key) > 0 {
		if plaintext, err = decryptECB(body, ref.Key); err != nil {
			return fmt.Errorf("wechat: decrypt media: %w", err)
		}
	}
	if _, err := dst.Write(plaintext); err != nil {
		return fmt.Errorf("wechat: write media: %w", err)
	}
	return nil
}

// UploadedMedia is what SendItem needs to reference a freshly uploaded blob.
type UploadedMedia struct {
	EncryptQueryParam string
	AESKey            []byte
	PlainSize         int
	CipherSize        int
}

// CDNMedia renders the reference the message item carries.
//
// aes_key is base64 of the key's *hex text*, not of the key bytes — the
// 16-byte key becomes 32 ASCII hex chars, and those 32 bytes are what gets
// base64-encoded. Tencent's own plugin does exactly this for every outbound
// media type (`Buffer.from(uploaded.aeskey).toString("base64")` over a hex
// string, @tencent-weixin/openclaw-weixin src/messaging/send.ts), and its
// downloader documents both encodings side by side. Sending base64 of the raw
// bytes instead uploads fine, gets ret=0, and delivers a message the client
// renders blank — see decodeMediaKey for the receiving half.
func (u *UploadedMedia) CDNMedia() *CDNMedia {
	return &CDNMedia{
		EncryptQueryParam: u.EncryptQueryParam,
		AESKey:            base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(u.AESKey))),
		// 1 means the parameter packs the thumbnail/mid-size descriptors too,
		// which is what the gateway returns for a bot upload.
		EncryptType: 1,
	}
}

// UploadMedia encrypts data and stores it on the CDN for one recipient.
func (c *Client) UploadMedia(ctx context.Context, data []byte, mediaType int, toUserID string) (*UploadedMedia, error) {
	key := make([]byte, aesKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("wechat: generate media key: %w", err)
	}
	fileKeyRaw := make([]byte, 16)
	if _, err := rand.Read(fileKeyRaw); err != nil {
		return nil, fmt.Errorf("wechat: generate file key: %w", err)
	}
	fileKey := hex.EncodeToString(fileKeyRaw)

	sum := md5.Sum(data) //nolint:gosec // The gateway specifies MD5 as the integrity field; it is not used as a security primitive.
	req := map[string]any{
		"filekey":       fileKey,
		"media_type":    mediaType,
		"to_user_id":    toUserID,
		"rawsize":       len(data),
		"rawfilemd5":    hex.EncodeToString(sum[:]),
		"filesize":      CiphertextSize(len(data)),
		"aeskey":        hex.EncodeToString(key),
		"no_need_thumb": true,
		"base_info":     c.baseInfo(),
	}
	var resp struct {
		Ret              int    `json:"ret"`
		ErrMsg           string `json:"errmsg"`
		UploadParam      string `json:"upload_param"`
		UploadFullURL    string `json:"upload_full_url"`
		ThumbUploadParam string `json:"thumb_upload_param"`
	}
	if err := c.doJSON(ctx, epGetUploadURL, req, &resp, DefaultAPITimeout); err != nil {
		return nil, err
	}
	if resp.Ret != 0 {
		return nil, &APIError{Endpoint: epGetUploadURL, Ret: resp.Ret, Message: resp.ErrMsg}
	}

	target := strings.TrimSpace(resp.UploadFullURL)
	if target == "" {
		if resp.UploadParam == "" {
			return nil, errors.New("wechat: getUploadUrl returned no upload target")
		}
		base := strings.TrimRight(c.cdnBase, "/")
		if base == "" {
			base = DefaultCDNBase
		}
		target = base + "/upload?encrypted_query_param=" + url.QueryEscape(resp.UploadParam) +
			"&filekey=" + url.QueryEscape(fileKey)
	}

	ciphertext, err := encryptECB(data, key)
	if err != nil {
		return nil, err
	}
	param, err := c.putToCDN(ctx, target, ciphertext)
	if err != nil {
		return nil, err
	}
	return &UploadedMedia{
		EncryptQueryParam: param,
		AESKey:            key,
		PlainSize:         len(data),
		CipherSize:        len(ciphertext),
	}, nil
}

// putToCDN writes the encrypted body and returns the download parameter the
// CDN hands back in a response header.
func (c *Client) putToCDN(ctx context.Context, target string, ciphertext []byte) (string, error) {
	var lastErr error
	for range cdnUploadAttempts {
		param, retryable, err := c.putToCDNOnce(ctx, target, ciphertext)
		if err == nil {
			return param, nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("wechat: CDN upload failed after %d attempts: %w", cdnUploadAttempts, lastErr)
}

func (c *Client) putToCDNOnce(ctx context.Context, target string, ciphertext []byte) (param string, retryable bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, cdnTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(ciphertext))
	if err != nil {
		return "", false, fmt.Errorf("wechat: build CDN upload: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", true, fmt.Errorf("wechat: CDN upload: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		// Wrong presigned parameters; repeating cannot fix them.
		return "", false, fmt.Errorf("wechat: CDN rejected the upload: HTTP %d: %s",
			resp.StatusCode, cdnErrorMessage(resp, body))
	}
	if resp.StatusCode != http.StatusOK {
		return "", true, fmt.Errorf("wechat: CDN upload: HTTP %d: %s",
			resp.StatusCode, cdnErrorMessage(resp, body))
	}
	param = resp.Header.Get("x-encrypted-param")
	if param == "" {
		return "", true, errors.New("wechat: CDN upload response carried no x-encrypted-param header")
	}
	return param, false, nil
}

func cdnErrorMessage(resp *http.Response, body []byte) string {
	if msg := resp.Header.Get("x-error-message"); msg != "" {
		return msg
	}
	return truncate(string(body), 256)
}
