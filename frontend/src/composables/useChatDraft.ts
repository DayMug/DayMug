import { onBeforeUnmount, watch, type Ref } from "vue";
import { loadDraft, saveDraft } from "@/lib/chatDrafts";

// Debounced so keystroke-by-keystroke localStorage thrash doesn't show up as
// input lag.
const DRAFT_SAVE_DEBOUNCE_MS = 300;

// useChatDraft keeps the unsent composer text per conversation in
// localStorage, so switching tabs or reloading doesn't lose what the user typed
// but hadn't sent. `chatDrafts` handles the bounds (LRU + per-entry length
// cap); this only wires up load-on-switch-in and save-on-edit.
//
// The debounced save is flushed synchronously on conversation switch and on
// unmount, because both would otherwise drop the last few keystrokes.
export function useChatDraft(conversationId: Ref<string>, text: Ref<string>): void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  // Which conversation a pending save belongs to. Reading conversationId at
  // fire time would mis-save to the *new* id after a fast conversation switch.
  let pendingConvId = "";

  function cancelPending() {
    if (timer) {
      clearTimeout(timer);
      timer = null;
    }
  }

  // `immediate` covers the initial mount, so a refresh restores the active
  // conversation's draft without a separate onMounted hook.
  watch(
    conversationId,
    (next, prev) => {
      if (prev && prev !== next) {
        // Capture the keystrokes for the conversation we're leaving before the
        // textarea content gets replaced below.
        cancelPending();
        saveDraft(prev, text.value);
      }
      pendingConvId = next ?? "";
      text.value = next ? loadDraft(next) : "";
    },
    { immediate: true },
  );

  watch(text, (next) => {
    const convId = conversationId.value;
    if (!convId) return;
    pendingConvId = convId;
    cancelPending();
    timer = setTimeout(() => {
      saveDraft(convId, next);
      timer = null;
    }, DRAFT_SAVE_DEBOUNCE_MS);
  });

  onBeforeUnmount(() => {
    cancelPending();
    if (pendingConvId) saveDraft(pendingConvId, text.value);
  });
}
