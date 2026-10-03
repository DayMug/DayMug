# WeChat integration (iLink Bot protocol)

Status: **the protocol layer, connector wiring, and inbound/outbound media are all done.** WeChat can be selected on the Agent edit page, paired by QR scan, and used to send and receive text and files. The only missing piece is tool-call progress mapping; see the end of this document for why.

## What this is

The WeChat side speaks Tencent's **iLink Bot open protocol**, the HTTP JSON API underneath the `Tencent/openclaw-weixin` OpenClaw plugin. It is not an Official Account, WeCom, or WeChat Customer Service, nor a reverse-engineered personal-account protocol: you scan a QR code once with the WeChat mobile app, and that single confirmation is exchanged for a long-lived `bot_token`.

Key takeaway: **this is outbound long polling, not callbacks.** It has the same shape as Slack Socket Mode / Feishu (Lark) persistent connections and needs no public endpoint, so it reuses the existing connection-supervision and backoff model in `imbot`.

## Protocol cheat sheet

Gateway: `https://ilinkai.weixin.qq.com`. After a successful login the server returns an **account-specific `baseurl`**; every subsequent request must go there, not to the login gateway.

| Purpose | Method and path |
|---|---|
| Fetch QR code | `GET /ilink/bot/get_bot_qrcode?bot_type=3` |
| Poll scan status (**long poll, ~30s**) | `GET /ilink/bot/get_qrcode_status?qrcode=<challenge>` |
| Receive messages (long poll, ~35s) | `POST /ilink/bot/getupdates` |
| Send message | `POST /ilink/bot/sendmessage` |
| Fetch typing ticket | `POST /ilink/bot/getconfig` |
| Typing indicator | `POST /ilink/bot/sendtyping` |
| Online/offline notification | `POST /ilink/bot/msg/notifystart` \| `.../notifystop` |
| Media upload presign | `POST /ilink/bot/getuploadurl` |

Request headers: `Authorization: Bearer <bot_token>`, `AuthorizationType: ilink_bot_token`, `iLink-App-Id: bot`, `iLink-App-ClientVersion` (`major<<16|minor<<8|patch`), `X-WECHAT-UIN` (a random uint32 per request → decimal string → base64; replay protection, must not be cached).

Pitfalls (the last few were found the hard way, in testing):

- **`context_token` must be echoed back.** It comes from the inbound message being replied to. Without it the gateway still reports success, but the message never reaches the user, so `Client.SendItem` rejects an empty value outright: an error beats silent loss.
- **There is no "edit a sent message".** Every outbound message is `message_state: FINISH`. The protocol's substitute is the two item types `TOOL_CALL_START(11)` / `TOOL_CALL_RESULT(12)` plus a message-level `run_id`; the WeChat client collapses entries sharing a `run_id` into one progress block.
- **`errcode: -14` means the session is invalid.** Retrying is pointless; the user must scan again.
- **`get_qrcode_status` is a long poll.** The gateway holds the request for ~30s before answering `{"ret":0,"status":"wait"}`. A client timeout shorter than that window turns every poll into a failure (surfacing in the browser as 502s). The timeout must cover the window plus transport slack, and **the timeout itself must be read as "still waiting", not as an error**.
- **`qrcode_img_content` is a URL, not an image** (e.g. `https://liteapp.weixin.qq.com/q/xxxx?qrcode=…&bot_type=3`). The field name is misleading: it is **the content to encode into the QR code**, not base64 image data. The server renders the QR code to PNG with `skip2/go-qrcode` and hands that to the frontend.
- **`getconfig` requires `ilink_user_id`**, which does not exist until some user has messaged the bot. It looks like the cheapest authenticated read, but calling it with an empty id fails a perfectly healthy pairing with `ret=-2 ilink_user_id required`. "Test connection" therefore uses `notifystart` instead: it needs only the token and is the very call the connector makes on startup, so passing it validates the path the real connection depends on.
- **There are 8 states**, not the handful you'd assume: `wait` `scaned` (the gateway really does drop an n) `confirmed` `expired` `scaned_but_redirect` `binded_redirect` `need_verifycode` `verify_code_blocked`. The two redirect states mean **keep polling**; the two verifycode states mean DayMug cannot act on the user's behalf and the user must resolve it on their phone.

## Code layout

```
backend/internal/imbot/wechat.go            Connector: long-poll loop, message normalization, /new routing
backend/internal/imbot/wechat_outbound.go   Responder: no-edit fallback + typing heartbeat
backend/internal/handler/bot_wechat.go      The two HTTP endpoints for QR pairing

backend/internal/imbot/wechat/     Protocol package (no imbot dependency, verifiable on its own)
├── types.go        Wire format: constants, WeixinMessage, item types, request/response envelopes
├── transport.go    HTTP transport: headers, tiered timeouts, bot_agent sanitizing, response size cap
├── login.go        QR login: fetch code, poll, exchange for Credentials{Token, BaseURL}
├── api.go          getUpdates / sendMessage / getConfig / sendTyping / notify*
├── poller.go       Long-poll loop: cursor, backoff, session invalidation; account load/save
├── crypto.go       AES-128-ECB + PKCS#7 (only here; see "Media upload and download")
├── media.go        CDN upload/download and item <-> media reference parsing
└── thread.go       /new command parsing and peer->thread mapping
```

## `/new`: WeChat has no threads, so users draw the boundary

A person's WeChat messages are one flat stream; the platform gives no "change of topic" signal. Without one, a conversation grows forever and every reply carries the entire history.

So `/new` is that missing signal:

- `/new`: switch to a fresh thread id, reply with a confirmation, produce no prompt.
- `/new <text>`: switch to a fresh thread, reply with the same confirmation, and use `<text>` as the first message of the new conversation.
- Any other `/xxx`: **passed through unchanged**. Agent CLIs have their own slash commands and we must not swallow them.
- Paths like `/home/foo/bar.go` are not mistaken for commands (a command name must start with a letter: `[A-Za-z][A-Za-z0-9_-]*`).

Thread ids look like `<unixMilli>-<4-byte hex>`: time-ordered for easier debugging, with a random suffix so two resets in the same millisecond don't collide. **An old id is never reused after a reset**; otherwise the new topic would resume the old conversation's CLI session.

### The confirmation goes through `Message.Ack`, not sent directly by the connector

WeChat has no thread UI, so "started a new conversation" and "quietly continued the old context" look identical on the phone. `/new <text>` is worse: even if the agent answers, the user can't tell whether `/new` was stripped or fed in as part of the message. So both forms reply with the same confirmation.

The confirmation text rides on `imbot.Message.Ack` and is sent by the **bridge**, not replied on the spot by the connector:

- **It must pass the same channel-rule gate.** If the connector replied directly, it would tell someone excluded by `allowed_user_ids` that "the bot is here, and it's listening to you".
- **Empty messages must still reach the bridge.** Dropping messages with "no body and no attachment" in the connector would leave a bare `/new` with no reply at all, so a message carrying `Ack` is delivered even with an empty body; after sending the confirmation, the bridge stops there if body and attachments are both empty, rather than taking an account slot for an empty prompt.

The protocol package provides an on-disk `FileThreadStore` (survives restarts), but **the connector currently uses an in-memory map**: it has no handle to DayMug's state directory, and the `platforms` registry factory can only pass in `BotConfig`. The cost is that boundaries drawn by `/new` are lost on server restart, and that peer falls back to its pre-restart conversation. Restarts are infrequent, so this stays for now; a real fix means passing a state directory to the factory.

## Capturing raw protocol traffic: `DAYMUG_WECHAT_WIRE_DEBUG=1`

The protocol is reverse-engineered with no vendor docs, so **the bytes the real gateway sends are the only source of truth**. And `json.Unmarshal` **silently drops** fields the struct doesn't declare, so reading the Go types never reveals what's missing. This switch logs the raw body before decoding:

```bash
systemctl --user edit daymug     # or add it directly to the unit
# Environment="DAYMUG_WECHAT_WIRE_DEBUG=1"
systemctl --user restart daymug
journalctl --user -u daymug -f | grep "wechat wire"
```

Each request logs one `wechat wire request <endpoint>` line and each response one `wechat wire response <endpoint>` line, truncated to 16 KiB each. To see what outbound images look like, check `request ilink/bot/sendmessage`; to see what the real gateway delivers, check `response ilink/bot/getupdates`. Comparing the two shows which fields the outbound side failed to fill in.

⚠️ **The traffic contains AES media keys and context tokens**, so it's off by default. Turn it off when you're done debugging; don't leave it in the persistent config.

Note that `daymug.service` is a **user-level unit** (`systemctl --user`), and its logs live in the user journal. `journalctl -u daymug` (without `--user`) finds nothing, which looks like "the service has no logs" but is really querying the wrong manager.

## Not yet verified

The protocol was reverse-engineered from the plugin source; Tencent publishes no API docs. The following **can only be tested with a real WeChat account**:

- Whether any WeChat account can scan `bot_type=3`, or whether there is an allowlist or qualification requirement.
- What the rate limits are (neither the plugin source nor its README says).
- Group chats: `WeixinMessage` has `group_id` / `session_id` fields, but the plugin doesn't handle group messages at all, only 1:1 DMs. If group messages are never delivered, `*` and `require_mention` in channel rules are no-ops on WeChat, and only `dm` matters.
- Whether `session_id` is reliably delivered and when it changes; this decides whether it could replace `/new` for thread splitting.

## Media upload and download

Inbound images / files / video / voice all become `imbot.Attachment`, with the download closure kept inside the connector: the bot token and AES key never leak, and the bridge, database, and browser only see decrypted bytes. Outbound goes through `imbot.ArtifactResponder`: files declared via `DAYMUG_ARTIFACT` are encrypted, uploaded to the CDN, then sent as separate messages; `image/*` uses an IMAGE item (displayed inline), everything else a FILE item.

The CDN uses **AES-128-ECB + PKCS#7**. ECB leaks block-level structure and no new code should choose it; it's used here purely because the gateway accepts nothing else, so the implementation is confined to the single file `wechat/crypto.go`. The Go standard library provides no ECB, so the block loop is hand-written.

The upload handshake must declare the plaintext MD5, plaintext length, and **ciphertext length** before the first byte is sent, so the whole file has to be in memory first (capped at 25 MiB, matching the bridge's per-file inbound limit). The ciphertext length is predictable precisely because PKCS#7 always appends padding. After a successful upload, the download parameter comes from the CDN response header **`x-encrypted-param`**; without it the media can never be referenced, so it counts as a failure even if the CDN returned 200. CDN 4xx responses are not retried: they mean the presigned parameters are wrong, and resending just wastes effort.

### `aes_key` is "base64 of hex text", not "base64 of key bytes"

The same `media.aes_key` field carries two encodings in the protocol, depending on who produced it:

| Case | Encoding | base64 decodes to |
|---|---|---|
| Inbound images (gateway-produced) | base64(raw 16 bytes) | 16 bytes |
| Inbound files / voice / video | base64(32-char hex text) | 32 bytes ASCII |
| **All outbound media** | **base64(32-char hex text)** | 32 bytes ASCII |

The outbound row is mandatory: `send.ts` in Tencent's own plugin `@tencent-weixin/openclaw-weixin` writes `Buffer.from(uploaded.aeskey).toString("base64")` for IMAGE / FILE / VIDEO alike, and `uploaded.aeskey` is already a hex string. `Buffer.from` without an encoding argument assumes utf8, so what goes into base64 is those 32 ASCII characters, not the 16-byte key. Its download-side `parseAesKey` documents both encodings side by side in a comment, the only authoritative statement of this.

**This bit us twice.** The first time, outbound only filled the media block with base64(raw bytes): the upload succeeded, `SendItem` returned `ret=0`, and the message was delivered, but the client got no usable key, so **images rendered blank** and files likewise wouldn't open. The second time it was misdiagnosed as "the key belongs in the item-level `aeskey`" and a hex field was added there. But the reference implementation never sends that field outbound; it only appears on **inbound** images, so images were still blank.

So now: outbound fills only `media.aes_key`, encoded as base64(hex text), and the IMAGE item does **not** carry an item-level `aeskey`; inbound, `decodeMediaKey` accepts both encodings. The latter isn't a nicety: when base64 decodes to 32 bytes, `crypto/aes` happily accepts it as a valid AES-256 key and silently decrypts garbage, so the length must be checked explicitly.

The whole chain produced zero errors and not a single log line: `PostAttachments` goes through `respondBestEffort`, which only logs when an error is returned, and there was no error. **When debugging this kind of issue, don't read your own code; read the traffic the reference implementation sends.**

## Not yet implemented: tool-call progress

The protocol has two item types, `TOOL_CALL_START(11)` / `TOOL_CALL_RESULT(12)`, and the WeChat client collapses entries sharing a `run_id` into a native progress block. **This is deliberately not done**, because doing it would require fabricating information:

IM progress only consumes `agent.KindToolUseStart` (`imProgress.observe` in `backend/internal/service/imbridge/imbot_run.go`), and **there is no reliable tool-end event**: the event alphabet does have `KindToolResult`, but `claudecli` emits it on the assistant's tool_use block (carrying the full input, not marking execution end), and adapters disagree on its semantics. Producing paired START/RESULT would require inference like "the previous tool ended when the next one starts", which is wrong for agents that call tools in parallel; sending only START without RESULT risks leaving an entry spinning forever in the WeChat client.

Both approaches gamble on a client whose **rendering behavior hasn't been verified on a real device**. It's cheaper to wait until a real device shows how the client renders an unpaired START than to guess now. Current progress feedback is the native typing indicator (`sendtyping`) plus queue-position messages, both of which are real; see the previous section.

## Two structural differences when wiring into imbot

Neither was solved by "adding a capability flag"; both use smaller techniques:

**1. No edit: express it through the missing primitive itself.** `progressResponder` (`backend/internal/imbot/responder.go`) is built entirely around "edit one message in place". The WeChat connector leaves the `edit` closure **nil**, and the skeleton takes the fallback path accordingly: `Start` / `Update` only refresh the typing indicator without sending messages, and `Complete` sends everything as new messages, the first chunk via `start` and the rest via `post`.

Why not add a `Capabilities.SupportsMessageEdit` boolean: a capability flag can drift from the implementation (claims support but edit isn't wired), while a nil closure can't; callers check whether the primitive itself exists. It also guarantees that existing Slack / Feishu (Lark) / Telegram behavior is unchanged down to the byte.

Why no "Thinking…" placeholder: WeChat can never edit it, so the user would end up with a dead placeholder message sitting above the real answer. The typing indicator (`sendtyping`) is the only in-progress signal this platform offers.

### The limit of silence: `Notice`

The fallback path's "say nothing until the answer is ready" only holds for **intermediate states the answer will replace**. Three kinds of text don't fit that category; they go through `imbot.Notify` → `progressResponder.Notice`:

| Text | Why it can't stay silent |
|---|---|
| Run failure notice | A failure has no answer to replace it. Going through `Update` means the user only ever sees the typing indicator spin, then nothing |
| Cancelled by a newer message | Same as above; the old turn will never produce anything |
| Queue position | "N ahead of you" is the only signal that distinguishes "waiting behind others" from "ignored me entirely" |

On platforms that can edit, `Notice` is just `Update` (replacing the progress message), so Slack / Feishu (Lark) / Telegram behavior is byte-for-byte unchanged; without an edit primitive it `post`s a new message. It's an **optional interface** (`imbot.NoticeResponder`); test doubles that implement only the four core methods fall back to `Update` automatically.

The cost is that queue positions become real messages on WeChat: `notifyIMQueuePositions` only announces the initial position and actual decreases, so the message count is bounded by queue depth and won't flood.

### Typing heartbeat and teardown

Refreshing the typing indicator only by piggybacking on agent events (`imProgress.render`, which also dedupes milestones within the same phase) is not enough: a 60s tool call produces zero events, so the indicator would go dark midway, exactly when the user most needs to know it's "still running". So `progressResponder` starts its own heartbeat goroutine with `activityInterval` (5s for WeChat; see `wechatTypingHeartbeat`), independent of the event stream.

How long one `sendtyping` stays lit on the gateway is undocumented and **hasn't been measured against the real gateway**, so it uses the shortest window among mainstream platforms (Telegram's 5s) rather than guessing longer.

Teardown relies on `imbot.TurnCloser`: `IMBridge.HandleMessage` `defer`s one `CloseResponder` on **every** exit, including rule rejections and failures, none of which reach `Complete`. `endActivity` is idempotent, so "`Complete` clears once + `Close` clears again" sends only one `TypingCancel`. Without this step, a failed turn would leave the WeChat chat stuck on "typing…" forever.

**2. No history backfill: follow Telegram's precedent and leave it nil.** `connector_contract_test.go` only covers connectors that can read history (Slack / Feishu (Lark)). Telegram, whose Bot API has no history read, explicitly leaves `LoadThreadMessages` nil and pins that with an assertion. WeChat is the same: the iLink protocol has no history-fetching API, and faking a backfill would be fabrication. `wechat_test.go` has the same assertion.

## Credentials and pairing

WeChat credentials aren't typed in; they're obtained by scanning, and they come in **two halves**: the token and the account-specific gateway address. Both go into existing generic slots in the `bots` table (`bot_token` holds the token, `bot_app_id` holds the gateway), so adding this platform **required no schema change and no migration** (the same idea as Telegram reusing `bot_token`).

Pairing uses two endpoints, both under `users/:id/bots`, reusing the same Agent ownership check:

| Endpoint | Purpose |
|---|---|
| `POST /api/users/:id/bots/wechat-pairing` | Fetch the QR code; returns `{challenge, qr_image}` (base64 PNG) |
| `GET /api/users/:id/bots/wechat-pairing?challenge=…` | **Single** query; returns `pending` / `confirmed` / `expired` |

The polling loop lives in the browser, not the server: closing the dialog ends pairing without leaving a hanging request on the server. Once confirmed, credentials are filled back into the form and the user clicks Save, the same path Slack / Feishu (Lark) credentials take into the same form. This also makes "pair while creating a bot" and "re-pair an existing bot" a single code path.

Any unknown status from the gateway is treated as `pending`: an unseen status more likely means a slow scan, and treating it as expired would force the user to rescan for nothing.
