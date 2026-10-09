// Split out of apiTypes.ts (kept as a re-export barrel) so each domain's
// types live next to their concerns. Import via "@/composables/apiTypes".

export type ThinkLevel = "" | "low" | "medium" | "high" | "max";

// One conversation that wants its owner back (store.ConversationAttention).
export interface ConversationAttentionRow {
  conversation_id: string;
  agent_id: string;
  title: string;
  state: "done" | "error" | "waiting";
  at: string;
}

export interface Conversation {
  id: string;
  user_id: string;
  title: string;
  // Provider names the underlying agent CLI this conversation talks to
  // ("claude" / "codex"). Locked once the first message has been sent.
  // Empty string for legacy rows persisted before this column existed —
  // the UI treats that as "use the server's default provider".
  provider: string;
  // Model is the CLI model id the backend passes via --model on every run.
  // Empty falls back to the CLI's own default.
  model: string;
  // Per-conversation reasoning effort. Empty means the request omits an effort
  // override and lets the provider/model use its own default.
  think_level?: ThinkLevel;
  // Account pins the conversation to one of the user's allowed accounts within
  // its provider type. Empty = the user's default account for that type.
  account_name: string;
  work_dir: string;
  session_id: string;
  notifications_enabled: boolean;
  // Pinned conversations float to the top of the sidebar list, sorted
  // among themselves by updated_at DESC. Toggled from the per-row pin
  // button next to the trash icon.
  pinned: boolean;
  // Manual sort position within the pinned block (ascending; smaller = higher).
  // Only meaningful while pinned; set by drag-to-reorder.
  pin_order: number;
  // True once the owner has created a public read-only share link. The token
  // itself is intentionally not exposed on conversation rows.
  shared?: boolean;
  source_type?: string;
  cron_job_id?: string;
  created_at: string;
  updated_at: string;
}

export interface ChatMessage {
  id: string;
  conversation_id: string;
  role: string;
  content: unknown;
  created_at: string;
  // Non-empty for user prompts that haven't yet finished the dispatcher
  // pipeline. "pending" = queued waiting for a worker; "processing" =
  // claude run in flight. Empty for completed history rows. The chat
  // surface routes pending rows to the staging area instead of inlining
  // them so a queued prompt never appears between in-flight tool calls.
  queue_status?: string;
  // Per-turn JSON metadata: assistant rows carry usage/model details and
  // user rows may carry uploaded-image previews. It is restored directly
  // from message history without a localStorage shadow.
  metadata?: {
    model?: string;
    // See AssistantMetadata.origin — marks a turn the provider started
    // itself rather than one the user asked for.
    origin?: string;
    usage?: Record<string, unknown>;
    attachments?: Array<{ name: string; mime: string; path: string; url: string }>;
    sender?: { platform: string; id?: string; name?: string };
  };
}

export interface SharedConversationPayload {
  conversation: Conversation;
  messages: ChatMessage[];
}

// UploadResult is what POST /api/uploads returns. The two paths are not
// interchangeable: `ref` is the absolute path the composer pastes into the
// outgoing message, literally true from the agent's perspective wherever its
// cwd happens to be, while `path` addresses the same file relative to the
// owner's home for the file-read API. The file lives in the agent's shared
// DayMug scope under that home, not in the conversation's project directory.
//
// `ref` stays in the stored message because it is part of the prompt the agent
// received, but ChatMessageItem strips it back out when rendering the bubble —
// the attachment chips below already name the same files.
export interface UploadResult {
  ref: string;
  path: string;
  name: string;
  size: number;
  mime: string;
  // Authenticated inline URL for rendering an uploaded image in the chat
  // transcript.
  url: string;
}
