# Desktop client (DayMug-Client)

[DayMug-Client](https://github.com/ifconfiger/DayMug-Client) is a Tauri app that shows the DayMug web UI in a native window and lets a conversation **run its file and shell tools on that computer**. The agent itself keeps running on the server — model credentials, session logs, billing and history never leave it; only the tools move.

In a plain browser nothing of this exists: no selector, no request, no extra tool or prompt. Everything below is gated on a marker only the desktop client injects.

## Using it

1. Install the client, enter the DayMug server URL, and log in as usual.
2. The page pairs the computer with your account silently (one device per client install; listed under `GET /api/devices`).
3. In a conversation, the composer shows a **run location** selector next to the model picker. Pick **Choose a folder on this computer…**: a native folder dialog opens; the conversation is now bound to that folder. Switch back to **Server** at any time.

A bound conversation's Read / Write / Edit / Glob / Grep / Bash calls execute on the computer, in the chosen folder, with the user's own permissions and no confirmation prompt (phase 1 runs every task automatically). Tool cards render exactly like the built-in ones.

The right-hand file panel follows the binding: it browses the chosen folder on the computer (folders, text and image previews, download) instead of the server workspace. It is read-only; switching back to **Server** restores the regular workspace panel.

Phase 1 supports the Claude family on the **Agent SDK** transport only. The CLI transports and the Codex family refuse a bound turn (`agent.ErrToolRouteUnsupported`) instead of running its tools on the server.

## How it works

```
DayMug-Client                                   DayMug server
 ├─ webview: https://<server>/  (no Tauri IPC)
 │    └─ window.__DAYMUG_CLIENT__ = {nonce,…}   ── POST /api/devices/pair {nonce}  (session cookie)
 └─ device runtime ── POST /api/devices/claim {nonce} → {device_id, token}
                  ══ WS /api/devices/connect (Bearer token) ══ DeviceHub
                        ▲ call {tool, cwd, args} / cancel           │
                        └──────── result (MCP CallToolResult) ──────┤
                                                                     │
          Agent SDK bridge ── HTTP MCP  http://127.0.0.1:<port>/api/devices/mcp  (Bearer per-conversation token)
```

- **Pairing.** The client generates a random nonce per launch and injects it into the page only when the page is on the configured server's origin. The logged-in page posts it to `/api/devices/pair`; the client, polling `/api/devices/claim`, receives a device token (stored hashed server-side, `0600` on the client). When the client is already connected under another account, the new credentials are pushed down its socket (`rebind`) instead. `GET /api/devices/current?nonce=` tells the page which device hosts it.
- **Tunnel.** The client dials out (works behind NAT) and announces its tools in a `hello` frame; the client owns the tool contract, so a client update can add a tool without a server release. Frames: `call {id, method, params}` / `cancel {id}` / `rebind {device_id, token}` server→client, `hello` / `result {id, result|error}` client→server. A call waits up to 15 s for a client that briefly dropped off.
- **Binding.** `conversation_devices (conversation_id, device_id, work_dir)` is its own table, so server-side conversations carry no device state. `PUT /api/conversations/:id/device` requires the device to be online: the folder is checked and canonicalised on the machine it lives on. Every tool call re-reads the binding and re-checks that the device belongs to the conversation's human owner, so a revoke or rebind applies mid-turn.
- **Agent wiring.** `service.DeviceHub.ApplyDeviceRoute` sets `RunRequest.ToolRoute`: the `daymug_local` HTTP MCP server, a `disallowedTools` list of every built-in that touches the host (Bash, Read, Write, Edit, MultiEdit, NotebookEdit, Glob, Grep, LS, Monitor, worktree tools…), and SDK `toolAliases` so a model-emitted built-in name (`Bash`) still lands on the device. A system-prompt section names the computer, OS and folder and tells the model to disregard the server's environment block. The web-artifact prompt is skipped: files are already on the user's computer.
- **MCP endpoint.** `/api/devices/mcp` is a minimal Streamable-HTTP MCP server (JSON responses, no SSE). It only answers direct loopback requests without proxy headers, and authenticates with an HMAC of the conversation id under a per-process secret — sent as a header so access logs never record it.
- **Instructions.** A bound turn's `ToolRoute.SettingSources` is `["user"]`, so the SDK no longer reads CLAUDE.md or `.claude/` settings from the server's working directory. Instead, the client's `project_instructions` call collects `~/.claude/CLAUDE.md` and, from the outermost ancestor down to the bound folder, `CLAUDE.md`, `.claude/CLAUDE.md`, `AGENTS.md` and `CLAUDE.local.md` (`@file` import lines inlined, 64 KiB per file, 192 KiB total). They are appended to the system prompt after the agent's DayMug-managed CLAUDE.md. The call waits at most 5 s; an offline or older client contributes nothing and the turn still runs.
- **File panel.** `GET /api/conversations/:id/device/files` and `…/files/read` relay `list_dir` / `read_file` to the client with `{root: work_dir, path}`. The server accepts only relative paths without `..`; the client canonicalises and confines the result to the bound folder, symlinks included. File bytes are served with passive content types (HTML and SVG as text) under a sandbox CSP, since they come from the user's machine.
- **Display.** `agent.DisplayToolName` strips the `mcp__daymug_local__` prefix, so the stored transcript and the UI show `Bash`, `Edit`, ….

## Security notes

- Binding a conversation to a computer gives the agent the user's full privileges there. Phase 1 has no approval step by design.
- The remote page has no Tauri IPC: everything it needs (folder dialog, directory checks) goes through the server over the authenticated socket. A compromised server can therefore drive every connected client — treat the server like any host you grant shell access to.
- Revoke a device with `DELETE /api/devices/:id`; its socket drops and its bindings are deleted.
