# Third-party notices

DayMug ships as a single binary: the Go backend with the frontend build
embedded. This file lists the third-party software that ends up in it.

## Frontend bundle

`pnpm build` generates `dist/third-party-licenses.txt` (see the
`thirdPartyLicenses` plugin in `frontend/vite.config.ts`). It lists every npm
package the bundle contains, with each package's license text, and is derived
from the build's module graph, so it always matches what is shipped. The file
is embedded in the binary and served at `/third-party-licenses.txt`.

It also covers what reaches the bundle outside the module graph:

- **@casualoffice/sheets** (Apache License 2.0,
  https://github.com/CasualOffice/sheets) and **@casualoffice/docs** (MIT,
  Copyright (c) EigenPal, https://docx-editor.dev/). These two editors are
  copied into the bundle by `frontend/scripts/sync-casual-office.mjs`, which
  **modifies** them at build time: it replaces some UI labels with localised
  text, injects a translation dictionary into the docs editor, and generates
  its own `embed.html` for each instead of using the upstream file.
- **Inter** (`@fontsource-variable/inter`, SIL Open Font License 1.1).
- **Material Symbols** (`@material-symbols/font-400`, Apache License 2.0).

For reference, the @casualoffice/docs license:

```
Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

The Apache License 2.0 text is the unmodified license reproduced after the
DayMug-specific condition in `LICENSE`.

## Go modules

Compiled into the binary (from `go-licenses report ./cmd/server` in
`backend/`; re-run it after changing `backend/go.mod`):

| Module | License | License text |
|---|---|---|
| `github.com/coder/websocket` | ISC | https://github.com/coder/websocket/blob/v1.8.14/LICENSE.txt |
| `github.com/coreos/go-oidc/v3/oidc` | Apache-2.0 | https://github.com/coreos/go-oidc/blob/v3.18.0/LICENSE |
| `github.com/creack/pty` | MIT | https://github.com/creack/pty/blob/v1.1.24/LICENSE |
| `github.com/dustin/go-humanize` | MIT | https://github.com/dustin/go-humanize/blob/v1.0.1/LICENSE |
| `github.com/fsnotify/fsnotify` | BSD-3-Clause | https://github.com/fsnotify/fsnotify/blob/v1.9.0/LICENSE |
| `github.com/gabriel-vasile/mimetype` | MIT | https://github.com/gabriel-vasile/mimetype/blob/v1.4.12/LICENSE |
| `github.com/gin-contrib/sse` | MIT | https://github.com/gin-contrib/sse/blob/v1.1.0/LICENSE |
| `github.com/gin-gonic/gin` | MIT | https://github.com/gin-gonic/gin/blob/v1.12.0/LICENSE |
| `github.com/go-jose/go-jose/v4` | Apache-2.0 | https://github.com/go-jose/go-jose/blob/v4.1.4/LICENSE |
| `github.com/go-jose/go-jose/v4/json` | BSD-3-Clause | https://github.com/go-jose/go-jose/blob/v4.1.4/json/LICENSE |
| `github.com/go-playground/locales` | MIT | https://github.com/go-playground/locales/blob/v0.14.1/LICENSE |
| `github.com/go-playground/universal-translator` | MIT | https://github.com/go-playground/universal-translator/blob/v0.18.1/LICENSE |
| `github.com/go-playground/validator/v10` | MIT | https://github.com/go-playground/validator/blob/v10.30.1/LICENSE |
| `github.com/goccy/go-yaml` | MIT | https://github.com/goccy/go-yaml/blob/v1.19.2/LICENSE |
| `github.com/gogo/protobuf` | BSD-3-Clause | https://github.com/gogo/protobuf/blob/v1.3.2/LICENSE |
| `github.com/google/uuid` | BSD-3-Clause | https://github.com/google/uuid/blob/v1.6.0/LICENSE |
| `github.com/gorilla/websocket` | BSD-2-Clause | https://github.com/gorilla/websocket/blob/v1.5.3/LICENSE |
| `github.com/larksuite/oapi-sdk-go/v3` | MIT | https://github.com/larksuite/oapi-sdk-go/blob/v3.9.9/LICENSE |
| `github.com/leodido/go-urn` | MIT | https://github.com/leodido/go-urn/blob/v1.4.0/LICENSE |
| `github.com/mattn/go-isatty` | MIT | https://github.com/mattn/go-isatty/blob/v0.0.20/LICENSE |
| `github.com/pelletier/go-toml/v2` | MIT | https://github.com/pelletier/go-toml/blob/v2.2.4/LICENSE |
| `github.com/quic-go/qpack` | MIT | https://github.com/quic-go/qpack/blob/v0.6.0/LICENSE.md |
| `github.com/quic-go/quic-go` | MIT | https://github.com/quic-go/quic-go/blob/v0.59.0/LICENSE |
| `github.com/remyoudompheng/bigfft` | BSD-3-Clause | https://github.com/remyoudompheng/bigfft/blob/24d4a6f8daec/LICENSE |
| `github.com/robfig/cron/v3` | MIT | https://github.com/robfig/cron/blob/v3.0.1/LICENSE |
| `github.com/skip2/go-qrcode` | MIT | https://github.com/skip2/go-qrcode/blob/da1b6568686e/LICENSE |
| `github.com/slack-go/slack` | BSD-2-Clause | https://github.com/slack-go/slack/blob/v0.27.0/LICENSE |
| `github.com/ugorji/go/codec` | MIT | https://github.com/ugorji/go/blob/codec/v1.3.1/codec/LICENSE |
| `go.mongodb.org/mongo-driver/v2` | Apache-2.0 | https://github.com/mongodb/mongo-go-driver/blob/v2.5.0/LICENSE |
| `golang.org/x/crypto` | BSD-3-Clause | https://cs.opensource.google/go/x/crypto/+/v0.48.0:LICENSE |
| `golang.org/x/net` | BSD-3-Clause | https://cs.opensource.google/go/x/net/+/v0.51.0:LICENSE |
| `golang.org/x/oauth2` | BSD-3-Clause | https://cs.opensource.google/go/x/oauth2/+/v0.36.0:LICENSE |
| `golang.org/x/sys` | BSD-3-Clause | https://cs.opensource.google/go/x/sys/+/v0.42.0:LICENSE |
| `golang.org/x/term` | BSD-3-Clause | https://cs.opensource.google/go/x/term/+/v0.40.0:LICENSE |
| `golang.org/x/text` | BSD-3-Clause | https://cs.opensource.google/go/x/text/+/v0.35.0:LICENSE |
| `google.golang.org/protobuf` | BSD-3-Clause | https://github.com/protocolbuffers/protobuf-go/blob/v1.36.10/LICENSE |
| `modernc.org/libc` | BSD-3-Clause | https://gitlab.com/cznic/libc/-/blob/v1.70.0/LICENSE |
| `modernc.org/mathutil` | BSD-3-Clause | https://gitlab.com/cznic/mathutil/-/blob/v1.7.1/LICENSE |
| `modernc.org/memory` | BSD-3-Clause | https://gitlab.com/cznic/memory/-/blob/v1.11.0/LICENSE |
| `modernc.org/sqlite` | BSD-3-Clause | https://gitlab.com/cznic/sqlite/-/blob/v1.48.1/LICENSE |
