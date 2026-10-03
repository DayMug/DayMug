import { ref, nextTick } from "vue";

// Options for useInlineRename. `onCommit` fires when the user accepts a
// non-empty new title that differs from the original; `focusInput` is invoked
// after the input has rendered so each consumer can scope the focus lookup
// however it likes (querySelector, ref-based, etc.).
export interface InlineRenameOptions {
  onCommit: (id: string, title: string) => void;
  focusInput?: () => void;
}

// useInlineRename centralizes the editing buffer + IME guard + commit/cancel
// trio that the conversation lists (desktop SessionListPanel, mobile
// AgentConversationList) both implemented locally. Behavior preserved:
//   - commit only when the trimmed title is non-empty AND we are still
//     editing the same id (guards against stale blur events).
//   - Enter mid-IME composition is ignored so a Chinese/Japanese candidate
//     selection doesn't accidentally submit.
export function useInlineRename({ onCommit, focusInput }: InlineRenameOptions) {
  const editingId = ref<string | null>(null);
  const editingTitle = ref("");
  // Tracks whether the rename input is mid-IME-composition. Pressing Enter
  // to commit a candidate (Chinese / Japanese / Korean) fires keydown.enter;
  // without this guard we'd treat that as "submit" and drop the in-flight
  // characters.
  const composing = ref(false);

  function start(id: string, currentTitle: string) {
    editingId.value = id;
    editingTitle.value = currentTitle ?? "";
    if (focusInput) {
      nextTick(focusInput);
    }
  }

  function commit(id: string) {
    const title = editingTitle.value.trim();
    if (title && editingId.value === id) {
      onCommit(id, title);
    }
    editingId.value = null;
  }

  function cancel() {
    editingId.value = null;
  }

  function onEnter(e: KeyboardEvent, id: string) {
    if (composing.value || e.isComposing) return;
    commit(id);
  }

  return {
    editingId,
    editingTitle,
    composing,
    start,
    commit,
    cancel,
    onEnter,
  };
}
