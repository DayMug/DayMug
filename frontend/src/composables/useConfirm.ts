import { ref } from "vue";

// Module-level singleton state for the app-wide confirmation dialog. A single
// ConfirmDialog instance (mounted at the app root) renders whatever is in
// `state`, so any component can call `confirm(...)` without owning a dialog of
// its own. We use a custom in-DOM dialog rather than the native
// window.confirm() because mobile Safari (iPhone/iPad) suppresses native
// alert/confirm/prompt dialogs inside web apps, leaving destructive actions
// silently un-confirmable.

export interface ConfirmOptions {
  title?: string;
  message: string;
  confirmText?: string;
  cancelText?: string;
  // "destructive" tints the confirm button red for delete/revoke/reset flows.
  variant?: "default" | "destructive";
}

interface ConfirmState extends ConfirmOptions {
  resolve: (value: boolean) => void;
}

const state = ref<ConfirmState | null>(null);

function confirm(options: ConfirmOptions): Promise<boolean> {
  // If a prior dialog is somehow still pending, resolve it as cancelled so the
  // earlier caller doesn't hang forever.
  if (state.value) state.value.resolve(false);
  return new Promise<boolean>((resolve) => {
    state.value = { ...options, resolve };
  });
}

function settle(result: boolean) {
  const current = state.value;
  if (!current) return;
  state.value = null;
  current.resolve(result);
}

export function useConfirm() {
  return { confirmState: state, confirm, settle };
}
