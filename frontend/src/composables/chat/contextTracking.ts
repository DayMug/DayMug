// Actions over the context-window state held in @/stores/chatContextStore:
// the localStorage mirror that survives a refresh, and the clear-context
// round-trip that rotates the underlying agent session.
import { loadConvMetaMap, saveConvMetaMap } from "@/lib/convMeta";

import {
  currentConversationId,
  currentModel,
  modelInfoLine,
} from "@/stores/activeConversationStore";
import { contextClearedAfterMessageId, contextUsage } from "@/stores/chatContextStore";
import { lastPersistedMessageId } from "@/stores/chatMessageStore";

import { clearConversationContext } from "../useApi";
import { pushActivity } from "./messageState";
import { errorMessage } from "@/lib/errorMessage";
import { i18n } from "@/i18n";

export type { ContextUsage, RateLimitInfo, RateLimitWindow } from "@/stores/chatContextStore";

export function persistConvMeta() {
  const id = currentConversationId.value;
  if (!id) return;
  const map = loadConvMetaMap();
  map[id] = {
    model: currentModel.value,
    contextUsage: contextUsage.value,
    modelInfo: modelInfoLine.value || undefined,
    contextClearedAfterMessageId: contextClearedAfterMessageId.value || undefined,
  };
  saveConvMetaMap(map);
}

// Divider activity emitted after a successful clear-context. Exported so the
// chat surface can detect it and suppress the inline trigger right after a
// fresh clear (no point offering "clear context" when the last thing we did
// was clear it).
export const CONTEXT_CLEARED_DIVIDER = "— context cleared, future messages start fresh —";

// clearContext rotates the underlying claude session id so the next user
// message starts a fresh CLI process with no prior context. The chat
// thread (DB + UI) is preserved and a divider activity is appended so
// the user can see *where* memory was reset. Refused while a turn is in
// flight — UI hides the trigger then, but we also surface server errors
// from a stale tab via the activity log. Returns false if the request
// failed so callers can react if needed.
export async function clearContext(): Promise<boolean> {
  const id = currentConversationId.value;
  if (!id) return false;
  try {
    await clearConversationContext(id);
  } catch (err) {
    pushActivity(
      "info",
      i18n.global.t("slashCommands.clearContextFailed", { error: errorMessage(err) }),
    );
    return false;
  }
  contextUsage.value = null;
  // Anchor the divider to the current tail so a refresh can put it back
  // in the same spot. Captured BEFORE pushActivity below — activity rows
  // are UI-only and would skew the lookup.
  contextClearedAfterMessageId.value = lastPersistedMessageId();
  // Past assistant rows keep their usage chip — that's history, not
  // pre-clear state. Only the context bar (token-bar at the top) is
  // wiped, and persistConvMeta flushes it from localStorage so a
  // refresh doesn't restore the pre-clear value.
  persistConvMeta();
  pushActivity("info", CONTEXT_CLEARED_DIVIDER);
  return true;
}
