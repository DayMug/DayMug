import { computed, type Ref } from "vue";
import type { RouteLocationNormalizedLoaded, Router } from "vue-router";

import { isHtmlFile } from "@/composables/useFileApi";

export interface UseWorkspacePreviewOptions {
  userId: Ref<string>;
  router: Router;
  route: RouteLocationNormalizedLoaded;
}

// useWorkspacePreview owns how the workspace panel opens files: the
// route-backed fullscreen preview overlay and the preview/editor "open in a
// new tab" routing. Instance-scoped (call inside setup()); the caller is
// responsible for registering `handleGlobalKeydown` on `document`.
export function useWorkspacePreview({ userId, router, route }: UseWorkspacePreviewOptions) {
  // Backed by `?preview=<path>` in the URL so a copied tab URL re-opens the same
  // preview, and the browser back button closes it.
  const fullscreenPreview = computed<string | null>({
    get() {
      const v = route.query.preview;
      return typeof v === "string" && v ? v : null;
    },
    set(value) {
      const current = typeof route.query.preview === "string" ? route.query.preview : null;
      const next = value && value.length > 0 ? value : null;
      if (next === current) return;
      const newQuery = { ...route.query };
      if (next === null) delete newQuery.preview;
      else newQuery.preview = next;
      router.push({ query: newQuery });
    },
  });

  function previewRoute(path: string, edit: "true" | "false" = "false") {
    return {
      name: "file",
      params: { userId: userId.value },
      query: { path, edit },
    } as const;
  }

  function openPreview(path: string, target: "new-tab" | "same-tab") {
    if (target === "same-tab") {
      // Opening a file normally inherits whether the page was editing the
      // previous one. An .html is the exception: it opens as the page it
      // renders to, because that is what someone double-clicking a web page
      // is asking to see. The workbench's Edit button is still one click away.
      const inherited = route.query.edit === "true" || route.query.edit === "1" ? "true" : "false";
      void router.push(previewRoute(path, isHtmlFile(path) ? "false" : inherited));
      return;
    }
    openPreviewInNewTab(path);
  }

  function openPreviewInNewTab(path: string) {
    const resolved = router.resolve(previewRoute(path));
    window.open(resolved.href, "_blank");
  }

  function openEditorInNewTab(path: string) {
    const resolved = router.resolve({
      name: "file",
      params: { userId: userId.value },
      query: { path, edit: "true" },
    });
    window.open(resolved.href, "_blank");
  }

  // Escape to close fullscreen preview
  function handleGlobalKeydown(e: KeyboardEvent) {
    if (e.key === "Escape" && fullscreenPreview.value) {
      fullscreenPreview.value = null;
    }
  }

  return {
    fullscreenPreview,
    previewRoute,
    openPreview,
    openPreviewInNewTab,
    openEditorInNewTab,
    handleGlobalKeydown,
  };
}
