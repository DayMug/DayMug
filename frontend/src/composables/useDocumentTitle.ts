import { watch, onScopeDispose, type Ref } from "vue";
import { useRoute } from "vue-router";
import type { Conversation } from "./useApi";

const DEFAULT_TITLE = "DayMug";

function buildTitle(convTitle: string | undefined): string {
  const trimmed = (convTitle ?? "").trim();
  return trimmed === "" ? DEFAULT_TITLE : trimmed;
}

/**
 * Mirror the active conversation's title onto document.title so the browser
 * tab reflects the current chat. Falls back to "DayMug" when no conversation
 * is selected or the title is still empty (e.g. brand new sessions). Restores
 * the default title when the calling scope is disposed.
 *
 * Skips updating while the user is on a /settings/* route — those pages own
 * their own title via usePageTitle so the tab reflects the settings page
 * (Appearance, Account, …) instead of the previously active chat. Watching
 * route.path also makes the watcher re-fire on the way back into /chat so
 * the conversation title is restored even when neither id nor list changed.
 */
export function useDocumentTitle(
  conversations: Ref<Conversation[]>,
  currentConversationId: Ref<string>,
) {
  // useRoute() returns undefined when called outside a router context (tests
  // mount this composable directly inside an effectScope without installing
  // the router). Guard so the chat-title watcher still runs in that mode —
  // the /settings/* skip simply never triggers when no path is reactive.
  const route = useRoute();
  watch(
    [conversations, currentConversationId, () => route?.path ?? ""],
    ([convs, id, path]) => {
      if (typeof path === "string" && path.startsWith("/settings")) return;
      // Bare-layout pages (preview, editor, login, setup) own their own
      // document.title — e.g. PreviewPage shows the opened file's name. The
      // chat-title mirror must not clobber it: PreviewPage is a lazily
      // loaded route, so on a brand-new tab the route.path change that
      // completes its navigation can re-fire this watcher *after* the page
      // has set the filename, flipping the tab back to "DayMug" (it doesn't
      // recur on refresh because the chunk is cached and the page mounts
      // first). Skipping bare routes lets the page's title always win.
      if (route?.meta?.layout === "bare") return;
      if (!id) {
        document.title = DEFAULT_TITLE;
        return;
      }
      const active = convs.find((c) => c.id === id);
      document.title = buildTitle(active?.title);
    },
    { immediate: true, deep: true },
  );

  onScopeDispose(() => {
    document.title = DEFAULT_TITLE;
  });
}

/**
 * Set document.title for a non-chat route (e.g. settings detail page) so the
 * browser tab reflects the page the user is actually on. The title is
 * suffixed with the brand, and the previous title is restored when the
 * calling scope is disposed (i.e. the page is unmounted).
 *
 * Pair with useDocumentTitle's /settings/* skip: while a settings page is
 * mounted, the chat-title watcher stays out of the way; on unmount we reset
 * to the brand and the chat-title watcher re-asserts on the next route
 * change.
 */
export function usePageTitle(title: Ref<string> | (() => string)) {
  const get = typeof title === "function" ? title : () => title.value;
  watch(
    get,
    (next) => {
      const trimmed = (next ?? "").trim();
      document.title = trimmed === "" ? DEFAULT_TITLE : `${trimmed} · ${DEFAULT_TITLE}`;
    },
    { immediate: true },
  );
  onScopeDispose(() => {
    document.title = DEFAULT_TITLE;
  });
}

export const _internals = { buildTitle, DEFAULT_TITLE };
