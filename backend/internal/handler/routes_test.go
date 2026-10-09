package handler

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"

	"github.com/DayMug/DayMug/backend/internal/service"
)

// Verify the SPA fallback never serves index.html under a JS/CSS asset URL.
// A stale browser holding /assets/index-OLDHASH.js after a redeploy must see
// 404, otherwise it gets index.html with text/html and trips the strict
// module MIME-type check.
func TestFrontendNoRoute_AssetNotFoundReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fsys := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><html></html>")},
		"assets/index-NEW.js":  {Data: []byte("export {};")},
		"assets/index-NEW.css": {Data: []byte("body{}")},
		"favicon.svg":          {Data: []byte("<svg/>")},
		"apple-touch-icon.png": {Data: []byte("\x89PNG")},
	}

	r := gin.New()
	if err := RegisterRoutes(r, nil, &service.Drainer{}, nil, RegisterRoutesOpts{FrontendFS: fsys}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		path       string
		wantStatus int
		wantCT     string // empty means don't check
		wantCache  string // empty means don't check
	}{
		{"existing js asset", "/assets/index-NEW.js", http.StatusOK, "text/javascript", "public, max-age=31536000, immutable"},
		{"existing css asset", "/assets/index-NEW.css", http.StatusOK, "text/css", "public, max-age=31536000, immutable"},
		{"existing favicon", "/favicon.svg", http.StatusOK, "", "no-cache"},
		{"existing apple touch icon", "/apple-touch-icon.png", http.StatusOK, "image/png", "no-cache"},
		{"stale js asset", "/assets/index-OLDHASH.js", http.StatusNotFound, "", ""},
		{"stale css asset", "/assets/styles-OLD.css", http.StatusNotFound, "", ""},
		{"stale image", "/assets/logo-OLD.png", http.StatusNotFound, "", ""},
		{"client route falls back to index.html", "/settings/admin", http.StatusOK, "text/html", "no-cache"},
		{"root falls back to index.html", "/", http.StatusOK, "text/html", "no-cache"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, http.NoBody)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body=%q)", w.Code, tc.wantStatus, w.Body.String())
			}
			if tc.wantCT != "" {
				ct := w.Header().Get("Content-Type")
				if !containsCT(ct, tc.wantCT) {
					t.Fatalf("Content-Type = %q, want it to contain %q", ct, tc.wantCT)
				}
			}
			if tc.wantCache != "" {
				cc := w.Header().Get("Cache-Control")
				if cc != tc.wantCache {
					t.Fatalf("Cache-Control = %q, want %q", cc, tc.wantCache)
				}
			}
		})
	}
}

// Release builds embed JS/CSS gzip-precompressed and drop the raw originals.
// A request for the logical asset path must then be served from the .gz
// sibling: compressed verbatim when the client accepts gzip, decompressed
// otherwise — never a 404.
func TestFrontendNoRoute_ServesPrecompressedAsset(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const jsBody = "export const x = 1;"
	var gzBuf bytes.Buffer
	zw := gzip.NewWriter(&gzBuf)
	if _, err := zw.Write([]byte(jsBody)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	gzBytes := gzBuf.Bytes()

	// Only the .gz exists — exactly what the release embed FS ships.
	fsys := fstest.MapFS{
		"index.html":              {Data: []byte("<!doctype html><html></html>")},
		"assets/index-NEW.js.gz":  {Data: gzBytes},
		"assets/index-NEW.css.gz": {Data: gzBytes},
	}

	r := gin.New()
	if err := RegisterRoutes(r, nil, &service.Drainer{}, nil, RegisterRoutesOpts{FrontendFS: fsys}); err != nil {
		t.Fatal(err)
	}

	t.Run("gzip client gets compressed bytes verbatim", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/index-NEW.js", http.NoBody)
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if enc := w.Header().Get("Content-Encoding"); enc != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", enc)
		}
		if !containsCT(w.Header().Get("Content-Type"), "text/javascript") {
			t.Fatalf("Content-Type = %q, want text/javascript", w.Header().Get("Content-Type"))
		}
		if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Fatalf("Cache-Control = %q", cc)
		}
		if !bytes.Equal(w.Body.Bytes(), gzBytes) {
			t.Fatalf("body is not the stored gzip bytes")
		}
	})

	t.Run("non-gzip client gets decompressed body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/index-NEW.js", http.NoBody)
		// No Accept-Encoding header.
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if enc := w.Header().Get("Content-Encoding"); enc != "" {
			t.Fatalf("Content-Encoding = %q, want empty", enc)
		}
		if w.Body.String() != jsBody {
			t.Fatalf("body = %q, want %q", w.Body.String(), jsBody)
		}
	})

	t.Run("missing asset with no gz sibling still 404s", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/assets/gone-OLD.js", http.NoBody)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", w.Code)
		}
	})
}

func containsCT(got, want string) bool {
	// Content-Type may include "; charset=utf-8" — substring match is enough.
	return len(got) >= len(want) && (got == want || (len(got) > len(want) && got[:len(want)] == want))
}

// The office editor embeds are staged into the build already gzipped and with
// no raw sibling, under version directories rather than content-hashed names.
// They therefore need the same two rules /assets/ gets — serve the .gz, and
// cache it for a year — plus a JavaScript content type for .mjs, which the
// document runtime uses and which some hosts have no mime entry for.
func TestFrontendNoRoute_ServesOfficeEmbedAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const body = "export const mountEmbedded = () => {};"
	var gzBuf bytes.Buffer
	zw := gzip.NewWriter(&gzBuf)
	if _, err := zw.Write([]byte(body)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	fsys := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html></html>")},
		"casual-office/sheets/0.20.0/embed-runtime.js.gz": {Data: gzBuf.Bytes()},
		"casual-office/docs/1.4.2/embed-runtime.mjs.gz":   {Data: gzBuf.Bytes()},
		"casual-office/sheets/0.20.0/embed.html":          {Data: []byte("<!doctype html>")},
	}

	r := gin.New()
	if err := RegisterRoutes(r, nil, &service.Drainer{}, nil, RegisterRoutesOpts{FrontendFS: fsys}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		wantCT  string
		wantGz  bool
		wantSrc string
	}{
		{
			name:   "spreadsheet runtime comes from its .gz sibling",
			path:   "/casual-office/sheets/0.20.0/embed-runtime.js",
			wantCT: "text/javascript",
			wantGz: true,
		},
		{
			name:   "document runtime .mjs is typed as JavaScript",
			path:   "/casual-office/docs/1.4.2/embed-runtime.mjs",
			wantCT: "text/javascript",
			wantGz: true,
		},
		{
			name:    "the generated embed.html is served raw",
			path:    "/casual-office/sheets/0.20.0/embed.html",
			wantCT:  "text/html",
			wantSrc: "<!doctype html>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, http.NoBody)
			req.Header.Set("Accept-Encoding", "gzip")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if !containsCT(w.Header().Get("Content-Type"), tc.wantCT) {
				t.Fatalf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tc.wantCT)
			}
			if cc := w.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
				t.Fatalf("Cache-Control = %q, want immutable", cc)
			}
			if tc.wantGz {
				if enc := w.Header().Get("Content-Encoding"); enc != "gzip" {
					t.Fatalf("Content-Encoding = %q, want gzip", enc)
				}
				if !bytes.Equal(w.Body.Bytes(), gzBuf.Bytes()) {
					t.Fatalf("body is not the stored gzip bytes")
				}
			}
			if tc.wantSrc != "" && w.Body.String() != tc.wantSrc {
				t.Fatalf("body = %q, want %q", w.Body.String(), tc.wantSrc)
			}
		})
	}
}
