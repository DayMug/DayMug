# Frontend Architecture (Vue 3)

Stack: Vue 3 + TypeScript (strict mode, `noUnusedLocals` / `noUnusedParameters`) + Vite + Tailwind CSS + shadcn-vue. Package manager is pnpm; 2-space indent; ESLint + Prettier. Path alias `@` → `src/`.

## Layout

```
frontend/src/
├── pages/          Route views (Login / Chat / Preview / SharedConversation / SetupAdmin / Settings group)
├── components/     Reusable components; ui/ holds shadcn primitives
├── stores/         Global singleton state (its only home, see below)
├── composables/    Stateful logic (auth, chat WS, API client, theme, i18n…)
├── router/         Route table
├── lib/            Small pure-function utilities
├── directives/     Global directives (code-block copy, external links, mermaid)
└── i18n/           vue-i18n setup + locales/ (en.ts / zh.ts) + parity check
```

## Pages (`pages/`)

| File | Purpose |
|------|------|
| `ChatPage.vue` | Main chat UI: message stream + input box + workspace panel |
| `LoginPage.vue` / `SetupAdminPage.vue` | Login / create the first admin when the database is empty |
| `SharedConversationPage.vue` | Read-only conversation view for share links (`/share/conversation/:token`) |
| `PreviewPage.vue` | Shell-less file preview/edit page. It is only a thin route wrapper around `FileWorkbench`: it binds `?path` / `?edit` / `?line` and adds the right-hand workspace panel |
| `settings/SettingsLayout.vue` / `SettingsHub.vue` | Settings shell, plus the personal settings / Agent management home |
| `settings/AdminPanel.vue` | Standalone admin panel; single entry point for users, global ops, terminal, IM bots, and global usage |
| `settings/UserAdd.vue` / `UserEdit.vue` | Agent create / edit (four sections: Basics · Role & Identity · Integrations · Advanced); the Agent list and archived Agents live in `SettingsHub.vue` |
| `settings/AdminUsers.vue` | Human user management (bulk default model / permissions / sandbox mode / provider bindings) |
| `settings/SystemSettings.vue` | System settings (theme / time format / language) |
| `settings/AdvancedSettings.vue` | Advanced settings for the current user (one `VAR=VAL` env var per line) |
| `settings/CronSettings.vue` | Crontab scheduled-task management (Agent, five-field expression, IANA time zone, task body, enable/disable, and recent sessions) |

> Settings also has personal pages for account, notifications, usage stats (`UsageStats`), and About. The admin side additionally has `AdminGlobal` (self-upgrade / paused tasks / database / help docs), `AdminModels` (provider accounts + price table; account login and availability self-check live in `ProviderAccountStatus`, and login goes through the admin terminal overlay `AdminTerminalOverlay`), and `AdminTerminal`. IM bots are configured per Agent, not on an admin page.

## Components (`components/`)

- Layout: `SidebarPanel.vue` (48px avatar sidebar + connection indicator), `ConversationListPanel.vue` (pure view of the conversation list: create/delete/rename/pin; `ConversationListContainer.vue` pulls data from the store and handles row actions, with one instance mounted in `App.vue`'s docked column and one in the narrow-screen drawer, distinguished only by `drawer`), `WorkspacePanel.vue` (workspace file browser; each interaction area is split into `composables/useWorkspace*.ts`: clipboard and move conflicts in `useWorkspaceClipboard`, inline rename in `useWorkspaceInlineRename`, artifact tabs in `useArtifactPanel`).
- Chat: `chat/ChatInput.vue` (input box; the `controls` named slot shares a fixed row with Send; that row never wraps, and the model name truncates on narrow screens), `chat/ChatFooterControls.vue` (the model/context control group inserted into that row: the `ModelSelector.vue` combined account/model/think-level picker + the `chat/ContextUsageBar.vue` token progress bar + `chat/RateLimitBadge.vue`), `chat/ChatMessageItem.vue` (markdown / tool-call / thinking rendering; clicking an image attachment opens `ImageLightbox.vue` fullscreen: zoom via wheel/double-click/buttons, drag to pan, pinch to zoom, click the backdrop or press Esc to close. Non-image attachments remain download links opening in a new tab). The desktop chat header shows only the conversation title.
- Files: `FileWorkbench.vue` (the file UI itself), `FilePreview.vue` (highlight.js syntax highlighting), `HtmlPreview.vue` (renders HTML as a web page, see below), `FileContextMenu.vue`, `FilePropertiesDialog.vue`, `FileIcon.vue`.
- Spreadsheets / documents: `OfficePane.vue` + `lib/office/` (embedded CasualOffice editors, see below).
- Forms/menus: `UserForm.vue` (shared Agent config form), `UserContextMenu.vue`, `DirPicker.vue` (server directory picker).
- `ui/`: shadcn-vue primitives.
- Code editing: `FileTextEditor.vue` (CodeMirror 6; the component itself is lazy-loaded, and language packs are each dynamically imported by file extension; the extension/theme/language tables live in `lib/codeEditor.ts`), `FileQuickOpen.vue` (Cmd+P / Cmd+Shift+F command palette).

### File workbench and the right-hand artifact panel

`FileWorkbench.vue` is the single implementation of the file UI, shared by two mount points: the preview page (`/file/:userId`) and the
artifact panel of the chat page's right-hand `WorkspacePanel`. All logic that decides **how to display** a file lives inside it (edit-mode
detection, the special case routing Office files only to the embedded editor, dirty-buffer interception, on-disk change conflicts); the host
only owns **which file** and where that selection is stored. It is shared because once this logic is copied into two places, the copies
inevitably drift apart.

The chat page (desktop right column and the mobile Files tab) passes `file-open-target="panel"` to `WorkspacePanel`:
opened files become lightweight tabs in the panel and the chat area stays put. The tab list, the current tab, and the directory view /
workbench toggle are owned by `useArtifactPanel`; the tab strip is `workspace/WorkspaceArtifactBar.vue`. The workbench is mounted with
`storage-scope="artifacts"` behind the directory view (`v-show`), so switching back to directory browsing doesn't lose unsaved buffers;
switching or closing a tab goes through the workbench's `requestNavigateAway` / `requestClosePath` dirty-state guards.

### HTML: rendered as a web page by default

Double-clicking an `.html` file opens it as a **web page**, not source: `useWorkspacePreview.openPreview()` does not carry over the previous
file's `edit` state for HTML, and `FilePreview` hands it to `HtmlPreview.vue`. The "Web page / Source" toggle in the workbench header
offers the other two views, and the `Edit` button still opens the code editor. Moving to the next file returns to the web page view.

`HtmlPreview` points the iframe at `files/preview/<path>` (the URL path is the workspace path), so relative references in the page
resolve to the files next to it on disk, including subdirectories and wasm. The sandbox includes `allow-same-origin`: without it the page
cannot access localStorage or any fetch-style loading (including `instantiateStreaming`), which is the trade-off it buys;
`allow-top-navigation` is still withheld, so the previewed page cannot touch the surrounding workbench.

Refresh-on-change runs on two tracks: the `.html` file itself is handled by the workbench's existing watch (passed down as `reloadToken`), while resources the page
**actually loaded** are subscribed to by `HtmlPreview` itself. It reads URLs back from the iframe's resource timeline (`PerformanceObserver`,
so late-loaded wasm / chunks count too), maps them to workspace paths, and caps at 16 entries (the server's per-subscription limit).
One rebuild changes several files at once, so refreshes are debounced by 120ms and coalesced into one.

### Spreadsheets and documents: embedded CasualOffice editors

`.xlsx` / `.xlsm` / `.csv` / `.tsv` / `.xls` / `.ods` and `.docx` are not parsed by the frontend itself; they are handed to
two off-the-shelf editors loaded in iframes (`@casualoffice/sheets`, `@casualoffice/docs`). Division of labor:

| Location | What it does |
|------|--------|
| `scripts/sync-casual-office.mjs` | At build time, copies the embed artifacts from node_modules into `public/casual-office/<package>/<version>/`, writing only `.gz`; generates its own `embed.html`, self-hosts fonts, and rewrites the upstream bundle for Chinese localization. Stamp-cached, so repeat runs return instantly |
| `src/lib/office/embed.ts` | Single source of truth for versions and URLs; read-only uses `viewMode=preview`; the language is baked into the bundle at build time, so a different file is picked per locale |
| `src/lib/office/host.ts` | Host bridge: answers `load.request` by reading the file and writes bytes back; converts csv both ways (`csvbridge.ts`); confirms before a lossy save |
| `src/lib/office/files.ts` | Which extension maps to which editor, and which are view-only |
| `handler/routes.go` | Serves `.gz` under the `casual-office/` prefix based on `Accept-Encoding`, with one-year immutable caching |

Several constraints **found by testing**; read the header comment of `host.ts` before changing this area:

- The sheets embed never sends `save.request`; its `requestSave` is defined but never called. Saving can only go through
  `globalThis.__casualEmbedApi.exportXlsx()` inside the iframe (reachable only because it is same-origin).
- That Blob belongs to the iframe's realm, so `instanceof ArrayBuffer` misjudges it; it must be copied into the host realm,
  otherwise JSZip throws on the csv path.
- Answer the handshake **only once**: the embed re-sends `casual.ready` when it receives `casual.hello`, so replying every time
  causes an infinite postMessage ping-pong.
- Its `dirtyChange` can't be read reliably from outside (loading itself reports dirty, editing one cell flaps true/false for a long run, and it often
  settles on false). So "are there unsaved changes" is determined by the host listening to input events inside the iframe, and is **used only
  to decide whether to show a confirmation**: spreadsheets are never written back implicitly.
- The upstream parser doesn't accept `.xls` / `.ods` (it only takes OOXML zip), so they are converted to xlsx via SheetJS and opened **read-only**.

For on-disk change sync see `composables/useFileWatch.ts` and `files/watch` in the [API reference](api-reference.md).
In edit mode with unsaved local changes, it **does not overwrite automatically**; it shows a banner letting the user choose
"Reload / Keep my changes". A silent reload would swallow what the user is writing, which is the one thing this feature must never
get wrong.

### Editor buffer model

`FileTextEditor` owns **one** CodeMirror `EditorView` and **one `EditorState` per file** (`path → {state, etag, savedDoc, scroll}`).
Switching tabs uses `view.setState()` + `scrollSnapshot()` replay: the document, selection, and undo history all live in the state, so they survive round trips across tabs;
none of that would survive the mount/unmount cycle of "one editor instance per tab".

- Dirty state is a content comparison (`Text.eq`) between the current document and `savedDoc` from the last load/save, not a keystroke flag, so undoing back to the save point clears the dirty mark on its own.
- For the mounted buffer, `view.state` is authoritative; the state in `buffers` is written back only when switching away. Theme/wrapping/font size live in compartments, and a background buffer gets a reconfigure when it is switched back in. Language packs attach asynchronously after the buffer is already editable; if loading fails, it stays plain text.
- Closing a tab just deletes the map entry (state is an immutable value, nothing to dispose); when closing the currently visible file, first give the view an empty state, otherwise the view keeps holding on to the old state.
- Formatting only applies to `.json` (`JSON.stringify` with two-space indent, one transaction, one undo step).
- The tab list is owned by `FileWorkbench` (it owns the model behind the buffers), so closing a tab is done half on each side: the workbench splices, the editor calls `closeFile()`.
- The tab set is stored under a **single** sessionStorage key (`daymug.editor.tabs.v1`, `composables/editorTabStorage.ts`), slotted by `<scope>:<userId>`, with a 20-entry / 14-day LRU. Keeping it in sessionStorage is deliberate: restoring tabs across browser restarts mostly resurrects paths the agent has long since moved. Restore must happen before anything is pushed into `openPaths`, otherwise the save watcher first overwrites the stored set with the current single file.

## Global state (`stores/`)

The project **does not use Pinia/Vuex**: a store is a plain module holding `ref`s at module scope, and consumers `import` exactly the refs they need. `stores/` is the **only home** for global singleton state.

| File | Holds |
|------|------|
| `activeConversationStore.ts` | The current conversation's id / subscription / model / provider / work_dir, plus two one-way publish channels, `titleUpdate` and `conversationListEvent` |
| `chatMessageStore.ts` | The rendered message array, the cross-source dedup set, and the history pagination cursor |
| `chatStreamStore.ts` | Streaming text, thinking flag, tool/sub-agent buffers |
| `chatQueueStore.ts` | Staged pending prompts and account-pool queue metrics |
| `chatContextStore.ts` | Context usage, rate limit, derived `contextPercent` / `lastUsage` |
| `agentActivityStore.ts` | Which agents / conversations are running or queued |
| `conversationListStore.ts` | Conversation list for the sidebar and the mobile Agents page, per-agent cache, panel open/closed preference |
| `chatAttachmentDraftStore.ts` | Per-conversation staging of uploaded-but-unsent attachments (the composer gets reused / unmounted, so it can't be the sole owner) |
| `conversationAttentionStore.ts` | Sessions that "need a revisit" (completed / awaiting answer / failed), mirroring `GET /api/conversation-attention` |
| `wsDeliveryStore.ts` | `seq` watermarks for the two event streams on the same WS, and the in-flight gap-refetch lock `deliveryResync` |
| `userStore.ts` / `authStore.ts` | Agent roster / current logged-in identity |
| `appChromeStore.ts` | Chrome shared by the app shell: help / app marketplace dialog toggles, admin upgrade red dot, share-notice toast |

Conventions (also written at the top of `stores/index.ts`):

1. A store holds only the state itself, plus invariant-maintenance functions that are meaningless apart from that state (e.g. `rememberMessageId` updates the dedup set and the cursor together). Network requests, formatting, and DOM logic stay in the composable that uses them.
2. Each store exports `reset<Name>Store()`, and `stores/index.ts` aggregates them into `resetChatStores()` / `resetAllStores()`. Tests use these for isolation instead of clearing refs one by one, so adding a new ref can't silently leak state between test cases.
3. Injected wiring (`bindConversationLookup`, `setChatScrollFn`, and the like) is installed once at module load and **is not state**; reset leaves it alone, otherwise after a reset the app would be "unwired" rather than "empty".
4. Composables outside `stores/` **must not hold shared mutable state at module scope**. Exempt are module-level constants, memo tables, server-payload caches that "change only once per deploy" (`useServerInfo` / `useModelRegistry` / `useHelpDoc`), and pure browser preferences (`useTheme` / `useTimeFormat`): they are exposed only through their own composable's API, and no second module reads or writes them directly. **Generation tokens** used for async races (`useChat`'s `switchGeneration` / `subscriptionSequence`, `useConversations`'s `activeListRun`) are exempt too: they only increase and are only compared for equality ("is this still the latest run"), so not resetting them is actually safer; after a reset an old request's generation could collide with a new one's. Conversely, flags that **gate behavior** (such as the WS gap-refetch in-flight lock `deliveryResync`) must live in a store and be cleared on reset, otherwise one unfinished request makes later tests / sessions silently skip the refetch.

## Composables (`composables/`)

Stateful logic lives here; the state itself lives in `stores/`, and composables handle fetching, orchestration, and side effects.

| File | Responsibility |
|------|------|
| `useApi.ts` / `useFileApi.ts` | Aggregated REST API export (split by domain into `api*.ts`; add new endpoints to the matching domain file) / file management API |
| `appWiring.ts` | `installAppWiring()`: called explicitly once from `main.ts`, connects the stores' publish channels to the app shell (title / conversation-list events / WS identity); these watchers are not registered as an import side effect. Pages and components import the `useChat` / `useUsers` / `useConversations` / `useAuth` / `useTheme` they need directly; there is no aggregate entry point |
| `useWebSocket.ts` | Chat WebSocket wrapper: auto-reconnect at a fixed 3 seconds with unlimited retries (flat parameters from `lib/backoff.ts`); frame types and type guards live in `lib/wsProtocol.ts` |
| `useChat.ts` + `chat/` | Chat orchestration facade: WS event dispatch, REST mapping; state lives in `stores/chat*` |
| `useConversations.ts` / `useUsers.ts` | Conversation / user CRUD and selection; list state lives in `stores/conversationListStore.ts` / `userStore.ts` |
| `useModelRegistry.ts` | Caches `/api/models` and exposes per-provider `Capabilities` (e.g. whether a mid-turn send can be inserted) |
| `useMarkdown.ts` | Markdown rendering (markdown-it) |
| `useTheme.ts` / `useLocale.ts` | Theme (initial value from the system `prefers-color-scheme`, manual dark/light toggle, not persisted) / language |

## Communication and proxying

- **REST**: `/api/*`, CRUD.
- **WebSocket**: `/api/terminal` carries real-time streaming conversations, with multi-client broadcast, cancellation, and queue status; `/api/users/:id/files/watch` pushes file changes; the admin terminal uses `/api/admin/terminal/ws`.
- **Dev proxy**: `vite.config.ts` proxies `/api/*` (including WS upgrades) to the backend at `127.0.0.1:8090` (what `init` writes; override with `DAYMUG_DEV_BACKEND`).
- **Production**: `pnpm build` outputs to `frontend/dist/`, which the backend embeds into the static binary via the `embed_frontend` tag.

## Verification pipeline (`frontend/`)

```bash
pnpm build   # vue-tsc type check + production build
pnpm test    # Vitest (tests sit next to the code as Foo.test.ts)
pnpm lint    # ESLint --fix
pnpm api-contract:check  # TS interfaces vs the Go contract snapshot
```

i18n coverage is enforced by a pre-commit hook; see [i18n](../development/i18n.md).
