// src/stores is the single home for application-wide singleton state. The app
// deliberately does not use Pinia; a store here is a plain module that owns
// `ref`s at module scope, which is exactly what Vue reactivity needs for
// shared state and what every consumer already imports directly.
//
// Conventions
//
//  1. A store file exports state (and only the invariant-keeping helpers that
//     are meaningless apart from it). Network calls, formatting and DOM
//     plumbing belong in the composable that uses the store.
//  2. Every store exports `reset<Name>Store()`, the canonical way to return it
//     to its post-boot values. Tests use these instead of ad-hoc per-ref
//     teardown so a newly added ref can't silently leak between cases.
//  3. Injected wiring (lookup callbacks, scroll hooks) is installed once at
//     module load and is NOT state — resets leave it alone, otherwise a reset
//     would leave the app unwired rather than merely empty.
//  4. Composables outside this directory must not declare shared mutable state
//     at module scope. Module-scope constants, memo tables and per-deploy
//     caches of immutable server payloads are fine; anything a second module
//     reads or writes belongs in a store.
import { resetActiveConversationStore } from "./activeConversationStore";
import { resetAppChromeStore } from "./appChromeStore";
import { resetAgentActivityStore } from "./agentActivityStore";
import { resetAuthStore } from "./authStore";
import { resetChatContextStore } from "./chatContextStore";
import { resetChatAttachmentDraftStore } from "./chatAttachmentDraftStore";
import { resetChatMessageStore } from "./chatMessageStore";
import { resetChatQueueStore } from "./chatQueueStore";
import { resetChatStreamStore } from "./chatStreamStore";
import { resetConversationListStore } from "./conversationListStore";
import { resetConversationAttentionStore } from "./conversationAttentionStore";
import { resetUserStore } from "./userStore";
import { resetWsDeliveryStore } from "./wsDeliveryStore";

// resetChatStores clears everything the chat surface accumulates for one
// conversation. Tests that drive the WebSocket dispatcher directly (rather
// than going through switchToConversation, which resets in production) call
// this in beforeEach so no singleton carries over between cases.
export function resetChatStores() {
  resetActiveConversationStore();
  resetChatAttachmentDraftStore();
  resetChatMessageStore();
  resetChatStreamStore();
  resetChatQueueStore();
  resetChatContextStore();
  resetAgentActivityStore();
  resetWsDeliveryStore();
}

// resetAllStores additionally drops the app-shell data (conversation lists,
// agent roster, identity). Nothing in production calls it — it exists so a
// test that mounts a whole page can start from a known-empty app.
export function resetAllStores() {
  resetChatStores();
  resetAppChromeStore();
  resetConversationAttentionStore();
  resetConversationListStore();
  resetUserStore();
  resetAuthStore();
}
