// useChat is the facade over the chat modules in ./chat/. It owns the single
// WebSocket client and the cross-module orchestration (conversation
// switching, sending, cancelling). The state it exposes lives in @/stores:
//   - chatMessageStore        — transcript rows, dedup set, history cursors
//   - chatStreamStore         — live stream / thinking / tool buffers
//   - chatQueueStore          — the staging-area queue + pool metrics
//   - chatContextStore        — context bar, rate limits
//   - activeConversationStore — active-conversation metadata + pub/sub
//   - agentActivityStore      — which agents/conversations are running or queued
// and the behaviour around it in:
//   - chat/messageState.ts      — REST mappers, history paging, scroll hook
//   - chat/contextTracking.ts   — convMeta persistence, clear-context
//   - chat/messageDispatcher.ts — the WS frame handlers
//   - chat/workDir.ts           — the work-dir hint + change round-trip
// Every useChat() caller shares the same store refs.
import { i18n } from "@/i18n";
import { loadConvMetaMap } from "@/lib/convMeta";

import { fetchConversation, fetchMessages } from "./useApi";
import { useWebSocket, type MessageAttachment } from "./useWebSocket";
import {
  bindConversationLookup,
  chatUserId,
  conversationAccount,
  conversationListEvent,
  conversationListResync,
  conversationProvider,
  conversationWorkDir,
  currentConversationId,
  currentModel,
  currentSubscriptionId,
  isWorkDirLocked,
  lookupConversation,
  modelInfoLine,
  recallAttachments,
  recallText,
  titleUpdate,
} from "@/stores/activeConversationStore";
import {
  activeAgentIds,
  activeConversationIds,
  backgroundConversationIds,
  queuedConversations,
  runningAgentIds,
  runningConversationIds,
  runningConversations,
} from "@/stores/agentActivityStore";
import {
  contextClearedAfterMessageId,
  contextPercent,
  contextUsage,
  lastUsage,
  rateLimits,
} from "@/stores/chatContextStore";
import {
  HISTORY_PAGE_SIZE,
  chatMessages,
  cursors,
  hasMoreHistory,
  isLoadingMoreHistory,
  knownMessageIds,
  lastPersistedMessageId,
  resultVersion,
  type ChatMessage,
} from "@/stores/chatMessageStore";
import {
  clearQueueMetrics,
  isStagedPromptStatus,
  nextPendingPromptSeq,
  pendingPrompts,
  PROMPT_QUEUE_STATUS_IM,
  queueAhead,
  queuePosition,
  queueRunning,
  startedPromptIds,
  type PendingPrompt,
} from "@/stores/chatQueueStore";
import {
  isThinking,
  isAnsweringUserQuestion,
  isTurnStatusKnown,
  pendingUserQuestion,
  streamingText,
} from "@/stores/chatStreamStore";
import { resetConversationDeliveryWatermark } from "@/stores/wsDeliveryStore";
import { hasAttachmentDraft } from "@/stores/chatAttachmentDraftStore";

import { CONTEXT_CLEARED_DIVIDER, clearContext, persistConvMeta } from "./chat/contextTracking";
import { createMessageDispatcher, resetTurnStreamBuffers } from "./chat/messageDispatcher";
import {
  loadMoreHistory,
  displayMessageContent,
  restMessageToChatMessage,
  scrollChat,
  setChatScrollFn,
  syncFromCursor,
  wireContentToString,
} from "./chat/messageState";
import {
  changeWorkDir,
  downgradeWorkDirHint,
  lockWorkDirAfterUpload,
  workDirHint,
} from "./chat/workDir";

export { bindConversationLookup };
export { CONTEXT_CLEARED_DIVIDER };
export type { RateLimitInfo } from "@/stores/chatContextStore";

const ws = useWebSocket();

// Set up message handler
ws.onMessage(createMessageDispatcher());

function sendInit(syncMissedMessages: boolean) {
  const convID = currentConversationId.value;
  if (!convID) return;
  ws.send({
    type: "init",
    conversation_id: convID,
    user_id: chatUserId.value,
    subscription_id: currentSubscriptionId.value,
    // Empty cursor falls back to the server's "send everything" path —
    // correct for a brand-new conversation, harmless for an established
    // one because the dedup set will skip what we've already rendered.
    last_message_id: cursors.lastSyncedMessageId || undefined,
  });
  if (syncMissedMessages) {
    // Belt-and-suspenders for physical reconnect / visibility resync: REST
    // heals any persisted rows missed while this socket was unavailable.
    // A normal conversation switch already loaded a fresh REST page, so
    // running this again would race that snapshot with an unnecessary cursor
    // request.
    void syncFromCursor();
  }
}

ws.onReconnect(() => sendInit(true));

function conversationMessageScope() {
  return {
    conversation_id: currentConversationId.value,
    subscription_id: currentSubscriptionId.value,
  };
}

let switchGeneration = 0;
let subscriptionSequence = 0;

function resetConversationSwitchState(id: string) {
  currentConversationId.value = id;
  chatMessages.value = [];
  resetTurnStreamBuffers();
  // Drop any thinking/queued state from the previous conversation; init_status
  // restores both the new conversation's turn state and the known-state flag.
  isThinking.value = false;
  isTurnStatusKnown.value = false;
  pendingUserQuestion.value = null;
  isAnsweringUserQuestion.value = false;
  knownMessageIds.clear();
  // Restore the last-known model + context usage from localStorage so the
  // header renders the right state immediately, before the WS events arrive.
  // Per-turn usage travels on each assistant row's metadata.usage from REST
  // and renders intrinsically under its bubble.
  const cachedMeta = loadConvMetaMap()[id];
  contextUsage.value = cachedMeta?.contextUsage ?? null;
  currentModel.value = cachedMeta?.model ?? "";
  conversationProvider.value = "";
  conversationAccount.value = "";
  modelInfoLine.value = cachedMeta?.modelInfo ?? "";
  contextClearedAfterMessageId.value = cachedMeta?.contextClearedAfterMessageId ?? "";
  conversationWorkDir.value = "";
  // Keep the workdir locked during the transition. Without this, a racy
  // "directory-changed" event from WorkspacePanel can overwrite the new
  // conversation's inherited work_dir before fetch completes.
  isWorkDirLocked.value = true;
  clearQueueMetrics();
  recallText.value = "";
  recallAttachments.value = [];
  pendingPrompts.value = [];
  startedPromptIds.clear();
  cursors.lastSyncedMessageId = "";
  cursors.oldestLoadedMessageId = "";
  resetConversationDeliveryWatermark();
  hasMoreHistory.value = false;
  isLoadingMoreHistory.value = false;
  // Drop the previous conversation's rate-limit frames; the next turn refills
  // them for the account the new conversation actually uses.
  rateLimits.value = {};
}

async function switchToConversation(id: string) {
  if (id === currentConversationId.value) return;

  const gen = ++switchGeneration;
  currentSubscriptionId.value = `sub-${++subscriptionSequence}`;
  resetConversationSwitchState(id);

  try {
    // Pull only the latest page on switch. A long conversation can be
    // megabytes of stream-json over the wire; loading everything makes the
    // user wait while old turns paint top-down before the scroll snaps to
    // the tail. ChatPage lazy-loads older pages when the user scrolls up.
    //
    // Conversation metadata (work_dir, title, …) usually lives in the
    // sidebar list cache already — useConversations.bindConversationLookup
    // wires a lookup that lets us skip the GET /api/conversations/:id
    // round-trip when the row is cached. Only fall back to the network
    // when the lookup misses (cross-agent navigation before the new
    // agent's list has loaded, or a direct deep-link into a conversation
    // we've never seen).
    const cachedConv = lookupConversation(id);
    const [msgs, conv] = await Promise.all([
      fetchMessages(id, { limit: HISTORY_PAGE_SIZE, beforeId: "" }),
      cachedConv ? Promise.resolve(cachedConv) : fetchConversation(id),
    ]);
    if (gen !== switchGeneration) return;

    conversationWorkDir.value = conv.work_dir;
    conversationProvider.value = conv.provider;
    conversationAccount.value = conv.account_name ?? "";
    // If the latest page is full, there's almost certainly older history
    // we haven't pulled yet. Treat the workdir as locked in that window
    // even if no user message appears in the loaded slice — the server
    // enforces the lock anyway, and unlocking eagerly would let a stray
    // workspace-nav event POST a changeWorkDir that bounces with 409.
    hasMoreHistory.value = msgs.length >= HISTORY_PAGE_SIZE;
    const hasUserMsg = msgs.some((m) => m.role === "user");
    isWorkDirLocked.value =
      hasUserMsg || hasMoreHistory.value || hasAttachmentDraft(currentConversationId.value);

    // Split queued prompts off into the staging area; everything else
    // becomes regular chat history. The dispatcher's `prompt_started`
    // will later promote them when a worker actually claims them.
    const inlineMsgs = msgs.filter(
      (m) => !(m.role === "user" && isStagedPromptStatus(m.queue_status)),
    );
    const pendingMsgs = msgs.filter(
      (m) => m.role === "user" && isStagedPromptStatus(m.queue_status),
    );
    chatMessages.value = inlineMsgs.map(restMessageToChatMessage);
    pendingPrompts.value = pendingMsgs.map((m) => ({
      id: m.id,
      content: wireContentToString(m.content),
      attachments: m.metadata?.attachments,
      sender: m.metadata?.sender,
      clientKey: `srv-${m.id}`,
      fromIM: m.queue_status === PROMPT_QUEUE_STATUS_IM,
    }));
    for (const m of msgs) {
      if (m.id) knownMessageIds.add(m.id);
    }
    if (msgs.length > 0) {
      cursors.lastSyncedMessageId = msgs[msgs.length - 1].id;
      cursors.oldestLoadedMessageId = msgs[0].id;
    }

    // Always surface the working directory at the top of the chat.
    // Wording depends on the lock state: while the workdir is still
    // changeable (no user messages yet) the hint includes the
    // "navigate in the workspace panel" instruction, otherwise it
    // collapses to just the path so the locked state isn't misleading.
    if (conv.work_dir) {
      chatMessages.value.unshift({
        role: "activity",
        content: workDirHint(conv.work_dir, isWorkDirLocked.value),
        activityType: "info",
      });
    }
    // Restore the model pill from convMeta so it survives a refresh
    // (system_init doesn't replay for an already-running claude session).
    // Sits right under the workdir hint at the top.
    if (modelInfoLine.value) {
      chatMessages.value.splice(conv.work_dir ? 1 : 0, 0, {
        role: "activity",
        content: modelInfoLine.value,
        activityType: "model",
      });
    }

    // Re-render the "context cleared" divider when the cached marker still
    // points at the current tail. Once a newer persisted message lands the
    // marker is stale — drop it so the inline trigger reappears naturally.
    if (contextClearedAfterMessageId.value) {
      if (lastPersistedMessageId() === contextClearedAfterMessageId.value) {
        chatMessages.value.push({
          role: "activity",
          content: CONTEXT_CLEARED_DIVIDER,
          activityType: "info",
        });
      } else {
        contextClearedAfterMessageId.value = "";
        persistConvMeta();
      }
    }

    // Immediate: switching into a conversation should always land the
    // reader at the latest message, even if their last position in the
    // previous one was scrolled up.
    scrollChat(true);
  } catch {
    // ignore
  }

  if (gen !== switchGeneration) return;
  if (ws.isConnected.value) {
    sendInit(false);
  } else {
    // connect() is idempotent while a handshake is already in progress. Its
    // onopen path calls sendInit from live state, so a failed first handshake
    // still joins the latest conversation on the eventual reconnect.
    ws.connect();
  }
}

function sendMessage(text: string, attachments?: MessageAttachment[], insert = false) {
  // Always send straight to the backend — no front-end queueing and no
  // waiting for `isThinking` to clear. Every prompt is persisted as
  // 'pending' first. Regular sends remain FIFO; the explicit Insert action
  // asks a capable backend to append it to the live task. Either way a browser
  // close cannot lose the message and refresh can rebuild state from history.
  isWorkDirLocked.value = true;
  // Now that the workdir is locked for this conversation, swap the
  // top-of-chat hint to its shortened form (drops the "navigate in the
  // workspace panel" call-to-action that no longer applies). The hint
  // already reflects any pre-send workdir change made via the workspace
  // panel — changeWorkDir() updates it in place — so no separate
  // "switched to" notice is needed.
  downgradeWorkDirHint();
  // The optimistic copy lives in pendingPrompts (the staging area), NOT
  // in chatMessages. We only promote it into history when the dispatcher
  // worker fires `prompt_started` — that way a queued message never
  // appears interleaved with the in-flight task's tool calls.
  const clientKey = `cli-${nextPendingPromptSeq()}-${Date.now()}`;
  pendingPrompts.value.push({
    content: displayMessageContent(text, undefined, attachments),
    ...(attachments?.length ? { attachments } : {}),
    clientKey,
  });
  scrollChat();
  const sent = ws.send({
    type: "input",
    content: text,
    ...conversationMessageScope(),
    ...(insert ? { steer: true } : {}),
    ...(attachments?.length ? { attachments } : {}),
  });
  if (!sent) {
    // The socket dropped between the composer's connected check and now, so
    // the server never saw this prompt. Leaving the optimistic row would show
    // a queued message that no worker will ever claim (and the reconnect's
    // init snapshot would silently drop it); auto-resending on reconnect could
    // run it twice if the prompt did land. Hand it back to the composer
    // instead, with a visible reason, and let the user resend.
    pendingPrompts.value = pendingPrompts.value.filter((p) => p.clientKey !== clientKey);
    recallAttachments.value = attachments ?? [];
    recallText.value = text;
    chatMessages.value.push({ role: "error", content: i18n.global.t("chat.sendFailedOffline") });
    scrollChat();
  }
}

function cancelMessage() {
  // Broad cancel: abort the currently-running claude run AND drop every
  // still-pending prompt for this conversation. The server replies with
  // cancel_ack carrying the dropped rows so we can clear the staging area.
  //
  // Recall the queued prompts into the editor *here* — client-side, from
  // the local staging area — rather than relying on cancel_ack. A cancel
  // should never silently discard messages the user lined up: they get
  // dropped back into the input so they can be re-edited and resent. We
  // read pendingPrompts before sending so the snapshot is the queue at
  // click time (the in-flight prompt was already promoted into history
  // and is not part of pendingPrompts, so it is not recalled). Doing this
  // locally also fixes the single-queued-message case, which the old
  // count-based cancel_ack heuristic dropped on the floor.
  // IM prompts are excluded: recalling one would drop a message the reader
  // sent from Slack or WeChat into this composer as if the web user had typed
  // it. Cancelling an IM turn discards nothing — the original is still in the
  // IM thread.
  const queuedPrompts = pendingPrompts.value.filter((p) => !p.fromIM);
  const queued = queuedPrompts.map((p) => p.content).filter((s) => s.length > 0);
  if (queued.length > 0) {
    recallAttachments.value = queuedPrompts.flatMap((p) => p.attachments ?? []);
    recallText.value = queued.join("\n\n");
  }
  ws.send({ type: "cancel", ...conversationMessageScope() });
  // Mark the in-flight user prompt (the most recent user-role row in
  // history) as cancelled so the bubble renders with the muted variant
  // instead of looking identical to a turn that completed normally.
  // Guarded on isThinking — the cancel button is hidden when no turn is
  // running, but a stale click shouldn't retroactively mark an old row.
  if (isThinking.value) {
    for (let i = chatMessages.value.length - 1; i >= 0; i -= 1) {
      const m = chatMessages.value[i];
      if (m.role === "user") {
        m.cancelled = true;
        break;
      }
    }
  }
}

function answerUserQuestion(requestId: string, answers: Record<string, string[]>) {
  if (
    !ws.isConnected.value ||
    !pendingUserQuestion.value ||
    pendingUserQuestion.value.request_id !== requestId ||
    isAnsweringUserQuestion.value
  ) {
    return;
  }
  isAnsweringUserQuestion.value = true;
  ws.send({
    type: "question_response",
    request_id: requestId,
    answers,
    ...conversationMessageScope(),
  });
}

// cancelPendingPrompt drops a single queued prompt by id/clientKey.
// Powers the staging area's per-message recall affordance: clicking
// the X on a queued bubble fires this so only that row is removed
// without affecting the in-flight job or sibling pending entries.
//
// The cancelled prompt's text is also stuffed back into the editor via
// recallText so the user can tweak it and resend without retyping —
// "recall" rather than "discard". (Earlier behaviour discarded the
// text on the assumption that an X click meant "throw it away", but
// the user-facing affordance reads as "edit this", so we recall.)
//
// Optimistic local removal happens immediately for snappy feedback;
// the server's cancel_ack reconciles state if anything raced (e.g. the
// worker claimed the prompt before our message landed, in which case
// the server-side cancel becomes a no-op and the prompt completes
// normally — its user_message echo populates history when ready).
//
// Accepts either the persisted DB id (preferred) or the clientKey we
// assigned at send time, for the brief window before input_ack lands
// and a row has no DB id to target.
function cancelPendingPrompt(idOrKey: string) {
  if (!idOrKey) return;
  const idx = pendingPrompts.value.findIndex(
    (p) => (p.id && p.id === idOrKey) || p.clientKey === idOrKey,
  );
  if (idx < 0) return;
  const target = pendingPrompts.value[idx];
  // An IM prompt cannot be recalled: its text was typed in Slack or WeChat, so
  // pulling it into this composer would invent a web message the reader never
  // sent, and the row is not in the dispatcher's pending set so the per-message
  // endpoint would not find it. Cancelling the turn is the real intent — and
  // leaving the card in place lets the server's own frames retire it, so the UI
  // never claims a cancel that did not land.
  if (target.fromIM) {
    cancelMessage();
    return;
  }
  pendingPrompts.value.splice(idx, 1);
  if (target.content) {
    recallAttachments.value = target.attachments ?? [];
    recallText.value = target.content;
  }
  if (target.id) {
    ws.send({
      type: "cancel",
      message_id: target.id,
      ...conversationMessageScope(),
    });
  }
}

function disconnectWs() {
  ws.disconnect();
}

export function useChat() {
  return {
    currentConversationId,
    chatMessages,
    streamingText,
    isThinking,
    isTurnStatusKnown,
    queueAhead,
    queuePosition,
    queueRunning,
    pendingPrompts,
    pendingUserQuestion,
    isAnsweringUserQuestion,
    contextUsage,
    contextPercent,
    rateLimits,
    resultVersion,
    chatUserId,
    titleUpdate,
    conversationListEvent,
    conversationListResync,
    activeAgentIds,
    activeConversationIds,
    runningAgentIds,
    runningConversationIds,
    runningConversations,
    backgroundConversationIds,
    queuedConversations,
    currentModel,
    conversationProvider,
    lastUsage,
    conversationWorkDir,
    isWorkDirLocked,
    recallText,
    recallAttachments,
    isConnected: ws.isConnected,
    reconnectAttempts: ws.reconnectAttempts,
    switchToConversation,
    sendMessage,
    cancelMessage,
    cancelPendingPrompt,
    answerUserQuestion,
    clearContext,
    changeWorkDir,
    lockWorkDirAfterUpload,
    disconnectWs,
    setChatScrollFn,
    loadMoreHistory,
    hasMoreHistory,
    isLoadingMoreHistory,
  };
}

export type { ChatMessage, PendingPrompt };
