package wechat

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadMediaDecryptsWithTheItemKey(t *testing.T) {
	key := []byte("0123456789abcdef")
	plaintext := []byte("the original bytes")
	ciphertext, err := encryptECB(plaintext, key)
	if err != nil {
		t.Fatalf("encryptECB: %v", err)
	}

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("encrypted_query_param"); got != "param-1" {
			t.Errorf("encrypted_query_param = %q", got)
		}
		_, _ = w.Write(ciphertext)
	}))
	defer cdn.Close()

	client := NewClient(ClientOptions{BaseURL: cdn.URL, CDNBase: cdn.URL, Token: "tok"})
	var buf bytes.Buffer
	err = client.DownloadMedia(context.Background(), MediaRef{QueryParam: "param-1", Key: key}, &buf, 1<<20)
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	if buf.String() != string(plaintext) {
		t.Errorf("downloaded %q, want %q", buf.String(), plaintext)
	}
}

// The gateway normally hands back a complete URL; it must win over the base.
func TestDownloadMediaPrefersTheGatewayURL(t *testing.T) {
	plaintext := []byte("plain body")
	var hit string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path
		_, _ = w.Write(plaintext)
	}))
	defer cdn.Close()

	client := NewClient(ClientOptions{BaseURL: cdn.URL, CDNBase: cdn.URL + "/unused"})
	var buf bytes.Buffer
	// No key: some media arrives unencrypted, and forcing a decrypt would fail.
	err := client.DownloadMedia(context.Background(), MediaRef{URL: cdn.URL + "/direct"}, &buf, 1<<20)
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	if hit != "/direct" {
		t.Errorf("fetched %q, want the gateway's own URL", hit)
	}
	if buf.String() != string(plaintext) {
		t.Errorf("downloaded %q", buf.String())
	}
}

func TestDownloadMediaRefusesOversizedBody(t *testing.T) {
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte{7}, 4096))
	}))
	defer cdn.Close()

	client := NewClient(ClientOptions{BaseURL: cdn.URL, CDNBase: cdn.URL})
	var buf bytes.Buffer
	err := client.DownloadMedia(context.Background(), MediaRef{QueryParam: "p"}, &buf, 512)
	if err == nil {
		t.Fatal("DownloadMedia accepted a body over the cap")
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes despite the failure", buf.Len())
	}
}

func TestMediaOf(t *testing.T) {
	hexKey := hex.EncodeToString([]byte("0123456789abcdef"))
	b64Key := base64.StdEncoding.EncodeToString([]byte("fedcba9876543210"))

	tests := []struct {
		name     string
		item     *MessageItem
		wantOK   bool
		wantName string
		wantKey  string
	}{
		{
			// The item-level hex key is the one the gateway populates for
			// images and is documented as preferred over media.aes_key.
			name: "image prefers the item-level hex key",
			item: &MessageItem{Type: ItemTypeImage, ImageItem: &ImageItem{
				Media: &CDNMedia{EncryptQueryParam: "p", AESKey: b64Key}, AESKey: hexKey,
			}},
			wantOK: true, wantName: "image.jpg", wantKey: "0123456789abcdef",
		},
		{
			name: "file keeps its own name",
			item: &MessageItem{Type: ItemTypeFile, FileItem: &FileItem{
				Media: &CDNMedia{EncryptQueryParam: "p", AESKey: b64Key}, FileName: "report.pdf",
			}},
			wantOK: true, wantName: "report.pdf", wantKey: "fedcba9876543210",
		},
		{
			// file/voice/video arrive with the key hex-encoded *inside* the
			// base64, which naively decodes to a 32-byte blob that crypto/aes
			// would accept as an AES-256 key and quietly decrypt to garbage.
			name: "file accepts a hex-in-base64 key",
			item: &MessageItem{Type: ItemTypeFile, FileItem: &FileItem{
				Media: &CDNMedia{
					EncryptQueryParam: "p",
					AESKey:            base64.StdEncoding.EncodeToString([]byte(hexKey)),
				},
				FileName: "report.pdf",
			}},
			wantOK: true, wantName: "report.pdf", wantKey: "0123456789abcdef",
		},
		{
			name: "unnamed file gets a fallback",
			item: &MessageItem{Type: ItemTypeFile, FileItem: &FileItem{
				Media: &CDNMedia{EncryptQueryParam: "p"},
			}},
			wantOK: true, wantName: "file.bin",
		},
		{
			name:   "text carries no media",
			item:   &MessageItem{Type: ItemTypeText, TextItem: &TextItem{Text: "hi"}},
			wantOK: false,
		},
		{
			name:   "an image with no CDN reference is not downloadable",
			item:   &MessageItem{Type: ItemTypeImage, ImageItem: &ImageItem{}},
			wantOK: false,
		},
		{"nil", nil, false, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, name, ok := MediaOf(tc.item)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if name != tc.wantName {
				t.Errorf("name = %q, want %q", name, tc.wantName)
			}
			if tc.wantKey != "" && string(ref.Key) != tc.wantKey {
				t.Errorf("key = %q, want %q", ref.Key, tc.wantKey)
			}
		})
	}
}

func TestUploadMediaEncryptsAndReportsTheDownloadParam(t *testing.T) {
	plaintext := []byte("artifact contents")
	var (
		uploadReq map[string]any
		gotCipher []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "getuploadurl"):
			if err := json.NewDecoder(r.Body).Decode(&uploadReq); err != nil {
				t.Errorf("decode: %v", err)
			}
			writeUploadURL(w, "http://"+r.Host+"/cdn/upload")
		case strings.HasSuffix(r.URL.Path, "/cdn/upload"):
			body, _ := io.ReadAll(r.Body)
			gotCipher = body
			if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
				t.Errorf("Content-Type = %q", got)
			}
			w.Header().Set("x-encrypted-param", "download-param")
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL, CDNBase: srv.URL, Token: "tok"})
	got, err := client.UploadMedia(context.Background(), plaintext, UploadMediaImage, "u1@im.wechat")
	if err != nil {
		t.Fatalf("UploadMedia: %v", err)
	}

	if got.EncryptQueryParam != "download-param" {
		t.Errorf("EncryptQueryParam = %q, want the CDN's x-encrypted-param header", got.EncryptQueryParam)
	}
	if got.PlainSize != len(plaintext) {
		t.Errorf("PlainSize = %d, want %d", got.PlainSize, len(plaintext))
	}
	if got.CipherSize != len(gotCipher) {
		t.Errorf("CipherSize = %d but %d bytes were uploaded", got.CipherSize, len(gotCipher))
	}

	// The declared sizes and digest describe the plaintext; the gateway
	// rejects the upload when they disagree with what lands on the CDN.
	sum := md5.Sum(plaintext) //nolint:gosec // matching the protocol's integrity field, not a security primitive
	if uploadReq["rawfilemd5"] != hex.EncodeToString(sum[:]) {
		t.Errorf("rawfilemd5 = %v, want the plaintext digest", uploadReq["rawfilemd5"])
	}
	if uploadReq["rawsize"] != float64(len(plaintext)) {
		t.Errorf("rawsize = %v, want %d", uploadReq["rawsize"], len(plaintext))
	}
	if uploadReq["filesize"] != float64(len(gotCipher)) {
		t.Errorf("filesize = %v, want the ciphertext length %d", uploadReq["filesize"], len(gotCipher))
	}
	if uploadReq["media_type"] != float64(UploadMediaImage) {
		t.Errorf("media_type = %v", uploadReq["media_type"])
	}

	// What lands on the CDN must be the ciphertext, decryptable with the key
	// the message item advertises.
	decrypted, err := decryptECB(gotCipher, got.AESKey)
	if err != nil {
		t.Fatalf("decrypt uploaded body: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Errorf("uploaded body decrypts to %q, want %q", decrypted, plaintext)
	}
	// aes_key carries the key's hex *text*, base64-encoded — base64 of the raw
	// 16 bytes is what made the client render a blank image.
	media := got.CDNMedia()
	if want := base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(got.AESKey))); media.AESKey != want {
		t.Errorf("CDNMedia aes_key = %q, want base64 of the hex text %q", media.AESKey, want)
	}
	if key := decodeMediaKey(media.AESKey); !bytes.Equal(key, got.AESKey) {
		t.Errorf("aes_key does not round-trip: got %x, want %x", key, got.AESKey)
	}
}

// A 4xx means the presigned parameters are wrong, so repeating cannot help.
func TestUploadMediaDoesNotRetryClientErrors(t *testing.T) {
	uploads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getuploadurl") {
			writeUploadURL(w, "http://"+r.Host+"/cdn/upload")
			return
		}
		uploads++
		w.Header().Set("x-error-message", "bad signature")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL, CDNBase: srv.URL, Token: "tok"})
	_, err := client.UploadMedia(context.Background(), []byte("x"), UploadMediaFile, "u1@im.wechat")
	if err == nil {
		t.Fatal("UploadMedia succeeded on a 403")
	}
	if uploads != 1 {
		t.Errorf("uploaded %d times, want exactly 1", uploads)
	}
	if !strings.Contains(err.Error(), "bad signature") {
		t.Errorf("err = %v, want the CDN's own message", err)
	}
}

func TestUploadMediaRetriesServerErrors(t *testing.T) {
	uploads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getuploadurl") {
			writeUploadURL(w, "http://"+r.Host+"/cdn/upload")
			return
		}
		uploads++
		if uploads < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("x-encrypted-param", "ok-param")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL, CDNBase: srv.URL, Token: "tok"})
	got, err := client.UploadMedia(context.Background(), []byte("x"), UploadMediaFile, "u1@im.wechat")
	if err != nil {
		t.Fatalf("UploadMedia: %v", err)
	}
	if got.EncryptQueryParam != "ok-param" {
		t.Errorf("EncryptQueryParam = %q", got.EncryptQueryParam)
	}
	if uploads != 3 {
		t.Errorf("uploaded %d times, want 3", uploads)
	}
}

// Losing the header means the media can never be referenced, so the upload has
// not actually succeeded even though the CDN said 200.
func TestUploadMediaFailsWithoutTheDownloadParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getuploadurl") {
			writeUploadURL(w, "http://"+r.Host+"/cdn/upload")
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(ClientOptions{BaseURL: srv.URL, CDNBase: srv.URL, Token: "tok"})
	if _, err := client.UploadMedia(context.Background(), []byte("x"), UploadMediaFile, "u"); err == nil {
		t.Fatal("UploadMedia succeeded without an x-encrypted-param header")
	}
}

func writeUploadURL(w http.ResponseWriter, fullURL string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ret": 0, "upload_full_url": fullURL})
}
